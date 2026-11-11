package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/llm"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/testdb"
	"github.com/jackc/pgx/v5"
)

func TestAgentMessageIdempotency(t *testing.T) {
	dsn := testdb.Schema(t, "AKHFLOW_TEST_DATABASE_URL")
	ctx := context.Background()
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	owner, err := db.CreateUser(ctx, "idempotency-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	editor, err := db.CreateUser(ctx, "idempotency-editor@example.invalid", "Editor", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "idempotency", "Project", owner)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.SetProjectMemberRole(ctx, project, owner, editor, "editor"); err != nil || !ok {
		t.Fatalf("set editor: %v", err)
	}
	chat, err := db.CreateChat(ctx, project, owner)
	if err != nil {
		t.Fatal(err)
	}
	otherChat, err := db.CreateChat(ctx, project, owner)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	var failAfterEffect atomic.Bool
	var effectCalls atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := calls.Add(1)
		if failAfterEffect.Load() {
			if effectCalls.Add(1) == 1 {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"create_task","arguments":"{\"project\":\"idempotency\",\"title\":\"effect\"}"}}]}}]}`)
				return
			}
			http.Error(w, "fixture failure", http.StatusInternalServerError)
			return
		}
		if count == 1 {
			entered <- struct{}{}
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
	}))
	defer llmServer.Close()
	client := llm.NewClient(llmServer.URL, "test")
	auth := &Auth{Store: db, Secret: []byte("fixture-secret"), TokenTTL: time.Hour}
	handler1 := New(db, auth, client, nil).Handler()
	auth2 := &Auth{Store: second, Secret: []byte("fixture-secret"), TokenTTL: time.Hour}
	handler2 := New(second, auth2, client, nil).Handler()
	token := func(user int64) string {
		value, err := auth.CreateToken(user)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	ownerToken := token(owner)
	editorToken := token(editor)
	const key = "00000000-0000-4000-8000-000000000001"
	request := func(handler http.Handler, chatID int64, userToken, requestKey, message string) *httptest.ResponseRecorder {
		payload, err := json.Marshal(map[string]string{"message": message})
		if err != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf("/api/chats/%d/messages", chatID)
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(payload)))
		req.Header.Set("Authorization", "Bearer "+userToken)
		if requestKey != "" {
			req.Header.Set("Idempotency-Key", requestKey)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- request(handler1, chat, ownerToken, key, "hello") }()
	<-entered
	pending := request(handler2, chat, ownerToken, key, "hello")
	if pending.Code != http.StatusAccepted || !strings.Contains(pending.Body.String(), "pending_or_unknown") {
		t.Fatalf("pending: %d %s", pending.Code, pending.Body.String())
	}
	mismatch := request(handler2, chat, ownerToken, key, "different")
	if mismatch.Code != http.StatusConflict {
		t.Fatalf("mismatch: %d", mismatch.Code)
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate launched agent: %d", calls.Load())
	}
	close(release)
	completed := <-first
	if completed.Code != http.StatusOK {
		t.Fatalf("first: %d %s", completed.Code, completed.Body.String())
	}
	replay := request(handler2, chat, ownerToken, key, "hello")
	var firstResult, replayResult llm.RunResult
	firstError := json.Unmarshal(completed.Body.Bytes(), &firstResult)
	replayError := json.Unmarshal(replay.Body.Bytes(), &replayResult)
	if replay.Code != http.StatusOK || firstError != nil || replayError != nil || !reflect.DeepEqual(firstResult, replayResult) {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	messages, err := db.MessagesOfChat(ctx, chat)
	if err != nil || len(messages) != 2 {
		t.Fatalf("messages: %d %v", len(messages), err)
	}
	if calls.Load() != 1 {
		t.Fatalf("replay launched agent: %d", calls.Load())
	}
	if response := request(handler1, chat, editorToken, key, "hello"); response.Code != http.StatusOK {
		t.Fatalf("other actor: %d", response.Code)
	}
	if response := request(handler1, otherChat, ownerToken, key, "hello"); response.Code != http.StatusOK {
		t.Fatalf("other chat: %d", response.Code)
	}
	for i := 0; i < 2; i++ {
		if response := request(handler1, chat, ownerToken, "", "legacy"); response.Code != http.StatusOK {
			t.Fatalf("legacy: %d", response.Code)
		}
	}
	if calls.Load() != 5 {
		t.Fatalf("separate operations: %d", calls.Load())
	}
	if response := request(handler1, chat, ownerToken, "bad-key", "hello"); response.Code != http.StatusBadRequest {
		t.Fatalf("bad key: %d", response.Code)
	}
	duplicate := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/chats/%d/messages", chat), strings.NewReader(`{"message":"hello"}`))
	duplicate.Header.Set("Authorization", "Bearer "+ownerToken)
	duplicate.Header.Add("Idempotency-Key", key)
	duplicate.Header.Add("Idempotency-Key", key)
	duplicateResponse := httptest.NewRecorder()
	handler1.ServeHTTP(duplicateResponse, duplicate)
	if duplicateResponse.Code != http.StatusBadRequest {
		t.Fatalf("duplicate key headers: %d", duplicateResponse.Code)
	}
	if ok, err := db.SetProjectMemberRole(ctx, project, owner, editor, "viewer"); err != nil || !ok {
		t.Fatalf("set viewer: %v", err)
	}
	if response := request(handler2, chat, editorToken, key, "hello"); response.Code != http.StatusForbidden {
		t.Fatalf("viewer replay: %d", response.Code)
	}
	crashKey := "00000000-0000-4000-8000-000000000002"
	claimed, _, err := db.ClaimAgentMessage(ctx, chat, owner, crashKey, "crash", "crash")
	if err != nil || !claimed {
		t.Fatalf("claim: %v", err)
	}
	if response := request(handler2, chat, ownerToken, crashKey, "crash"); response.Code != http.StatusAccepted {
		t.Fatalf("crash replay: %d", response.Code)
	}
	if calls.Load() != 5 {
		t.Fatal("crashed request was rerun")
	}
	failAfterEffect.Store(true)
	effectKey := "00000000-0000-4000-8000-000000000003"
	failed := request(handler1, chat, ownerToken, effectKey, "create a task")
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("failed request: %d %s", failed.Code, failed.Body.String())
	}
	tasks, err := db.ListTasks(ctx, project, "")
	if err != nil || len(tasks) != 1 || tasks[0].Title != "effect" {
		t.Fatalf("partial effect: %v %v", tasks, err)
	}
	if response := request(handler2, chat, ownerToken, effectKey, "create a task"); response.Code != http.StatusAccepted {
		t.Fatalf("partial effect replay: %d", response.Code)
	}
	if effectCalls.Load() != 2 {
		t.Fatal("request was rerun after a partial effect")
	}

	rollbackKey := "00000000-0000-4000-8000-000000000004"
	claimed, _, err = db.ClaimAgentMessage(ctx, chat, owner, rollbackKey, "rollback", "rollback")
	if err != nil || !claimed {
		t.Fatalf("claim for rollback: %v", err)
	}
	connection, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(ctx)
	_, err = connection.Exec(ctx, `CREATE FUNCTION reject_agent_reply() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.role = 'agent' THEN RAISE EXCEPTION 'fixture failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER reject_agent_reply BEFORE INSERT ON agent_messages FOR EACH ROW EXECUTE FUNCTION reject_agent_reply()`)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.MessagesOfChat(ctx, chat)
	if err != nil {
		t.Fatal(err)
	}
	err = db.FinishAgentMessage(ctx, chat, owner, rollbackKey, "reply", nil, llm.RunResult{Reply: "reply"})
	if err == nil {
		t.Fatal("expected reply insertion to fail")
	}
	after, err := db.MessagesOfChat(ctx, chat)
	if err != nil || len(before) != len(after) {
		t.Fatalf("reply transaction was not rolled back: %v", err)
	}
	if response := request(handler2, chat, ownerToken, rollbackKey, "rollback"); response.Code != http.StatusAccepted {
		t.Fatalf("failed finish was reported as complete: %d", response.Code)
	}
}
