package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/domain"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/toolbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProjectRolesEnforceAccess(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_ROLES_TEST_DSN")
	if dsn == "" {
		t.Skip("AKHFLOW_ROLES_TEST_DSN is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("roles_%d", time.Now().UnixNano())
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
	createUser := func(email string) int64 {
		t.Helper()
		id, err := db.CreateUser(ctx, email, email, "hash")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	owner := createUser("owner@example.invalid")
	viewer := createUser("viewer@example.invalid")
	editor := createUser("editor@example.invalid")
	legacy := createUser("legacy@example.invalid")
	outsider := createUser("outsider@example.invalid")
	projectID, err := db.CreateProject(ctx, "roles-test", "Roles", owner)
	if err != nil {
		t.Fatal(err)
	}
	for id, role := range map[int64]string{viewer: "viewer", editor: "editor"} {
		ok, err := db.SetProjectMemberRole(ctx, projectID, owner, id, role)
		if err != nil || !ok {
			t.Fatalf("set role %s: %v", role, err)
		}
	}
	_, err = admin.Exec(ctx, "INSERT INTO "+schema+".project_members(project_id,user_id) VALUES($1,$2)", projectID, legacy)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := db.CreateTask(ctx, domain.Task{ProjectID: projectID, AuthorID: owner, Title: "test", Status: "todo", Priority: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	epicID, err := db.CreateEpic(ctx, projectID, "epic", "", "todo")
	if err != nil {
		t.Fatal(err)
	}
	chatID, err := db.CreateChat(ctx, projectID, owner)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := db.CreateProject(ctx, "other-test", "Other", outsider)
	if err != nil {
		t.Fatal(err)
	}
	foreignTask, err := db.CreateTask(ctx, domain.Task{ProjectID: otherID, AuthorID: outsider, Title: "foreign", Status: "todo", Priority: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	auth := &Auth{Store: db, Secret: []byte("test-secret"), TokenTTL: time.Hour}
	handler := New(db, auth, nil, nil).Handler()
	request := func(userID int64, method, path, body string) int {
		t.Helper()
		token, err := auth.CreateToken(userID)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response.Code
	}
	for _, actor := range []struct {
		id       int64
		name     string
		writable bool
	}{{viewer, "viewer", false}, {editor, "editor", true}, {legacy, "member", true}} {
		path := fmt.Sprintf("/api/tasks/%d", taskID)
		wantOutsider, wantMember := 403, 403
		if actor.writable {
			wantOutsider, wantMember = 400, 200
		}
		if status := request(actor.id, "PATCH", path, fmt.Sprintf(`{"assignee_id":%d}`, outsider)); status != wantOutsider {
			t.Errorf("%s assigning outsider: got %d want %d", actor.name, status, wantOutsider)
		}
		if status := request(actor.id, "PATCH", path, fmt.Sprintf(`{"assignee_id":%d}`, viewer)); status != wantMember {
			t.Errorf("%s assigning member: got %d want %d", actor.name, status, wantMember)
		}
		for _, target := range []string{"outsider@example.invalid", "viewer@example.invalid"} {
			args, _ := json.Marshal(map[string]any{"task_id": taskID, "email": target})
			_, err := toolbox.ByName("set_assignee").Handler(ctx, toolbox.Deps{Store: db, ActorID: actor.id}, args)
			if (err == nil) != (actor.writable && target == "viewer@example.invalid") {
				t.Errorf("%s tool assigning %s: %v", actor.name, target, err)
			}
		}
	}
	checks := []struct {
		actor              int64
		method, path, body string
		status             int
	}{
		{viewer, "GET", "/api/projects/roles-test/tasks", "", 200},
		{viewer, "GET", fmt.Sprintf("/api/tasks/%d", taskID), "", 200},
		{viewer, "POST", "/api/projects/roles-test/tasks", `{"title":"blocked"}`, 403},
		{viewer, "POST", "/api/projects/roles-test/imports/trello", `{}`, 403},
		{viewer, "POST", "/api/projects/roles-test/imports/bitrix24", `{}`, 403},
		{viewer, "POST", "/api/projects/roles-test/imports/akhcheck", `{}`, 403},
		{viewer, "PATCH", fmt.Sprintf("/api/tasks/%d", taskID), `{"title":"blocked"}`, 403},
		{viewer, "DELETE", fmt.Sprintf("/api/tasks/%d", taskID), "", 403},
		{viewer, "POST", fmt.Sprintf("/api/tasks/%d/comments", taskID), `{"body":"blocked"}`, 403},
		{viewer, "POST", fmt.Sprintf("/api/tasks/%d/dependencies", taskID), `{}`, 403},
		{viewer, "DELETE", fmt.Sprintf("/api/tasks/%d/dependencies/1", taskID), "", 403},
		{viewer, "POST", "/api/projects/roles-test/epics", `{"title":"blocked"}`, 403},
		{viewer, "DELETE", fmt.Sprintf("/api/epics/%d", epicID), "", 403},
		{viewer, "POST", "/api/projects/roles-test/chats", "", 403},
		{viewer, "POST", fmt.Sprintf("/api/chats/%d/messages", chatID), `{}`, 403},
		{viewer, "PUT", "/api/projects/roles-test/members", `{"email":"editor@example.invalid","role":"viewer"}`, 403},
		{outsider, "GET", fmt.Sprintf("/api/tasks/%d", taskID), "", 403},
		{outsider, "PATCH", fmt.Sprintf("/api/tasks/%d", taskID), `{"title":"blocked"}`, 403},
		{editor, "POST", "/api/projects/roles-test/tasks", `{"title":"allowed"}`, 201},
		{legacy, "POST", "/api/projects/roles-test/tasks", `{"title":"legacy"}`, 201},
		{owner, "PUT", "/api/projects/roles-test/members", `{"email":"editor@example.invalid","role":"viewer"}`, 200},
		{editor, "POST", "/api/projects/roles-test/tasks", `{"title":"revoked"}`, 403},
		{owner, "PUT", "/api/projects/roles-test/members", `{"email":"owner@example.invalid","role":"viewer"}`, 403},
	}
	for _, check := range checks {
		if status := request(check.actor, check.method, check.path, check.body); status != check.status {
			t.Errorf("%s %s actor %d: got %d want %d", check.method, check.path, check.actor, status, check.status)
		}
	}
	for _, tool := range toolbox.Registry {
		readOnly := tool.Name == "list_tasks" || tool.Name == "list_epics" || tool.Name == "get_task" || tool.Name == "list_attachments"
		args, _ := json.Marshal(map[string]any{"project": "roles-test", "task_id": taskID})
		_, err := tool.Handler(ctx, toolbox.Deps{Store: db, ActorID: viewer}, args)
		if readOnly && err != nil {
			t.Errorf("viewer read tool %s: %v", tool.Name, err)
		}
		if !readOnly && (err == nil || err.Error() != "access denied") {
			t.Errorf("viewer write tool %s: expected access denied, got %v", tool.Name, err)
		}
	}
	args, _ := json.Marshal(map[string]any{"project": "other-test", "task_id": taskID, "email": "outsider@example.invalid"})
	_, err = toolbox.ByName("set_assignee").Handler(ctx, toolbox.Deps{Store: db, ActorID: outsider}, args)
	if err == nil {
		t.Error("foreign project argument bypassed task authorization")
	}
	args, _ = json.Marshal(map[string]any{"project": "roles-test", "task_id": foreignTask, "title": "wrong project"})
	_, err = toolbox.ByName("create_task").Handler(ctx, toolbox.Deps{Store: db, ActorID: outsider}, args)
	if err == nil {
		t.Error("foreign task argument bypassed project authorization")
	}
}
