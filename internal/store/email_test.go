package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/domain"
	"akhflow.ai/akhflow/internal/testdb"
)

func TestEmailQueueIntegration(t *testing.T) {
	dsn := testdb.Schema(t, "TEST_DATABASE_URL")
	ctx := context.Background()
	s, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	assigneeEmail := fmt.Sprintf("assignee-%d@example.invalid", suffix)
	user, err := s.CreateUser(ctx, assigneeEmail, "Assignee", "hash")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateUser(ctx, fmt.Sprintf("outsider-%d@example.invalid", suffix), "Outsider", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateProject(ctx, fmt.Sprintf("email-%d", suffix), "Test", user)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask(ctx, domain.Task{ProjectID: project, Title: "Test task", Status: "backlog", Priority: "low", AuthorID: user, AssigneeID: &user})
	if err != nil {
		t.Fatal(err)
	}
	move := func(status string) {
		t.Helper()
		if err := s.UpdateTask(ctx, task, []string{"status"}, []any{status}); err != nil {
			t.Fatal(err)
		}
	}
	claim := func() *EmailNotification {
		t.Helper()
		item, err := s.ClaimEmailNotification(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	move("todo")
	if item := claim(); item != nil {
		t.Fatal("unsubscribed user received a notification")
	}
	if err := s.SetEmailNotifications(ctx, user, true); err != nil {
		t.Fatal(err)
	}
	move("todo")
	if item := claim(); item != nil {
		t.Fatal("unchanged status created a notification")
	}
	move("in_progress")
	item := claim()
	if item == nil || item.TaskID != task || item.OldStatus != "todo" || item.NewStatus != "in_progress" || item.Recipient != assigneeEmail {
		t.Fatalf("wrong notification: %+v", item)
	}
	if item := claim(); item != nil {
		t.Fatal("leased notification was claimed twice")
	}
	if err := s.DeleteEmailNotification(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	move("done")
	if err := s.SetEmailNotifications(ctx, user, false); err != nil {
		t.Fatal(err)
	}
	if item := claim(); item != nil {
		t.Fatal("unsubscribing did not clear pending notification")
	}
	if err := s.SetEmailNotifications(ctx, other, true); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTask(ctx, task, []string{"assignee_id"}, []any{other}); err != nil {
		t.Fatal(err)
	}
	move("todo")
	if item := claim(); item != nil {
		t.Fatal("non-member assignee received a notification")
	}
}
