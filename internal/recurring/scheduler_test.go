package recurring

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/domain"
	"akhflow.ai/akhflow/internal/events"
	"akhflow.ai/akhflow/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

type schedulerFixture struct {
	db     *store.Store
	pool   *pgxpool.Pool
	dsn    string
	master int64
	due    time.Time
}

func nextDailyDue(due time.Time, now time.Time) time.Time {
	next := time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, time.UTC)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for !next.After(today) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func newSchedulerFixture(t *testing.T) schedulerFixture {
	t.Helper()
	dsn := os.Getenv("AKHFLOW_RECURRING_TEST_DSN")
	if dsn == "" {
		t.Skip("set AKHFLOW_RECURRING_TEST_DSN to an isolated PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("recurring_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	scoped := parsed.String()
	db, err := store.New(scoped)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	owner, err := db.CreateUser(ctx, "scheduler@example.invalid", "Scheduler", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "scheduler", "Scheduler", owner)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	due := time.Date(now.Year(), now.Month(), now.Day()-3, 0, 0, 0, 0, now.Location())
	task := domain.Task{ProjectID: project, AuthorID: owner, Title: "Master", Description: "Details", Status: domain.StatusTodo, Priority: domain.PriorityHigh, Recurring: "daily", DueDate: &due, Labels: []string{"test-label"}}
	master, err := db.CreateTask(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	return schedulerFixture{db: db, pool: pool, dsn: scoped, master: master, due: due}
}

func (f schedulerFixture) check(t *testing.T, count int, due time.Time) {
	t.Helper()
	ctx := context.Background()
	var actualCount int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE recurring=''`).Scan(&actualCount); err != nil {
		t.Fatal(err)
	}
	if actualCount != count {
		t.Errorf("occurrences = %d, want %d", actualCount, count)
	}
	var actualDue time.Time
	if err := f.pool.QueryRow(ctx, `SELECT due_date FROM tasks WHERE id=$1`, f.master).Scan(&actualDue); err != nil {
		t.Fatal(err)
	}
	if actualDue.Format(time.DateOnly) != due.Format(time.DateOnly) {
		t.Errorf("master due = %s, want %s", actualDue.Format(time.DateOnly), due.Format(time.DateOnly))
	}
}

func TestSchedulerRollbackAndRetry(t *testing.T) {
	f := newSchedulerFixture(t)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `CREATE FUNCTION fail_occurrence() RETURNS trigger AS $$
		BEGIN IF NEW.recurring = '' THEN RAISE EXCEPTION 'injected insert failure'; END IF; RETURN NEW; END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER fail_occurrence BEFORE INSERT ON tasks FOR EACH ROW EXECUTE FUNCTION fail_occurrence()`)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	sub := bus.Subscribe(0)
	defer bus.Unsubscribe(sub)
	scheduler := NewScheduler(f.db, bus)
	scheduler.Logger = log.New(io.Discard, "", 0)
	scheduler.TickOnce(ctx)
	f.check(t, 0, f.due)
	select {
	case event := <-sub.Ch:
		t.Errorf("event for failed insertion: %+v", event)
	default:
	}
	if _, err := f.pool.Exec(ctx, "DROP TRIGGER fail_occurrence ON tasks"); err != nil {
		t.Fatal(err)
	}
	scheduler.TickOnce(ctx)
	nextDue := nextDailyDue(f.due, time.Now())
	f.check(t, 1, nextDue)
	var title, description, status, priority, label string
	err = f.pool.QueryRow(ctx, `SELECT t.title, t.description, t.status, t.priority, l.label FROM tasks t JOIN task_labels l ON l.task_id=t.id WHERE t.recurring=''`).Scan(&title, &description, &status, &priority, &label)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Master" || description != "Details" || status != "todo" || priority != "high" || label != "test-label" {
		t.Errorf("occurrence did not preserve fields: %q %q %q %q %q", title, description, status, priority, label)
	}
	select {
	case event := <-sub.Ch:
		if event.Type != "task_created" || event.Title != "Master" {
			t.Errorf("unexpected event: %+v", event)
		}
	default:
		t.Error("missing occurrence event")
	}
	scheduler.TickOnce(ctx)
	f.check(t, 1, nextDue)
}

func TestSchedulerWorker(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_RECURRING_WORKER_DSN")
	if dsn == "" {
		t.Skip("worker process only")
	}
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	scheduler := NewScheduler(db, nil)
	scheduler.Logger = log.New(io.Discard, "", 0)
	scheduler.TickOnce(context.Background())
}

func TestSchedulerRollsBackOccurrenceWhenAdvanceFails(t *testing.T) {
	f := newSchedulerFixture(t)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `CREATE FUNCTION fail_advance() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'injected update failure'; END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER fail_advance BEFORE UPDATE OF due_date ON tasks FOR EACH ROW EXECUTE FUNCTION fail_advance()`)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewScheduler(f.db, nil)
	scheduler.Logger = log.New(io.Discard, "", 0)
	scheduler.TickOnce(ctx)
	f.check(t, 0, f.due)
}

func TestSchedulerUsesCalendarDateForAdvancement(t *testing.T) {
	f := newSchedulerFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE tasks SET due_date='2030-01-01' WHERE id=$1`, f.master); err != nil {
		t.Fatal(err)
	}
	location := time.FixedZone("UTC+3", 3*60*60)
	now := time.Date(2030, time.January, 2, 1, 0, 0, 0, location)
	first, err := f.db.CreateScheduledOccurrence(ctx, f.master, now)
	if err != nil || first == nil {
		t.Fatalf("first occurrence: %+v, %v", first, err)
	}
	second, err := f.db.CreateScheduledOccurrence(ctx, f.master, now)
	if err != nil || second != nil {
		t.Fatalf("duplicate occurrence: %+v, %v", second, err)
	}
	next := time.Date(2030, time.January, 3, 0, 0, 0, 0, location)
	f.check(t, 1, next)
}

func TestConcurrentSchedulerProcesses(t *testing.T) {
	f := newSchedulerFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := f.pool.Exec(ctx, `CREATE FUNCTION delay_master() RETURNS trigger AS $$
		BEGIN PERFORM pg_sleep(0.4); RETURN NEW; END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER delay_master BEFORE UPDATE OF due_date ON tasks FOR EACH ROW EXECUTE FUNCTION delay_master()`)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errors := make(chan string, 6)
	var workers sync.WaitGroup
	for i := 0; i < 6; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSchedulerWorker$", "-test.count=1")
			cmd.Env = append(os.Environ(), "AKHFLOW_RECURRING_WORKER_DSN="+f.dsn)
			<-start
			output, err := cmd.CombinedOutput()
			if err != nil {
				errors <- fmt.Sprintf("worker failed: %v: %s", err, strings.TrimSpace(string(output)))
			}
		}()
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	f.check(t, 1, nextDailyDue(f.due, time.Now()))
}
