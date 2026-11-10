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
	"strings"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/llm"
	"akhflow.ai/akhflow/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The model is deliberately stubbed: this tests tool execution and project
// isolation, not a real model's ability to interpret Russian.
func TestNaturalLanguageTaskCreation(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_NATURAL_LANGUAGE_TEST_DSN")
	if dsn == "" {
		t.Skip("set AKHFLOW_NATURAL_LANGUAGE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("natural_language_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	}()
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
	other, err := db.CreateUser(ctx, "other@example.invalid", "Other", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "demo", "Demo", owner)
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := db.CreateUser(ctx, "viewer@example.invalid", "Viewer", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if updated, err := db.SetProjectMemberRole(ctx, project, owner, viewer, "viewer"); err != nil || !updated {
		t.Fatalf("add viewer: updated=%v err=%v", updated, err)
	}
	foreign, err := db.CreateProject(ctx, "foreign", "Foreign", other)
	if err != nil {
		t.Fatal(err)
	}

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected model request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) < 2 {
			t.Errorf("invalid model request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if !strings.Contains(request.Messages[0].Content, `project "demo"`) {
			t.Errorf("model was not given current project: %q", request.Messages[0].Content)
		}
		last := request.Messages[len(request.Messages)-1]
		if last.Role == "user" {
			found := false
			for _, tool := range request.Tools {
				if tool.Function.Name == "create_task" {
					found = true
				}
			}
			if !found {
				t.Error("create_task is not available to the model")
			}
			target := "demo"
			if strings.Contains(last.Content, "чужом") {
				target = "foreign"
			}
			args, _ := json.Marshal(map[string]string{"project": target, "title": "Подготовить демо"})
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "tool_calls": []any{map[string]any{
					"id": "call", "type": "function", "function": map[string]any{"name": "create_task", "arguments": string(args)},
				}},
			}}}})
			return
		}
		if last.Role != "tool" || last.ToolCallID != "call" {
			t.Errorf("expected tool response, got %+v", last)
		}
		var result struct {
			OK bool `json:"ok"`
		}
		if err := json.Unmarshal([]byte(last.Content), &result); err != nil {
			t.Errorf("decode tool result: %v", err)
		}
		reply := "Задача создана"
		if !result.OK {
			reply = "Нет доступа к проекту"
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": reply}}}})
	}))
	defer model.Close()

	auth := &Auth{Store: db, Secret: []byte("test-only-signing-key"), TokenTTL: time.Hour}
	token, err := auth.CreateToken(owner)
	if err != nil {
		t.Fatal(err)
	}
	api := New(db, auth, llm.NewClient(model.URL, "fake"), nil).Handler()
	request := func(path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		api.ServeHTTP(response, req)
		return response
	}
	response := request("/api/projects/demo/chats", nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("create chat: %d %s", response.Code, response.Body.String())
	}
	var chat struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &chat); err != nil || chat.ID == 0 {
		t.Fatalf("invalid chat response: %s (%v)", response.Body.String(), err)
	}
	path := fmt.Sprintf("/api/chats/%d/messages", chat.ID)
	for _, tc := range []struct {
		message, reply string
		ok             bool
	}{
		{"Создай задачу Подготовить демо", "Задача создана", true},
		{"Создай задачу в чужом проекте", "Нет доступа к проекту", false},
	} {
		response := request(path, map[string]string{"message": tc.message})
		if response.Code != http.StatusOK {
			t.Fatalf("send message: %d %s", response.Code, response.Body.String())
		}
		var result llm.RunResult
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Reply != tc.reply || len(result.ToolCalls) != 1 || result.ToolCalls[0].Tool != "create_task" {
			t.Fatalf("unexpected agent response: %+v", result)
		}
		var toolResult struct {
			OK      bool   `json:"ok"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(result.ToolCalls[0].Result), &toolResult); err != nil {
			t.Fatal(err)
		}
		if toolResult.OK != tc.ok || (!tc.ok && !strings.Contains(toolResult.Message, "access denied")) {
			t.Fatalf("unexpected tool result: %+v", toolResult)
		}
	}
	viewerToken, err := auth.CreateToken(viewer)
	if err != nil {
		t.Fatal(err)
	}
	viewerRequest := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"message":"Создай задачу"}`))
	viewerRequest.Header.Set("Authorization", "Bearer "+viewerToken)
	viewerResponse := httptest.NewRecorder()
	api.ServeHTTP(viewerResponse, viewerRequest)
	if viewerResponse.Code != http.StatusForbidden {
		t.Fatalf("viewer creating a task via chat: got %d", viewerResponse.Code)
	}
	local, err := db.ListTasks(ctx, project, "")
	if err != nil {
		t.Fatal(err)
	}
	remote, err := db.ListTasks(ctx, foreign, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 1 || local[0].Title != "Подготовить демо" || local[0].AuthorID != owner || len(remote) != 0 {
		t.Fatalf("unexpected tasks: local=%+v foreign=%+v", local, remote)
	}
}
