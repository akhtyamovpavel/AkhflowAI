package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/domain"
	"akhflow.ai/akhflow/internal/events"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/toolbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRecurringCompletionAtomicity(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_ISOLATION_TEST_DSN")
	if dsn == "" {
		t.Skip("AKHFLOW_ISOLATION_TEST_DSN is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("recurring_completion_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := store.New(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	inspect, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer inspect.Close()
	owner, err := db.CreateUser(ctx, "owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "recurring", "Recurring", owner)
	if err != nil {
		t.Fatal(err)
	}
	auth := &Auth{Store: db, Secret: []byte("test-signing-key"), TokenTTL: time.Hour}
	token, err := auth.CreateToken(owner)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	sub := bus.Subscribe(project)
	defer bus.Unsubscribe(sub)
	handler := New(db, auth, nil, bus).Handler()
	deps := toolbox.Deps{Store: db, ActorID: owner, Actor: "owner@example.invalid", Bus: bus}
	patch := func(id int64, body string) error {
		req := httptest.NewRequest("PATCH", fmt.Sprintf("/api/tasks/%d", id), strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			return fmt.Errorf("PATCH status %d: %s", response.Code, response.Body.String())
		}
		return nil
	}
	move := func(id int64) error {
		args, _ := json.Marshal(map[string]any{"task_id": id, "status": "done"})
		_, err := toolbox.ByName("move_task").Handler(ctx, deps, args)
		return err
	}
	create := func(title string) int64 {
		t.Helper()
		due := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
		task := domain.Task{ProjectID: project, AuthorID: owner, Title: title, Status: "todo", Priority: "high", DueDate: &due, Recurring: "daily", Labels: []string{"recurring-label"}}
		id, err := db.CreateTask(ctx, task)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	count := func(title string) int {
		t.Helper()
		var n int
		if err := inspect.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE project_id=$1 AND title=$2`, project, title).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	task := create("Concurrent")
	const workers = 16
	start := make(chan struct{})
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			if index%2 == 0 {
				errs <- patch(task, `{"status":"done"}`)
			} else {
				errs <- move(task)
			}
		}(i)
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent completion: %v", err)
		}
	}
	if err := move(task); err != nil {
		t.Fatal(err)
	}
	if err := patch(task, `{"status":"done"}`); err != nil {
		t.Fatal(err)
	}
	if got := count("Concurrent"); got != 2 {
		t.Errorf("concurrent and repeated completion created %d tasks, want original and one next", got)
	}
	var labels int
	if err := inspect.QueryRow(ctx, `SELECT count(*) FROM tasks t JOIN task_labels l ON l.task_id=t.id WHERE t.title=$1 AND t.id<>$2 AND l.label=$3`, "Concurrent", task, "recurring-label").Scan(&labels); err != nil {
		t.Fatal(err)
	}
	if labels != 1 {
		t.Errorf("next occurrence labels: %d", labels)
	}
	var moved, created int
	for len(sub.Ch) > 0 {
		event := <-sub.Ch
		if event.Type == "task_moved" && event.TaskID == task {
			moved++
		}
		if event.Type == "task_created" && event.Title == "Concurrent" {
			created++
		}
	}
	if moved != 1 || created != 1 {
		t.Errorf("events: moved=%d created=%d, want one each", moved, created)
	}
	if err := patch(task, `{"status":"todo"}`); err != nil {
		t.Fatal(err)
	}
	if err := move(task); err != nil {
		t.Fatal(err)
	}
	if got := count("Concurrent"); got != 2 {
		t.Errorf("reopening created another next occurrence: %d tasks", got)
	}

	mixed := create("Before mixed patch")
	epic, err := db.CreateEpic(ctx, project, "Epic", "", "open")
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"status":"done","title":"After mixed patch","recurring":"weekly","epic_id":%d}`, epic)
	if err := patch(mixed, body); err != nil {
		t.Fatal(err)
	}
	var nextID, nextEpic int64
	var recurrence string
	var due time.Time
	err = inspect.QueryRow(ctx, `SELECT id, epic_id, recurring, due_date FROM tasks WHERE title=$1 AND id<>$2`, "After mixed patch", mixed).Scan(&nextID, &nextEpic, &recurrence, &due)
	if err != nil {
		t.Fatal(err)
	}
	if nextEpic != epic || recurrence != "weekly" || due.Format(time.DateOnly) != "2026-11-17" {
		t.Errorf("next occurrence did not preserve mixed update: epic=%d recurrence=%q due=%s", nextEpic, recurrence, due)
	}
	for _, method := range []string{"REST", "MCP"} {
		title := "Monthly " + method
		monthly := create(title)
		january := time.Date(2028, time.January, 31, 0, 0, 0, 0, time.UTC)
		err := db.UpdateTask(ctx, monthly, []string{"due_date", "recurring"}, []any{january, "monthly"})
		if err != nil {
			t.Fatal(err)
		}
		if method == "REST" {
			err = patch(monthly, `{"status":"done"}`)
		} else {
			err = move(monthly)
		}
		if err != nil {
			t.Fatal(err)
		}
		var nextDue time.Time
		err = inspect.QueryRow(ctx, `SELECT due_date FROM tasks WHERE title=$1 AND id<>$2`, title, monthly).Scan(&nextDue)
		if err != nil {
			t.Fatal(err)
		}
		if nextDue.Format(time.DateOnly) != "2028-02-29" {
			t.Errorf("%s monthly completion due = %s, want 2028-02-29", method, nextDue.Format(time.DateOnly))
		}
	}

	failure := create("Failure")
	if err := db.SetEmailNotifications(ctx, owner, true); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateTask(ctx, failure, []string{"assignee_id"}, []any{owner}); err != nil {
		t.Fatal(err)
	}
	for len(sub.Ch) > 0 {
		<-sub.Ch
	}
	_, err = inspect.Exec(ctx, `CREATE FUNCTION reject_recurrence() RETURNS trigger AS $$ BEGIN IF NEW.title = 'Failure' THEN RAISE EXCEPTION 'forced failure'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inspect.Exec(ctx, `CREATE TRIGGER reject_recurrence BEFORE INSERT ON tasks FOR EACH ROW EXECUTE FUNCTION reject_recurrence()`); err != nil {
		t.Fatal(err)
	}
	if err := patch(failure, `{"status":"done"}`); err == nil {
		t.Error("REST completion succeeded despite failed occurrence creation")
	}
	if err := move(failure); err == nil {
		t.Error("tool completion succeeded despite failed occurrence creation")
	}
	stored, err := db.TaskByID(ctx, failure)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "todo" || count("Failure") != 1 {
		t.Errorf("failed completion persisted: task=%+v", stored)
	}
	if len(sub.Ch) != 0 {
		t.Error("failed completion published an event")
	}
	var queued int
	if err := inspect.QueryRow(ctx, `SELECT count(*) FROM email_notifications WHERE task_id=$1`, failure).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Errorf("failed completion queued %d emails", queued)
	}
	if _, err := inspect.Exec(ctx, `DROP TRIGGER reject_recurrence ON tasks`); err != nil {
		t.Fatal(err)
	}
	if err := move(failure); err != nil {
		t.Fatal(err)
	}
	if got := count("Failure"); got != 2 {
		t.Errorf("retry after failure: %d tasks, want 2", got)
	}

	for i := 0; i < 12; i++ {
		title := fmt.Sprintf("Scheduler race %d", i)
		due := time.Now().AddDate(0, 0, -3)
		master, err := db.CreateTask(ctx, domain.Task{ProjectID: project, AuthorID: owner, Title: title, Status: "todo", Priority: "medium", DueDate: &due, Recurring: "daily"})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			_, err := db.CreateScheduledOccurrence(ctx, master, time.Now())
			results <- err
		}()
		go func() {
			<-start
			results <- patch(master, `{"status":"done"}`)
		}()
		close(start)
		for j := 0; j < 2; j++ {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		var masters, occurrences int
		err = inspect.QueryRow(ctx, `SELECT count(*) FILTER (WHERE recurring<>''), count(*) FILTER (WHERE recurring='') FROM tasks WHERE title=$1`, title).Scan(&masters, &occurrences)
		if err != nil {
			t.Fatal(err)
		}
		if masters != 2 || occurrences > 1 {
			t.Errorf("scheduler and completion race: recurring=%d one-off=%d", masters, occurrences)
		}
		stored, err := db.TaskByID(ctx, master)
		if err != nil || stored == nil || stored.Status != "done" {
			t.Fatalf("master after scheduler race: %+v, %v", stored, err)
		}
	}

	legacy := create("Legacy")
	legacyNext := create("Legacy")
	if err := db.Audit(ctx, "owner@example.invalid", "recur_task", "task", &legacyNext, map[string]any{"from": legacy}); err != nil {
		t.Fatal(err)
	}
	if _, err := inspect.Exec(ctx, `ALTER TABLE tasks DROP COLUMN recurrence_spawned`); err != nil {
		t.Fatal(err)
	}
	if _, err := inspect.Exec(ctx, `DELETE FROM schema_migrations WHERE name='017_recurring_completion.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := move(legacy); err != nil {
		t.Fatal(err)
	}
	if got := count("Legacy"); got != 2 {
		t.Errorf("migration did not preserve existing chain: %d tasks", got)
	}
}
