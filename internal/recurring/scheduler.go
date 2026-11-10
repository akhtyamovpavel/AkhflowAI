package recurring

import (
	"context"
	"log"
	"os"
	"time"

	"akhflow.ai/akhflow/internal/events"
	"akhflow.ai/akhflow/internal/store"
)

type Scheduler struct {
	Store  *store.Store
	Bus    *events.Bus
	Tick   time.Duration
	Logger *log.Logger
}

func NewScheduler(st *store.Store, bus *events.Bus) *Scheduler {
	tick := 60 * time.Second
	if v := os.Getenv("RECURRING_TICK"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			tick = d
		}
	}
	return &Scheduler{
		Store:  st,
		Bus:    bus,
		Tick:   tick,
		Logger: log.Default(),
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	if s.Tick <= 0 {
		s.Logger.Println("recurring scheduler disabled")
		return
	}
	s.Logger.Printf("recurring scheduler: tick every %s", s.Tick)
	t := time.NewTicker(s.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.TickOnce(context.Background())
		}
	}
}

func (s *Scheduler) TickOnce(ctx context.Context) {
	now := time.Now()
	tasks, err := s.Store.ListRecurringOverdue(ctx, now)
	if err != nil {
		s.Logger.Printf("recurring: list overdue: %v", err)
		return
	}
	for _, t := range tasks {
		occ, err := s.Store.CreateScheduledOccurrence(ctx, t.ID, now)
		if err != nil {
			s.Logger.Printf("recurring: create occurrence for master %d: %v", t.ID, err)
			continue
		}
		if occ == nil {
			continue
		}
		eid := occ.ID
		if err := s.Store.Audit(ctx, "scheduler", "recur_task", "task", &eid, map[string]any{"master": t.ID, "due": occ.DueDate}); err != nil {
			s.Logger.Printf("recurring: audit: %v", err)
		}
		if s.Bus != nil {
			s.Bus.Publish(events.Event{Type: "task_created", ProjectID: occ.ProjectID, TaskID: occ.ID, Title: occ.Title, Actor: "scheduler"})
		}
	}
}
