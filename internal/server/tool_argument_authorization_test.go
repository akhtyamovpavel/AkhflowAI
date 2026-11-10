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
	"akhflow.ai/akhflow/internal/llm"
	"akhflow.ai/akhflow/internal/mcp"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/toolbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestToolArgumentAuthorization(t *testing.T) {
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
	schema := fmt.Sprintf("tool_args_%d", time.Now().UnixNano())
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
	owner, err := db.CreateUser(ctx, "owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	actor, err := db.CreateUser(ctx, "actor@example.invalid", "Actor", "hash")
	if err != nil {
		t.Fatal(err)
	}
	viewerProject, err := db.CreateProject(ctx, "viewer-project", "Viewer", owner)
	if err != nil {
		t.Fatal(err)
	}
	editorProject, err := db.CreateProject(ctx, "editor-project", "Editor", owner)
	if err != nil {
		t.Fatal(err)
	}
	for project, role := range map[int64]string{viewerProject: "viewer", editorProject: "editor"} {
		if ok, err := db.SetProjectMemberRole(ctx, project, owner, actor, role); err != nil || !ok {
			t.Fatalf("set role %s: %v", role, err)
		}
	}
	create := func(project int64, title string) int64 {
		t.Helper()
		id, err := db.CreateTask(ctx, domain.Task{ProjectID: project, AuthorID: owner, Title: title, Status: "todo", Priority: "low"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	viewerTask := create(viewerProject, "Viewer task")
	editorTask := create(editorProject, "Editor task")
	deps := toolbox.Deps{Store: db, ActorID: actor, Actor: "actor@example.invalid"}
	call := func(name, args string) error {
		_, err := toolbox.ByName(name).Handler(ctx, deps, json.RawMessage(args))
		return err
	}

	for _, args := range []string{
		fmt.Sprintf(`{"task_id":%d,"TASK_ID":%d,"status":"done"}`, viewerTask, editorTask),
		fmt.Sprintf(`{"TASK_ID":%d,"task_id":%d,"status":"done"}`, editorTask, viewerTask),
		fmt.Sprintf(`{"task_id":%d,"task_id":%d,"status":"done"}`, viewerTask, editorTask),
		fmt.Sprintf(`{"task_id":%d,"\u0054ASK_ID":%d,"status":"done"}`, viewerTask, editorTask),
		fmt.Sprintf(`{"task_id":%d,"taſk_id":%d,"status":"done"}`, viewerTask, editorTask),
	} {
		if err := call("move_task", args); err == nil {
			t.Errorf("ambiguous task arguments succeeded: %s", args)
		}
	}
	for _, name := range []string{"create_epic", "import_trello", "import_bitrix24_tasks", "import_akhcheck_course"} {
		args := `{"project":"viewer-project","PROJECT":"editor-project"}`
		if err := call(name, args); err == nil {
			t.Errorf("%s accepted ambiguous project arguments", name)
		}
	}
	for name, extra := range map[string]string{
		"add_comment":        `,"body":"forbidden"`,
		"set_assignee":       `,"email":""`,
		"set_task_epic":      `,"epic_id":null`,
		"add_dependency":     fmt.Sprintf(`,"depends_on":%d`, editorTask),
		"remove_dependency":  fmt.Sprintf(`,"depends_on":%d`, editorTask),
		"set_task_recurring": `,"recurring":"daily"`,
		"delete_task":        "",
	} {
		args := fmt.Sprintf(`{"task_id":%d,"TASK_ID":%d%s}`, viewerTask, editorTask, extra)
		if err := call(name, args); err == nil {
			t.Errorf("%s accepted ambiguous task arguments", name)
		}
	}
	for _, args := range []string{
		`{"project":"viewer-project","PROJECT":"editor-project","title":"Forbidden"}`,
		`{"PROJECT":"editor-project","project":"viewer-project","title":"Forbidden"}`,
		`{"project":"viewer-project","project":"editor-project","title":"Forbidden"}`,
	} {
		if err := call("create_task", args); err == nil {
			t.Errorf("ambiguous project arguments succeeded: %s", args)
		}
	}
	if err := call("move_task", fmt.Sprintf(`{"task_id":%d,"status":"done"}`, viewerTask)); err == nil {
		t.Error("viewer can move a task")
	}
	if err := call("move_task", fmt.Sprintf(`{"TASK_ID":%d,"STATUS":"in_progress"}`, editorTask)); err != nil {
		t.Errorf("editor with unambiguous uppercase arguments: %v", err)
	}
	if err := call("create_task", `{"PROJECT":"editor-project","TITLE":"Allowed"}`); err != nil {
		t.Errorf("editor with unambiguous uppercase project: %v", err)
	}
	if err := call("list_tasks", `{"project":"viewer-project"}`); err != nil {
		t.Errorf("viewer read: %v", err)
	}
	if err := call("get_agent_usage", `{"PROJECT":"viewer-project","DAYS":7}`); err != nil {
		t.Errorf("typed handler with unambiguous uppercase project: %v", err)
	}
	if err := call("list_attachments", fmt.Sprintf(`{"TASK_ID":%d}`, viewerTask)); err != nil {
		t.Errorf("viewer attachment read with uppercase task ID: %v", err)
	}
	viewerLarge := create(viewerProject, "Large viewer ID")
	editorLarge := create(editorProject, "Large editor ID")
	const viewerExactID int64 = 9007199254740992
	const editorExactID int64 = 9007199254740993
	if _, err := admin.Exec(ctx, "UPDATE "+schema+".tasks SET id=$1 WHERE id=$2", viewerExactID, viewerLarge); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "UPDATE "+schema+".tasks SET id=$1 WHERE id=$2", editorExactID, editorLarge); err != nil {
		t.Fatal(err)
	}
	if err := call("move_task", fmt.Sprintf(`{"task_id":%d,"status":"done"}`, editorExactID)); err != nil {
		t.Errorf("editor's large task ID: %v", err)
	}
	largeViewer, err := db.TaskByID(ctx, viewerExactID)
	if err != nil || largeViewer == nil || largeViewer.Status != "todo" {
		t.Errorf("large viewer task modified: %+v, %v", largeViewer, err)
	}
	largeEditor, err := db.TaskByID(ctx, editorExactID)
	if err != nil || largeEditor == nil || largeEditor.Status != "done" {
		t.Errorf("large editor task not updated: %+v, %v", largeEditor, err)
	}

	malicious := fmt.Sprintf(`{"task_id":%d,"TASK_ID":%d,"status":"done"}`, viewerTask, editorTask)
	rpc := &mcp.Server{Store: db, UserID: actor, Actor: deps.Actor, APIKey: "fixture-key"}
	rpcBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"move_task","arguments":%s}}`, malicious)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(rpcBody))
	req.Header.Set("Authorization", "Bearer fixture-key")
	response := httptest.NewRecorder()
	rpc.HTTPHandler().ServeHTTP(response, req)
	var rpcResult struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &rpcResult); err != nil || !rpcResult.Result.IsError {
		t.Errorf("MCP accepted ambiguous arguments: %s, %v", response.Body.String(), err)
	}

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if request.Messages[len(request.Messages)-1].Role == "tool" {
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"OK"}}]}`)
			return
		}
		encoded, _ := json.Marshal(malicious)
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"1","type":"function","function":{"name":"move_task","arguments":%s}}]}}]}`, encoded)
	}))
	defer model.Close()
	agent := llm.Agent{Client: llm.NewClient(model.URL, "fixture"), Deps: deps}
	result, err := agent.Run(ctx, "test", "", nil)
	if err != nil || len(result.ToolCalls) != 1 {
		t.Fatalf("agent run: %+v, %v", result, err)
	}
	var toolResult toolbox.Result
	if err := json.Unmarshal([]byte(result.ToolCalls[0].Result), &toolResult); err != nil || toolResult.Ok {
		t.Errorf("agent accepted ambiguous arguments: %+v, %v", toolResult, err)
	}
	stored, err := db.TaskByID(ctx, viewerTask)
	if err != nil || stored == nil || stored.Status != "todo" {
		t.Errorf("viewer task modified: %+v, %v", stored, err)
	}
	tasks, err := db.ListTasks(ctx, viewerProject, "")
	if err != nil || len(tasks) != 2 {
		t.Errorf("viewer project changed: %d tasks, %v", len(tasks), err)
	}
}
