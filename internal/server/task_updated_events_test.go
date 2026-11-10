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
	"akhflow.ai/akhflow/internal/llm"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/toolbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTaskUpdateEvents(t *testing.T) {
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
	schema := fmt.Sprintf("task_events_%d", time.Now().UnixNano())
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
	user := func(email string) int64 {
		t.Helper()
		id, err := db.CreateUser(ctx, email, email, "hash")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	owner := user("owner@example.invalid")
	viewer := user("viewer@example.invalid")
	outsider := user("outsider@example.invalid")
	project, err := db.CreateProject(ctx, "events", "Events", owner)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateProject(ctx, "other", "Other", outsider)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.SetProjectMemberRole(ctx, project, owner, viewer, "viewer"); err != nil || !ok {
		t.Fatalf("viewer: %v", err)
	}
	taskID, err := db.CreateTask(ctx, domain.Task{ProjectID: project, AuthorID: owner, Title: "Original", Status: "backlog", Priority: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	sub := bus.Subscribe(project)
	defer bus.Unsubscribe(sub)
	otherSub := bus.Subscribe(other)
	defer bus.Unsubscribe(otherSub)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if req.Messages[len(req.Messages)-1].Role == "tool" {
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"OK"}}]}`)
			return
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"set_assignee","arguments":"{\"task_id\":%d,\"email\":\"viewer@example.invalid\"}"}}]}}]}`, taskID)
	}))
	defer model.Close()
	auth := &Auth{Store: db, Secret: []byte("test-only-signing-key"), TokenTTL: time.Hour}
	handler := New(db, auth, llm.NewClient(model.URL, "fake"), bus).Handler()
	request := func(actor int64, method, path, body string) int {
		t.Helper()
		token, err := auth.CreateToken(actor)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response.Code
	}
	check := func(title string, types ...string) {
		t.Helper()
		for _, kind := range types {
			select {
			case event := <-sub.Ch:
				if event.Type != kind || event.ProjectID != project || event.TaskID != taskID || event.Title != title || event.Actor != "owner@example.invalid" {
					t.Errorf("event: %+v, want %s with title %q", event, kind, title)
				}
				if kind == "task_moved" {
					stored, err := db.TaskByID(ctx, taskID)
					if err != nil || stored == nil || event.Status != stored.Status {
						t.Errorf("moved status: event=%q task=%+v error=%v", event.Status, stored, err)
					}
				}
			default:
				t.Errorf("missing %s event", kind)
			}
		}
		select {
		case event := <-sub.Ch:
			t.Errorf("unexpected extra event: %+v", event)
		default:
		}
		select {
		case event := <-otherSub.Ch:
			t.Errorf("event leaked to another project: %+v", event)
		default:
		}
	}
	path := fmt.Sprintf("/api/tasks/%d", taskID)
	if got := request(owner, "PATCH", path, `{"title":"Edited","description":"Details"}`); got != 200 {
		t.Fatalf("patch: %d", got)
	}
	check("Edited", "task_updated")
	if got := request(owner, "PATCH", path, `{"priority":"high","recurring":"weekly","estimate_min":10}`); got != 200 {
		t.Fatalf("patch: %d", got)
	}
	check("Edited", "task_updated")
	if got := request(owner, "PATCH", path, `{"status":"in_progress"}`); got != 200 {
		t.Fatalf("patch: %d", got)
	}
	check("Edited", "task_moved")
	if got := request(owner, "PATCH", path, `{"status":"todo","title":"Combined","recurring":""}`); got != 200 {
		t.Fatalf("patch: %d", got)
	}
	check("Combined", "task_moved", "task_updated")
	if got := request(owner, "PATCH", path, `{"status":"todo"}`); got != 200 {
		t.Fatalf("unchanged status: %d", got)
	}
	check("Combined")
	if got := request(owner, "PATCH", path, `{"status":"todo","description":"Changed"}`); got != 200 {
		t.Fatalf("mixed unchanged status: %d", got)
	}
	check("Combined", "task_updated")
	stored, err := db.TaskByID(ctx, taskID)
	if err != nil || stored.Description != "Changed" {
		t.Fatalf("mixed patch not saved: %+v, %v", stored, err)
	}
	move := toolbox.ByName("move_task")
	moveDeps := toolbox.Deps{Store: db, ActorID: owner, Actor: "owner@example.invalid", Bus: bus}
	moveArgs, _ := json.Marshal(map[string]any{"task_id": taskID, "status": "todo"})
	if _, err := move.Handler(ctx, moveDeps, moveArgs); err != nil {
		t.Fatal(err)
	}
	check("Combined")
	moveArgs, _ = json.Marshal(map[string]any{"task_id": taskID, "status": "in_progress"})
	if _, err := move.Handler(ctx, moveDeps, moveArgs); err != nil {
		t.Fatal(err)
	}
	check("Combined", "task_moved")
	if _, err := move.Handler(ctx, moveDeps, moveArgs); err != nil {
		t.Fatal(err)
	}
	check("Combined")
	token, err := auth.CreateToken(owner)
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 6; round++ {
		status := "backlog"
		if round%2 != 0 {
			status = "in_progress"
		}
		body := fmt.Sprintf(`{"status":%q}`, status)
		args, _ := json.Marshal(map[string]any{"task_id": taskID, "status": status})
		start := make(chan struct{})
		errors := make(chan error, 16)
		var workers sync.WaitGroup
		for worker := 0; worker < 16; worker++ {
			workers.Add(1)
			go func(useREST bool) {
				defer workers.Done()
				<-start
				if useREST {
					req := httptest.NewRequest("PATCH", path, strings.NewReader(body))
					req.Header.Set("Authorization", "Bearer "+token)
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, req)
					if response.Code != 200 {
						errors <- fmt.Errorf("patch status: %d", response.Code)
					}
					return
				}
				if _, err := move.Handler(ctx, moveDeps, args); err != nil {
					errors <- err
				}
			}(worker%2 == 0)
		}
		close(start)
		workers.Wait()
		close(errors)
		for err := range errors {
			t.Error(err)
		}
		check("Combined", "task_moved")
	}
	if got := request(owner, "PATCH", path, fmt.Sprintf(`{"assignee_id":%d}`, viewer)); got != 200 {
		t.Fatalf("patch assignee: %d", got)
	}
	check("Combined", "task_updated")
	if got := request(owner, "PATCH", path, `{}`); got != 200 {
		t.Fatalf("empty patch: %d", got)
	}
	check("Combined")
	if got := request(viewer, "PATCH", path, `{"title":"Forbidden"}`); got != 403 {
		t.Fatalf("viewer patch: %d", got)
	}
	if got := request(owner, "PATCH", path, `{"priority":"invalid"}`); got != 400 {
		t.Fatalf("invalid patch: %d", got)
	}
	check("Combined")
	tool := toolbox.ByName("set_assignee")
	deps := toolbox.Deps{Store: db, ActorID: owner, Actor: "owner@example.invalid", Bus: bus}
	args, _ := json.Marshal(map[string]any{"task_id": taskID, "email": "viewer@example.invalid"})
	if _, err := tool.Handler(ctx, deps, args); err != nil {
		t.Fatal(err)
	}
	check("Combined", "task_updated")
	args, _ = json.Marshal(map[string]any{"task_id": taskID, "email": ""})
	if _, err := tool.Handler(ctx, deps, args); err != nil {
		t.Fatal(err)
	}
	check("Combined", "task_updated")
	args, _ = json.Marshal(map[string]any{"task_id": taskID, "email": "outsider@example.invalid"})
	if _, err := tool.Handler(ctx, deps, args); err == nil {
		t.Fatal("outsider assignment unexpectedly succeeded")
	}
	check("Combined")
	viewerDeps := toolbox.Deps{Store: db, ActorID: viewer, Actor: "viewer@example.invalid", Bus: bus}
	args, _ = json.Marshal(map[string]any{"task_id": taskID, "email": "owner@example.invalid"})
	if _, err := tool.Handler(ctx, viewerDeps, args); err == nil {
		t.Fatal("viewer assignment unexpectedly succeeded")
	}
	check("Combined")
	if got := request(owner, "POST", "/api/agent/chat", `{"message":"Assign the task"}`); got != 200 {
		t.Fatalf("agent: %d", got)
	}
	check("Combined", "task_updated")
}
