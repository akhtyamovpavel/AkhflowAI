package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/toolbox"
)

func TestAssigneeMembership(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_ASSIGNEE_TEST_DSN")
	if dsn == "" {
		t.Skip("set AKHFLOW_ASSIGNEE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("assignee_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	dsnURL, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := dsnURL.Query()
	query.Set("search_path", schema)
	dsnURL.RawQuery = query.Encode()
	dsn = dsnURL.String()
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := time.Now().UnixNano()
	ownerEmail := fmt.Sprintf("owner-%d@example.invalid", suffix)
	memberEmail := fmt.Sprintf("member-%d@example.invalid", suffix)
	outsiderEmail := fmt.Sprintf("outsider-%d@example.invalid", suffix)
	owner, err := db.CreateUser(ctx, ownerEmail, "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	member, err := db.CreateUser(ctx, memberEmail, "Member", "hash")
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := db.CreateUser(ctx, outsiderEmail, "Outsider", "hash")
	if err != nil {
		t.Fatal(err)
	}
	slug := fmt.Sprintf("assignee-%d", suffix)
	project, err := db.CreateProject(ctx, slug, "Test", owner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO project_members(project_id, user_id) VALUES($1, $2)`, project, member)
	if err != nil {
		t.Fatal(err)
	}
	auth := &Auth{Store: db, Secret: []byte("test-only-signing-secret"), TokenTTL: time.Hour}
	token, err := auth.CreateToken(owner)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(db, auth, nil, nil).Handler()
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	checkAssignee := func(id int64, want *int64) {
		t.Helper()
		task, err := db.TaskByID(ctx, id)
		if err != nil || task == nil {
			t.Fatalf("load task: task=%+v err=%v", task, err)
		}
		if want == nil {
			if task.AssigneeID != nil {
				t.Fatalf("expected no assignee, got %d", *task.AssigneeID)
			}
			return
		}
		if task.AssigneeID == nil || *task.AssigneeID != *want {
			t.Fatalf("assignee: got %v, want %d", task.AssigneeID, *want)
		}
	}
	path := "/api/projects/" + slug + "/tasks"
	response := request(http.MethodPost, path, map[string]any{"title": "denied", "assignee_id": outsider})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("create with outsider: %d %s", response.Code, response.Body.String())
	}
	response = request(http.MethodPost, path, map[string]any{"title": "allowed", "assignee_id": member})
	if response.Code != http.StatusCreated {
		t.Fatalf("create with member: %d %s", response.Code, response.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	checkAssignee(created.ID, &member)
	taskPath := fmt.Sprintf("/api/tasks/%d", created.ID)
	for _, value := range []any{outsider, -1, 1.5, "invalid"} {
		response = request(http.MethodPatch, taskPath, map[string]any{"assignee_id": value, "title": "changed"})
		if response.Code != http.StatusBadRequest {
			t.Fatalf("patch assignee %v: %d %s", value, response.Code, response.Body.String())
		}
		checkAssignee(created.ID, &member)
		task, err := db.TaskByID(ctx, created.ID)
		if err != nil || task.Title != "allowed" {
			t.Fatalf("invalid assignment changed another field: task=%+v err=%v", task, err)
		}
	}
	response = request(http.MethodPatch, taskPath, map[string]any{"assignee_id": nil})
	if response.Code != http.StatusOK {
		t.Fatalf("clear assignee: %d %s", response.Code, response.Body.String())
	}
	checkAssignee(created.ID, nil)
	response = request(http.MethodPatch, taskPath, map[string]any{"assignee_id": owner})
	if response.Code != http.StatusOK {
		t.Fatalf("patch member assignee: %d %s", response.Code, response.Body.String())
	}
	checkAssignee(created.ID, &owner)

	deps := toolbox.Deps{Store: db, ActorID: owner, Actor: ownerEmail}
	call := func(name string, args map[string]any) (toolbox.Result, error) {
		t.Helper()
		payload, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		return toolbox.ByName(name).Handler(ctx, deps, payload)
	}
	_, err = call("create_task", map[string]any{"project": slug, "title": "denied tool", "assignee": outsiderEmail})
	if err == nil {
		t.Fatal("tool create accepted non-member")
	}
	result, err := call("create_task", map[string]any{"project": slug, "title": "allowed tool", "assignee": memberEmail})
	if err != nil || !result.Ok {
		t.Fatalf("tool create member: result=%+v err=%v", result, err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("tool create returned unexpected data: %T", result.Data)
	}
	toolTaskID, ok := data["id"].(int64)
	if !ok {
		t.Fatalf("tool create returned invalid ID: %v", data["id"])
	}
	checkAssignee(toolTaskID, &member)
	_, err = call("set_assignee", map[string]any{"task_id": created.ID, "email": outsiderEmail})
	if err == nil {
		t.Fatal("tool set_assignee accepted non-member")
	}
	checkAssignee(created.ID, &owner)
	result, err = call("set_assignee", map[string]any{"task_id": created.ID, "email": memberEmail})
	if err != nil || !result.Ok {
		t.Fatalf("tool set member: result=%+v err=%v", result, err)
	}
	checkAssignee(created.ID, &member)
	foreignDeps := toolbox.Deps{Store: db, ActorID: outsider, Actor: outsiderEmail}
	foreignArgs, err := json.Marshal(map[string]any{"task_id": created.ID, "email": ownerEmail})
	if err != nil {
		t.Fatal(err)
	}
	_, err = toolbox.ByName("set_assignee").Handler(ctx, foreignDeps, foreignArgs)
	if err == nil {
		t.Fatal("outsider was allowed to assign a task")
	}
	checkAssignee(created.ID, &member)
	result, err = call("set_assignee", map[string]any{"task_id": created.ID, "email": ""})
	if err != nil || !result.Ok {
		t.Fatalf("tool clear assignee: result=%+v err=%v", result, err)
	}
	checkAssignee(created.ID, nil)
}
