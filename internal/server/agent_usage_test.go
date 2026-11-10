package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/toolbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAgentUsageIsolationAndPeriods(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_AGENT_USAGE_TEST_DSN")
	if dsn == "" {
		t.Skip("set AKHFLOW_AGENT_USAGE_TEST_DSN to an isolated PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("agent_usage_%d", time.Now().UnixNano())
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	params := parsed.Query()
	params.Set("search_path", schema)
	parsed.RawQuery = params.Encode()
	db, err := store.New(parsed.String())
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
	outsider, err := db.CreateUser(ctx, "outsider@example.invalid", "Other", "hash")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := db.CreateUser(ctx, "viewer@example.invalid", "Viewer", "hash")
	if err != nil {
		t.Fatal(err)
	}
	editor, err := db.CreateUser(ctx, "editor@example.invalid", "Editor", "hash")
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := db.CreateProject(ctx, "usage-one", "Project", owner)
	if err != nil {
		t.Fatal(err)
	}
	for id, role := range map[int64]string{viewer: "viewer", editor: "editor"} {
		ok, err := db.SetProjectMemberRole(ctx, projectID, owner, id, role)
		if err != nil || !ok {
			t.Fatalf("set role %s: %v", role, err)
		}
	}
	otherID, err := db.CreateProject(ctx, "usage-two", "Other", outsider)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateProject(ctx, "usage-empty", "Empty", owner)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(ctx, projectID, owner)
	if err != nil {
		t.Fatal(err)
	}
	otherChat, err := db.CreateChat(ctx, otherID, outsider)
	if err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO ` + schema + `.agent_messages(chat_id, role, content, tool_calls, created_at)
		VALUES ($1, $2, $3, $4::jsonb, now() - $5::integer * interval '1 day')`
	fixtures := []struct {
		chatID int64
		role   string
		calls  string
		age    int
	}{
		{chat, "user", "null", 1},
		{chat, "agent", `[{"tool":"one"},{"tool":"two"}]`, 1},
		{chat, "user", "null", 15},
		{chat, "agent", `{"unexpected":true}`, 15},
		{chat, "user", "null", 40},
		{otherChat, "user", "null", 1},
		{otherChat, "agent", `[{"tool":"other"}]`, 1},
	}
	for _, item := range fixtures {
		_, err := admin.Exec(ctx, insert, item.chatID, item.role, "private content", item.calls, item.age)
		if err != nil {
			t.Fatal(err)
		}
	}
	auth := &Auth{Store: db, Secret: []byte("test-only"), TokenTTL: time.Hour}
	ownerToken, err := auth.CreateToken(owner)
	if err != nil {
		t.Fatal(err)
	}
	otherToken, err := auth.CreateToken(outsider)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(db, auth, nil, nil).Handler()
	request := func(token, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	path := "/api/projects/usage-one/agent/usage"
	if got := request("", path).Code; got != http.StatusUnauthorized {
		t.Fatalf("anonymous request: %d", got)
	}
	if got := request(otherToken, path).Code; got != http.StatusForbidden {
		t.Fatalf("outsider request: %d", got)
	}
	if got := request(ownerToken, path+"?days=8").Code; got != http.StatusBadRequest {
		t.Fatalf("invalid period: %d", got)
	}
	emptyResponse := request(ownerToken, "/api/projects/usage-empty/agent/usage")
	var empty store.AgentUsage
	if err := json.Unmarshal(emptyResponse.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if emptyResponse.Code != http.StatusOK || empty != (store.AgentUsage{Days: 30}) {
		t.Fatalf("empty project: status %d, result %+v", emptyResponse.Code, empty)
	}
	for _, test := range []struct {
		query string
		want  store.AgentUsage
	}{
		{"?days=7", store.AgentUsage{Days: 7, Requests: 1, Replies: 1, ToolCalls: 2}},
		{"", store.AgentUsage{Days: 30, Requests: 2, Replies: 2, ToolCalls: 2}},
		{"?days=0", store.AgentUsage{Days: 0, Requests: 3, Replies: 2, ToolCalls: 2}},
	} {
		response := request(ownerToken, path+test.query)
		var got store.AgentUsage
		if response.Code != http.StatusOK {
			t.Fatalf("request %s: %d %s", test.query, response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Fatalf("request %s: got %+v, want %+v", test.query, got, test.want)
		}
	}
	tool := toolbox.ByName("get_agent_usage")
	if tool == nil {
		t.Fatal("agent usage tool missing from registry")
	}
	args := json.RawMessage(`{"project":"usage-one","days":7}`)
	for name, id := range map[string]int64{"viewer": viewer, "editor": editor} {
		token, err := auth.CreateToken(id)
		if err != nil {
			t.Fatal(err)
		}
		response := request(token, path+"?days=7")
		var usage store.AgentUsage
		if err := json.Unmarshal(response.Body.Bytes(), &usage); err != nil {
			t.Fatal(err)
		}
		want := store.AgentUsage{Days: 7, Requests: 1, Replies: 1, ToolCalls: 2}
		if response.Code != http.StatusOK || usage != want {
			t.Errorf("%s REST usage: status %d, result %+v", name, response.Code, usage)
		}
		if got := request(token, "/api/projects/usage-two/agent/usage").Code; got != http.StatusForbidden {
			t.Errorf("%s REST foreign project: %d", name, got)
		}
		result, err := tool.Handler(ctx, toolbox.Deps{Store: db, ActorID: id}, args)
		if err != nil || !result.Ok || result.Data != want {
			t.Errorf("%s tool usage: %+v, error: %v", name, result, err)
		}
		_, err = tool.Handler(ctx, toolbox.Deps{Store: db, ActorID: id}, json.RawMessage(`{"project":"usage-two"}`))
		if err == nil {
			t.Errorf("%s could access foreign project through tool", name)
		}
	}
	result, err := tool.Handler(ctx, toolbox.Deps{Store: db, ActorID: owner}, args)
	if err != nil || !result.Ok || result.Data != (store.AgentUsage{Days: 7, Requests: 1, Replies: 1, ToolCalls: 2}) {
		t.Fatalf("tool result: %+v, error: %v", result, err)
	}
	_, err = tool.Handler(ctx, toolbox.Deps{Store: db, ActorID: outsider}, args)
	if err == nil {
		t.Fatal("outsider could access metrics through tool")
	}
	badArgs := json.RawMessage(`{"project":"usage-one","days":-1}`)
	_, err = tool.Handler(ctx, toolbox.Deps{Store: db, ActorID: owner}, badArgs)
	if err == nil {
		t.Fatal("tool accepted an invalid period")
	}
}
