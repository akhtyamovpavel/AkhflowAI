package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/domain"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/testdb"
	"akhflow.ai/akhflow/internal/toolbox"
)

func TestEmailSettingsAndStatusChanges(t *testing.T) {
	dsn := testdb.Schema(t, "TEST_DATABASE_URL")
	ctx := context.Background()
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	user, err := db.CreateUser(ctx, fmt.Sprintf("email-%d@example.invalid", suffix), "Member", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, fmt.Sprintf("email-%d", suffix), "Test", user)
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.CreateTask(ctx, domain.Task{ProjectID: project, Title: "HTTP task", Status: "backlog", Priority: "low", AuthorID: user, AssigneeID: &user})
	if err != nil {
		t.Fatal(err)
	}
	auth := &Auth{Store: db, Secret: []byte("test-only-signing-secret"), TokenTTL: time.Hour}
	token, err := auth.CreateToken(user)
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, auth, nil, nil)
	s.EmailAvailable = true
	handler := s.Handler()
	request := func(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if authenticated {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	if got := request("GET", "/api/settings/email-notifications", "", false).Code; got != http.StatusUnauthorized {
		t.Fatalf("anonymous settings request: %d", got)
	}
	if got := request("PUT", "/api/settings/email-notifications", `{"enabled":"yes"}`, true).Code; got != http.StatusBadRequest {
		t.Fatalf("invalid setting: %d", got)
	}
	if got := request("PUT", "/api/settings/email-notifications", `{"enabled":true}`, true).Code; got != http.StatusOK {
		t.Fatalf("enable notifications: %d", got)
	}
	path := fmt.Sprintf("/api/tasks/%d", task)
	if got := request("PATCH", path, `{"status":"todo"}`, true).Code; got != http.StatusOK {
		t.Fatalf("status update: %d", got)
	}
	item, err := db.ClaimEmailNotification(ctx)
	if err != nil || item == nil || item.TaskID != task || item.NewStatus != "todo" {
		t.Fatalf("REST notification: item=%+v err=%v", item, err)
	}
	if err := db.DeleteEmailNotification(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"task_id": task, "status": "in_progress"})
	result, err := toolbox.ByName("move_task").Handler(ctx, toolbox.Deps{Store: db, ActorID: user, Actor: "test"}, args)
	if err != nil || !result.Ok {
		t.Fatalf("toolbox move: result=%+v err=%v", result, err)
	}
	item, err = db.ClaimEmailNotification(ctx)
	if err != nil || item == nil || item.NewStatus != "in_progress" || !strings.HasSuffix(item.Recipient, "@example.invalid") {
		t.Fatalf("MCP notification: item=%+v err=%v", item, err)
	}
}
