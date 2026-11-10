package events

import (
	"sync"
	"time"
)

type Event struct {
	Type      string    `json:"type"` // task_created, task_moved, task_updated, task_comment, dependency_added, dependency_removed
	ProjectID int64     `json:"project_id"`
	TaskID    int64     `json:"task_id,omitempty"`
	Title     string    `json:"title,omitempty"`
	Status    string    `json:"status,omitempty"`
	Actor     string    `json:"actor,omitempty"`
	At        time.Time `json:"at"`
}

type Subscription struct {
	ProjectID int64
	Ch        chan Event
}

// Bus is an in-memory pub/sub for project-scoped events (SSE notifications).
// Publish never blocks: slow subscribers drop events.
type Bus struct {
	mu   sync.RWMutex
	subs []*Subscription
}

func NewBus() *Bus { return &Bus{} }

// Subscribe returns a subscription for one project; projectID 0 subscribes to all projects.
func (b *Bus) Subscribe(projectID int64) *Subscription {
	s := &Subscription{ProjectID: projectID, Ch: make(chan Event, 64)}
	b.mu.Lock()
	b.subs = append(b.subs, s)
	b.mu.Unlock()
	return s
}

func (b *Bus) Unsubscribe(s *Subscription) {
	b.mu.Lock()
	for i, x := range b.subs {
		if x == s {
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			break
		}
	}
	b.mu.Unlock()
}

func (b *Bus) Publish(e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, s := range b.subs {
		if s.ProjectID != 0 && s.ProjectID != e.ProjectID {
			continue
		}
		select {
		case s.Ch <- e:
		default:
		}
	}
}
