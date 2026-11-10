package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"regexp"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const eventChannel = "akhflow_mcp_events"
const maxEventBytes = 6000
const eventQueueSize = 256
const cleanupSQL = `DELETE FROM mcp_events WHERE created_at < now() - interval '5 minutes'
	OR id IN (SELECT id FROM mcp_events ORDER BY created_at DESC OFFSET 10000)`

var eventIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Publisher never waits for PostgreSQL in the mutation path. Its bounded queue
// can drop notifications under pressure, as can the SSE subscriber queues.
type Publisher struct {
	mu     sync.Mutex
	queue  chan Event
	closed bool
	cancel context.CancelFunc
	done   chan struct{}
}

func NewPublisher(dsn string) (*Publisher, error) {
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Publisher{queue: make(chan Event, eventQueueSize), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		defer pool.Close()
		lastCleanup := time.Time{}
		for event := range p.queue {
			if ctx.Err() != nil {
				return
			}
			if err := sendEvent(ctx, pool, event); err != nil {
				log.Print("MCP event delivery failed")
			}
			if time.Since(lastCleanup) >= time.Minute {
				cleanupCtx, cancel := context.WithTimeout(ctx, time.Second)
				_, _ = pool.Exec(cleanupCtx, cleanupSQL)
				cancel()
				lastCleanup = time.Now()
			}
		}
	}()
	return p, nil
}

func (p *Publisher) Publish(event Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	select {
	case p.queue <- event:
	default:
	}
}

// Close drains pending events briefly, including when an MCP stdio client exits.
func (p *Publisher) Close() {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.queue)
	}
	p.mu.Unlock()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		p.cancel()
		<-p.done
	}
	p.cancel()
}

func sendEvent(parent context.Context, pool *pgxpool.Pool, event Event) error {
	if event.ProjectID <= 0 || event.Type == "" {
		return errors.New("invalid event")
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if len(payload) > maxEventBytes {
		event.Title = ""
		event.Actor = ""
		payload, err = json.Marshal(event)
		if err != nil || len(payload) > maxEventBytes {
			return errors.New("event too large")
		}
	}
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	id := hex.EncodeToString(key)
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	_, err = pool.Exec(ctx, `WITH inserted AS (
		INSERT INTO mcp_events(id, event) VALUES($1, $2::jsonb) RETURNING id
	) SELECT pg_notify('akhflow_mcp_events', id) FROM inserted`, id, string(payload))
	return err
}

// Listen starts with an established LISTEN subscription, then reconnects after
// database disconnects. Notifications during a disconnect are not replayed.
func Listen(ctx context.Context, dsn string, bus *Bus) error {
	connectCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	conn, err := connectListener(connectCtx, dsn)
	if err == nil {
		_, err = conn.Exec(connectCtx, "LISTEN "+eventChannel)
	}
	cancel()
	if err != nil {
		if conn != nil {
			_ = conn.Close(context.Background())
		}
		go listenLoop(ctx, dsn, nil, bus)
		return err
	}
	go listenLoop(ctx, dsn, conn, bus)
	return nil
}

func connectListener(ctx context.Context, dsn string) (*pgx.Conn, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	config.RuntimeParams["application_name"] = "akhflow-events-" + config.RuntimeParams["search_path"]
	return pgx.ConnectConfig(ctx, config)
}

func listenLoop(ctx context.Context, dsn string, conn *pgx.Conn, bus *Bus) {
	seen := map[string]time.Time{}
	lastCleanup := time.Time{}
	defer func() {
		if conn != nil {
			_ = conn.Close(context.Background())
		}
	}()
	for ctx.Err() == nil {
		if conn == nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			connectCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			var err error
			conn, err = connectListener(connectCtx, dsn)
			if err == nil {
				_, err = conn.Exec(connectCtx, "LISTEN "+eventChannel)
			}
			cancel()
			if err != nil {
				if conn != nil {
					_ = conn.Close(context.Background())
				}
				conn = nil
				continue
			}
		}
		if time.Since(lastCleanup) >= time.Minute {
			cleanupCtx, cancel := context.WithTimeout(ctx, time.Second)
			_, _ = conn.Exec(cleanupCtx, cleanupSQL)
			cancel()
			lastCleanup = time.Now()
			for id, at := range seen {
				if time.Since(at) >= 5*time.Minute {
					delete(seen, id)
				}
			}
		}
		waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		n, err := conn.WaitForNotification(waitCtx)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			continue
		}
		if err != nil {
			_ = conn.Close(context.Background())
			conn = nil
			continue
		}
		if !eventIDPattern.MatchString(n.Payload) {
			continue
		}
		if _, exists := seen[n.Payload]; exists {
			continue
		}
		queryCtx, cancel := context.WithTimeout(ctx, time.Second)
		var data []byte
		err = conn.QueryRow(queryCtx, `SELECT event FROM mcp_events WHERE id=$1 AND created_at >= now() - interval '5 minutes'`, n.Payload).Scan(&data)
		cancel()
		if err != nil {
			continue
		}
		var event Event
		if len(data) > 2*maxEventBytes || json.Unmarshal(data, &event) != nil || event.ProjectID <= 0 || event.Type == "" {
			continue
		}
		if len(seen) >= 10000 {
			for id := range seen {
				delete(seen, id)
				break
			}
		}
		seen[n.Payload] = time.Now()
		bus.Publish(event)
	}
}
