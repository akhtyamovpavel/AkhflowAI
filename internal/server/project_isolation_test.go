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
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/domain"
	"akhflow.ai/akhflow/internal/mcp"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/toolbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTaskProjectIsolation(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_ISOLATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set AKHFLOW_ISOLATION_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("isolation_%d", time.Now().UnixNano())
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
	db, err := store.New(dsnURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateUser(ctx, "owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	member, err := db.CreateUser(ctx, "member@example.invalid", "Member", "hash")
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := db.CreateUser(ctx, "outsider@example.invalid", "Outsider", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "private", "Private", owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateProject(ctx, "other", "Other", outsider); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "INSERT INTO "+schema+".project_members(project_id,user_id,role) VALUES($1,$2,'member')", project, member); err != nil {
		t.Fatal(err)
	}
	taskID, err := db.CreateTask(ctx, domain.Task{ProjectID: project, AuthorID: owner, Title: "Private task", Status: "todo", Priority: "low"})
	if err != nil {
		t.Fatal(err)
	}
	depID, err := db.CreateTask(ctx, domain.Task{ProjectID: project, AuthorID: owner, Title: "Private dependency", Status: "todo", Priority: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddComment(ctx, taskID, owner, "private comment"); err != nil {
		t.Fatal(err)
	}
	auth := &Auth{Store: db, Secret: []byte("isolation-test-signing-key"), TokenTTL: time.Hour}
	tokens := make(map[int64]string)
	for _, id := range []int64{owner, member, outsider} {
		tokens[id], err = auth.CreateToken(id)
		if err != nil {
			t.Fatal(err)
		}
	}
	api := New(db, auth, nil, nil).Handler()
	request := func(user int64, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		out := httptest.NewRecorder()
		api.ServeHTTP(out, req)
		return out
	}
	base := fmt.Sprintf("/api/tasks/%d", taskID)
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{"GET", base, ""},
		{"PATCH", base, `{"title":"changed"}`},
		{"DELETE", base, ""},
		{"GET", base + "/comments", ""},
		{"POST", base + "/comments", `{"body":"intrusion"}`},
		{"GET", base + "/dependencies", ""},
		{"POST", base + "/dependencies", fmt.Sprintf(`{"depends_on":%d}`, depID)},
		{"DELETE", fmt.Sprintf("%s/dependencies/%d", base, depID), ""},
	}
	for _, test := range cases {
		response := request(outsider, test.method, test.path, test.body)
		if response.Code != http.StatusForbidden && response.Code != http.StatusNotFound {
			t.Errorf("outsider %s %s: %d %s", test.method, test.path, response.Code, response.Body.String())
		}
	}
	for _, user := range []int64{owner, member} {
		for _, path := range []string{base, base + "/comments", base + "/dependencies"} {
			response := request(user, "GET", path, "")
			if response.Code != http.StatusOK {
				t.Errorf("member %d GET %s: %d %s", user, path, response.Code, response.Body.String())
			}
		}
	}
	if response := request(member, "PATCH", base, `{"title":"member update"}`); response.Code != http.StatusOK {
		t.Fatalf("member patch: %d %s", response.Code, response.Body.String())
	}
	if response := request(member, "POST", base+"/comments", `{"body":"member comment"}`); response.Code != http.StatusCreated {
		t.Fatalf("member comment: %d %s", response.Code, response.Body.String())
	}
	toolCases := []struct {
		name string
		args string
	}{
		{"get_task", fmt.Sprintf(`{"task_id":%d}`, taskID)},
		{"add_comment", fmt.Sprintf(`{"task_id":%d,"body":"intrusion"}`, taskID)},
		{"set_assignee", fmt.Sprintf(`{"task_id":%d,"email":"outsider@example.invalid"}`, taskID)},
		{"move_task", fmt.Sprintf(`{"task_id":%d,"status":"done"}`, taskID)},
		{"delete_task", fmt.Sprintf(`{"task_id":%d}`, taskID)},
		{"set_task_epic", fmt.Sprintf(`{"task_id":%d}`, taskID)},
		{"set_task_recurring", fmt.Sprintf(`{"task_id":%d,"recurring":"daily"}`, taskID)},
		{"add_dependency", fmt.Sprintf(`{"task_id":%d,"depends_on":%d}`, taskID, depID)},
		{"remove_dependency", fmt.Sprintf(`{"task_id":%d,"depends_on":%d}`, taskID, depID)},
	}
	for _, test := range toolCases {
		tool := toolbox.ByName(test.name)
		if tool == nil {
			t.Fatalf("missing tool %s", test.name)
		}
		for _, user := range []int64{outsider, 0} {
			_, err := tool.Handler(ctx, toolbox.Deps{Store: db, ActorID: user}, json.RawMessage(test.args))
			if err == nil || !strings.Contains(err.Error(), "access denied") {
				t.Errorf("actor %d, tool %s: expected access denial, got %v", user, test.name, err)
			}
		}
	}
	for _, actor := range []struct {
		id      int64
		isError bool
	}{
		{outsider, true},
		{0, true},
		{member, false},
	} {
		server := &mcp.Server{Store: db, UserID: actor.id}
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_task","arguments":{"task_id":%d}}}`, taskID)
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
		response := httptest.NewRecorder()
		server.HTTPHandler().ServeHTTP(response, req)
		var result struct {
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Result.IsError != actor.isError {
			t.Errorf("MCP actor %d: %s", actor.id, response.Body.String())
		}
		if actor.isError && (strings.Contains(response.Body.String(), "Private task") || strings.Contains(response.Body.String(), "private comment")) {
			t.Errorf("MCP actor %d received private data", actor.id)
		}
	}
	result, err := toolbox.ByName("get_task").Handler(ctx, toolbox.Deps{Store: db, ActorID: member}, json.RawMessage(fmt.Sprintf(`{"task_id":%d}`, taskID)))
	if err != nil || !result.Ok {
		t.Fatalf("member get_task: %+v %v", result, err)
	}
	result, err = toolbox.ByName("add_comment").Handler(ctx, toolbox.Deps{Store: db, ActorID: member}, json.RawMessage(fmt.Sprintf(`{"task_id":%d,"body":"tool member comment"}`, taskID)))
	if err != nil || !result.Ok {
		t.Fatalf("member add_comment: %+v %v", result, err)
	}
	result, err = toolbox.ByName("set_assignee").Handler(ctx, toolbox.Deps{Store: db, ActorID: member}, json.RawMessage(fmt.Sprintf(`{"task_id":%d,"email":"member@example.invalid"}`, taskID)))
	if err != nil || !result.Ok {
		t.Fatalf("member set_assignee: %+v %v", result, err)
	}
}
