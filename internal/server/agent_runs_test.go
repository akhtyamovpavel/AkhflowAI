package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/llm"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/testdb"
	"akhflow.ai/akhflow/internal/toolbox"
	"github.com/jackc/pgx/v5"
)

func TestAgentRuns(t *testing.T) {
	dsn := testdb.Schema(t, "AKHFLOW_TEST_DATABASE_URL")
	ctx := context.Background()
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateUser(ctx, "runs-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "runs", "Runs", owner)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(ctx, project, owner)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	}))
	defer model.Close()
	auth := &Auth{Store: db, Secret: []byte("fixture-secret"), TokenTTL: time.Hour}
	token, err := auth.CreateToken(owner)
	if err != nil {
		t.Fatal(err)
	}
	server := New(db, auth, llm.NewClient(model.URL, "test"), nil)
	handler := server.Handler()
	request := func(method, path, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	path := fmt.Sprintf("/api/chats/%d/runs", chat)
	key := "00000000-0000-4000-8000-000000000001"
	first := request("POST", path, key, `{"message":"hello"}`)
	if first.Code != 202 {
		t.Fatalf("create: %d %s", first.Code, first.Body)
	}
	var run store.AgentRun
	if err = json.Unmarshal(first.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	duplicate := request("POST", path, key, `{"message":"hello"}`)
	var again store.AgentRun
	if err = json.Unmarshal(duplicate.Body.Bytes(), &again); err != nil || again.ID != run.ID {
		t.Fatalf("duplicate: %d %s", duplicate.Code, duplicate.Body)
	}
	if got := request("POST", path, key, `{"message":"different"}`); got.Code != 409 {
		t.Fatalf("conflict: %d", got.Code)
	}
	workerCtx, stop := context.WithCancel(ctx)
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); server.RunAgentWorker(workerCtx) }()
	}
	defer func() { stop(); workers.Wait() }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	current := request("GET", fmt.Sprintf("/api/runs/%d", run.ID), "", "")
	if err = json.Unmarshal(current.Body.Bytes(), &run); err != nil || run.Status != "running" {
		t.Fatalf("running: %s", current.Body)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current = request("GET", fmt.Sprintf("/api/runs/%d", run.ID), "", "")
		if err = json.Unmarshal(current.Body.Bytes(), &run); err == nil && run.Status == "succeeded" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run did not finish: %s", current.Body)
}

func TestRunLeaseAndCancellation(t *testing.T) {
	dsn := testdb.Schema(t, "AKHFLOW_TEST_DATABASE_URL")
	ctx := context.Background()
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateUser(ctx, "lease-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "lease", "Lease", owner)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(ctx, project, owner)
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.EnqueueRun(ctx, chat, owner, "00000000-0000-4000-8000-000000000001", "hello", "hello")
	if err != nil {
		t.Fatal(err)
	}
	token := "00000000-0000-4000-8000-000000000002"
	claimed, err := db.ClaimRun(ctx, token)
	if err != nil || claimed == nil || claimed.ID != run.ID {
		t.Fatalf("claim: %v", err)
	}
	other, err := db.ClaimRun(ctx, "00000000-0000-4000-8000-000000000003")
	if err != nil || other != nil {
		t.Fatalf("second claim: %v", err)
	}
	if err = db.StartRunStep(ctx, run.ID, token, 1, 1, "create_task"); err != nil {
		t.Fatal(err)
	}
	if err = db.FinishRunStep(ctx, run.ID, "00000000-0000-4000-8000-000000000003", 1, "succeeded", ""); err == nil {
		t.Fatal("stale token accepted")
	}
	if err = db.CancelRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.StartRunStep(ctx, run.ID, token, 2, 2, "create_task"); err == nil {
		t.Fatal("step after cancel accepted")
	}
	result := map[string]any{"reply": "done", "iterations": 1}
	if err = db.FinishRun(ctx, run.ID, token, "succeeded", "", result, "done", nil); err != nil {
		t.Fatal(err)
	}
	finished, err := db.AgentRun(ctx, run.ID)
	if err != nil || finished.Status != "canceled" || len(finished.Steps) != 1 || finished.Steps[0].Status != "interrupted" {
		t.Fatalf("canceled partial run: %+v %v", finished, err)
	}
	if err = db.FinishRun(ctx, run.ID, token, "succeeded", "", result, "done", nil); err == nil {
		t.Fatal("terminal run overwritten")
	}
	newRun, err := db.EnqueueRun(ctx, chat, owner, "00000000-0000-4000-8000-000000000004", "next", "next")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ClaimRun(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err = db.StartRunStep(ctx, newRun.ID, token, 1, 1, "create_task"); err != nil {
		t.Fatal(err)
	}
	connection, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(ctx)
	if _, err = connection.Exec(ctx, `UPDATE agent_runs SET lease_until=now()-interval '1 second' WHERE id=$1`, newRun.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err = db.ClaimRun(ctx, "00000000-0000-4000-8000-000000000005")
	if err != nil || claimed != nil {
		t.Fatalf("expired run was reclaimed: %v", err)
	}
	interrupted, err := db.AgentRun(ctx, newRun.ID)
	if err != nil || interrupted.Status != "interrupted" || len(interrupted.Steps) != 1 {
		t.Fatalf("interrupted run: %+v %v", interrupted, err)
	}
	if err = db.FinishRunStep(ctx, newRun.ID, token, 1, "succeeded", ""); err == nil {
		t.Fatal("expired lease accepted")
	}
}

func TestRunProjectScopeAndPermissions(t *testing.T) {
	dsn := testdb.Schema(t, "AKHFLOW_TEST_DATABASE_URL")
	ctx := context.Background()
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateUser(ctx, "scope-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := db.CreateUser(ctx, "scope-viewer@example.invalid", "Viewer", "hash")
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := db.CreateUser(ctx, "scope-outsider@example.invalid", "Outsider", "hash")
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.CreateProject(ctx, "scope-one", "One", owner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateProject(ctx, "scope-two", "Two", owner)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.SetProjectMemberRole(ctx, first, owner, viewer, "viewer"); err != nil || !ok {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(ctx, first, owner)
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.EnqueueRun(ctx, chat, owner, "00000000-0000-4000-8000-000000000001", "hello", "hello")
	if err != nil {
		t.Fatal(err)
	}
	auth := &Auth{Store: db, Secret: []byte("fixture-secret"), TokenTTL: time.Hour}
	handler := New(db, auth, nil, nil).Handler()
	request := func(user int64, method, path string) int {
		token, err := auth.CreateToken(user)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(`{"message":"hello"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", "00000000-0000-4000-8000-000000000002")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	chatPath := fmt.Sprintf("/api/chats/%d/runs", chat)
	runPath := fmt.Sprintf("/api/runs/%d", run.ID)
	if got := request(viewer, "GET", chatPath); got != 200 {
		t.Fatalf("viewer list: %d", got)
	}
	if got := request(viewer, "POST", chatPath); got != 403 {
		t.Fatalf("viewer create: %d", got)
	}
	if got := request(viewer, "POST", runPath+"/cancel"); got != 403 {
		t.Fatalf("viewer cancel: %d", got)
	}
	if got := request(outsider, "GET", runPath); got != 403 {
		t.Fatalf("outsider get: %d", got)
	}
	tool := toolbox.ByName("create_task")
	deps := toolbox.Deps{Store: db, ActorID: owner, ProjectID: first}
	_, err = tool.Handler(ctx, deps, json.RawMessage(`{"project":"scope-two","title":"not allowed"}`))
	if err == nil {
		t.Fatal("cross-project tool call accepted")
	}
}

func TestConcurrentRunIdempotency(t *testing.T) {
	dsn := testdb.Schema(t, "AKHFLOW_TEST_DATABASE_URL")
	ctx := context.Background()
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateUser(ctx, "concurrent-runs@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "concurrent-runs", "Runs", owner)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(ctx, project, owner)
	if err != nil {
		t.Fatal(err)
	}
	ids := make(chan int64, 10)
	failures := make(chan error, 10)
	var group sync.WaitGroup
	for i := 0; i < 10; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			run, err := db.EnqueueRun(ctx, chat, owner, "00000000-0000-4000-8000-000000000001", "hello", "hello")
			if err != nil {
				failures <- err
				return
			}
			ids <- run.ID
		}()
	}
	group.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var id int64
	for value := range ids {
		if id == 0 {
			id = value
		}
		if value != id {
			t.Fatal("duplicate run created")
		}
	}
	messages, err := db.MessagesOfChat(ctx, chat)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages=%d error=%v", len(messages), err)
	}
}

func TestRunQueueBoundAndAtomicFinish(t *testing.T) {
	dsn := testdb.Schema(t, "AKHFLOW_TEST_DATABASE_URL")
	ctx := context.Background()
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateUser(ctx, "queue-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "queue", "Queue", owner)
	if err != nil {
		t.Fatal(err)
	}
	key := "00000000-0000-4000-8000-000000000001"
	var lastChat int64
	for i := 0; i < 101; i++ {
		chat, err := db.CreateChat(ctx, project, owner)
		if err != nil {
			t.Fatal(err)
		}
		lastChat = chat
		_, err = db.EnqueueRun(ctx, chat, owner, key, "hello", "hello")
		if i < 100 && err != nil {
			t.Fatal(err)
		}
		if i == 100 && !errors.Is(err, store.ErrRunQueueFull) {
			t.Fatalf("queue bound: %v", err)
		}
	}
	messages, err := db.MessagesOfChat(ctx, lastChat)
	if err != nil || len(messages) != 0 {
		t.Fatalf("orphan message: %v %v", messages, err)
	}
	token := "00000000-0000-4000-8000-000000000002"
	run, err := db.ClaimRun(ctx, token)
	if err != nil || run == nil {
		t.Fatalf("claim: %v", err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `CREATE FUNCTION reject_run_reply() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.role='agent' THEN RAISE EXCEPTION 'rejected'; END IF; RETURN NEW; END $$`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `CREATE TRIGGER reject_run_reply BEFORE INSERT ON agent_messages FOR EACH ROW EXECUTE FUNCTION reject_run_reply()`)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]any{"reply": "done", "iterations": 1}
	if err = db.FinishRun(ctx, run.ID, token, "succeeded", "", result, "done", nil); err == nil {
		t.Fatal("expected reply failure")
	}
	current, err := db.AgentRun(ctx, run.ID)
	if err != nil || current.Status != "running" || current.Result != nil {
		t.Fatalf("finish was not atomic: %+v %v", current, err)
	}
}
