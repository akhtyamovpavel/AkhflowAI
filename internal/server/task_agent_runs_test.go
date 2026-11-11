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

	"akhflow.ai/akhflow/internal/domain"
	"akhflow.ai/akhflow/internal/llm"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/testdb"
)

func TestTaskRunsSnapshotAndIdempotency(t *testing.T) {
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
	owner, err := db.CreateUser(ctx, "task-run-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := db.CreateUser(ctx, "task-run-viewer@example.invalid", "Viewer", "hash")
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := db.CreateUser(ctx, "task-run-outsider@example.invalid", "Outsider", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "task-runs", "Runs", owner)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.SetProjectMemberRole(ctx, project, owner, viewer, "viewer"); err != nil || !ok {
		t.Fatal(err)
	}
	taskID, err := db.CreateTask(ctx, domain.Task{ProjectID: project, AuthorID: owner, Title: "Original title", Description: "Original description", Status: "todo", Priority: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	key := "00000000-0000-4000-8000-000000000001"
	ids := make(chan int64, 10)
	failures := make(chan error, 10)
	var group sync.WaitGroup
	for i := 0; i < 10; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			run, err := db.EnqueueTaskRun(ctx, taskID, owner, key, "inspect")
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
	var runID int64
	for id := range ids {
		if runID == 0 {
			runID = id
		}
		if id != runID {
			t.Fatal("duplicate run")
		}
	}
	if err = db.UpdateTask(ctx, taskID, []string{"title=$1", "description=$2"}, []any{"Changed title", "Changed description"}); err != nil {
		t.Fatal(err)
	}
	replay, err := db.EnqueueTaskRun(ctx, taskID, owner, key, "inspect")
	if err != nil || replay.ID != runID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if _, err = db.EnqueueTaskRun(ctx, taskID, owner, key, "different"); !errors.Is(err, store.ErrAgentRequestConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if _, err = db.EnqueueTaskRun(ctx, taskID, viewer, key, "inspect"); !errors.Is(err, store.ErrTaskRunAccess) {
		t.Fatalf("viewer: %v", err)
	}
	chats, err := db.ChatsOfProject(ctx, project)
	if err != nil || len(chats) != 1 {
		t.Fatalf("chats: %v %v", chats, err)
	}
	messages, err := db.MessagesOfChat(ctx, replay.ChatID)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages: %v %v", messages, err)
	}
	snapshot, err := db.TaskRunSnapshot(ctx, runID)
	if err != nil || snapshot.Title != "Original title" || snapshot.Description != "Original description" {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
	auth := &Auth{Store: db, Secret: []byte("fixture-secret"), TokenTTL: time.Hour}
	captured := make(chan string, 1)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		captured <- body.Messages[len(body.Messages)-1].Content
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	}))
	defer model.Close()
	server := New(db, auth, llm.NewClient(model.URL, "test"), nil)
	handler := server.Handler()
	request := func(user int64, method, path string) int {
		token, err := auth.CreateToken(user)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(`{"message":"inspect"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	path := fmt.Sprintf("/api/tasks/%d/runs", taskID)
	if got := request(viewer, "GET", path); got != 200 {
		t.Fatalf("viewer list: %d", got)
	}
	if got := request(viewer, "POST", path); got != 403 {
		t.Fatalf("viewer create: %d", got)
	}
	if got := request(outsider, "GET", path); got != 403 {
		t.Fatalf("outsider list: %d", got)
	}
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); server.RunAgentWorker(workerCtx) }()
	defer func() { stop(); <-done }()
	select {
	case prompt := <-captured:
		if !strings.Contains(prompt, "Original title") || !strings.Contains(prompt, "Original description") || strings.Contains(prompt, "Changed title") {
			t.Fatalf("wrong prompt: %s", prompt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not run")
	}
	deadline := time.Now().Add(5 * time.Second)
	finished := false
	for time.Now().Before(deadline) {
		current, err := db.AgentRun(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == "succeeded" {
			finished = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !finished {
		t.Fatal("run did not succeed")
	}
	task, err := db.TaskByID(ctx, taskID)
	if err != nil || task.Status != "todo" {
		t.Fatalf("task status changed: %+v %v", task, err)
	}
	if err = db.DeleteTask(ctx, taskID); err != nil {
		t.Fatal(err)
	}
	stored, err := db.AgentRun(ctx, runID)
	if err != nil || stored == nil || stored.TaskID != nil {
		t.Fatalf("run lost after deletion: %+v %v", stored, err)
	}
	snapshot, err = db.TaskRunSnapshot(ctx, runID)
	if err != nil || snapshot.Title != "Original title" {
		t.Fatalf("snapshot lost: %+v %v", snapshot, err)
	}
	if got := request(owner, "GET", path); got != 404 {
		t.Fatalf("deleted task list: %d", got)
	}
}

func TestTaskRunQueueFullDoesNotCreateChat(t *testing.T) {
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
	owner, err := db.CreateUser(ctx, "task-queue-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "task-queue", "Queue", owner)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := db.CreateTask(ctx, domain.Task{ProjectID: project, AuthorID: owner, Title: "Task", Status: "todo", Priority: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	key := "00000000-0000-4000-8000-000000000001"
	original, err := db.EnqueueTaskRun(ctx, taskID, owner, key, "inspect")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 99; i++ {
		chat, err := db.CreateChat(ctx, project, owner)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.EnqueueRun(ctx, chat, owner, key, "fill", "fill"); err != nil {
			t.Fatal(err)
		}
	}
	chatsBefore, err := db.ChatsOfProject(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	same, err := db.EnqueueTaskRun(ctx, taskID, owner, key, "inspect")
	if err != nil || same.ID != original.ID {
		t.Fatalf("full queue replay: %+v %v", same, err)
	}
	_, err = db.EnqueueTaskRun(ctx, taskID, owner, "00000000-0000-4000-8000-000000000002", "different")
	if !errors.Is(err, store.ErrRunQueueFull) {
		t.Fatalf("queue should be full: %v", err)
	}
	chatsAfter, err := db.ChatsOfProject(ctx, project)
	if err != nil || len(chatsBefore) != len(chatsAfter) {
		t.Fatalf("queue created chat: %d %d %v", len(chatsBefore), len(chatsAfter), err)
	}
}
