package events

import (
	"testing"
	"time"
)

func TestPublisherDoesNotBlockWhenDatabaseIsUnavailable(t *testing.T) {
	publisher, err := NewPublisher("postgres://postgres@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < 20; i++ {
		publisher.Publish(Event{Type: "task_created", ProjectID: 1})
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("publishing blocked for %s", elapsed)
	}
	publisher.Close()
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("close blocked for %s", elapsed)
	}
}
