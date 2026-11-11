package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/domain"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/testdb"
	"akhflow.ai/akhflow/internal/toolbox"
)

func TestAcceptanceCriteria(t *testing.T) {
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
	owner, err := db.CreateUser(ctx, "criteria-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := db.CreateUser(ctx, "criteria-viewer@example.invalid", "Viewer", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "criteria", "Criteria", owner)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.SetProjectMemberRole(ctx, project, owner, viewer, "viewer"); err != nil || !ok {
		t.Fatal(err)
	}
	auth := &Auth{Store: db, Secret: []byte("fixture-secret"), TokenTTL: time.Hour}
	handler := New(db, auth, nil, nil).Handler()
	request := func(user int64, method, path, body string) *httptest.ResponseRecorder {
		token, err := auth.CreateToken(user)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	create := request(owner, "POST", "/api/projects/criteria/tasks", `{"title":"Task","acceptance_criteria":"готово"}`)
	if create.Code != 201 {
		t.Fatalf("create: %d %s", create.Code, create.Body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err = json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/tasks/%d", created.ID)
	get := request(owner, "GET", path, "")
	var detail struct {
		Task domain.Task `json:"task"`
	}
	if err = json.Unmarshal(get.Body.Bytes(), &detail); err != nil || detail.Task.AcceptanceCriteria != "готово" {
		t.Fatalf("get: %s %v", get.Body, err)
	}
	for _, value := range []string{"null", "123", "true", `"\u0000"`, `"` + strings.Repeat("я", 8193) + `"`} {
		payload := `{"acceptance_criteria":` + value + `}`
		if got := request(owner, "PATCH", path, payload); got.Code != 400 {
			t.Fatalf("invalid patch %q: %d", value[:min(len(value), 10)], got.Code)
		}
		if got := request(owner, "POST", "/api/projects/criteria/tasks", `{"title":"Invalid","acceptance_criteria":`+value+`}`); got.Code != 400 {
			t.Fatalf("invalid create: %d", got.Code)
		}
	}
	if got := request(viewer, "PATCH", path, `{"acceptance_criteria":"forbidden"}`); got.Code != 403 {
		t.Fatalf("viewer: %d", got.Code)
	}
	boundary, err := json.Marshal(map[string]string{"acceptance_criteria": strings.Repeat("я", 8192)})
	if err != nil {
		t.Fatal(err)
	}
	if got := request(owner, "PATCH", path, string(boundary)); got.Code != 200 {
		t.Fatalf("valid byte boundary: %d", got.Code)
	}
	if got := request(owner, "PATCH", path, `{"acceptance_criteria":"проверка"}`); got.Code != 200 {
		t.Fatalf("patch: %d %s", got.Code, got.Body)
	}
	run, err := db.EnqueueTaskRun(ctx, created.ID, owner, "00000000-0000-4000-8000-000000000001", "inspect")
	if err != nil {
		t.Fatal(err)
	}
	set := toolbox.ByName("set_task_acceptance_criteria")
	if set == nil {
		t.Fatal("tool missing")
	}
	args := json.RawMessage(fmt.Sprintf(`{"task_id":%d,"acceptance_criteria":"после"}`, created.ID))
	if _, err = set.Handler(ctx, toolbox.Deps{Store: db, ActorID: viewer}, args); err == nil {
		t.Fatal("viewer tool write accepted")
	}
	if _, err = set.Handler(ctx, toolbox.Deps{Store: db, ActorID: owner}, args); err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.TaskRunSnapshot(ctx, run.ID)
	if err != nil || snapshot.AcceptanceCriteria != "проверка" {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
	prompt, err := taskRunPrompt(snapshot, "inspect")
	if err != nil || !strings.Contains(prompt, "проверка") || strings.Contains(prompt, "после") {
		t.Fatalf("snapshot prompt: %s %v", prompt, err)
	}
	task, err := db.TaskByID(ctx, created.ID)
	if err != nil || task.AcceptanceCriteria != "после" || task.Status != "todo" {
		t.Fatalf("task: %+v %v", task, err)
	}
	if _, err = set.Handler(ctx, toolbox.Deps{Store: db, ActorID: owner}, json.RawMessage(fmt.Sprintf(`{"task_id":%d,"acceptance_criteria":null}`, created.ID))); err == nil {
		t.Fatal("null accepted")
	}
	other, err := db.CreateProject(ctx, "criteria-other", "Other", owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = set.Handler(ctx, toolbox.Deps{Store: db, ActorID: owner, ProjectID: other}, args); err == nil {
		t.Fatal("cross-project write accepted")
	}
	createTool := toolbox.ByName("create_task")
	result, err := createTool.Handler(ctx, toolbox.Deps{Store: db, ActorID: owner}, json.RawMessage(`{"project":"criteria","title":"Tool","acceptance_criteria":"критерий"}`))
	if err != nil || !result.Ok {
		t.Fatalf("tool create: %+v %v", result, err)
	}
	if _, err = createTool.Handler(ctx, toolbox.Deps{Store: db, ActorID: owner}, json.RawMessage(`{"project":"criteria","title":"Bad","acceptance_criteria":null}`)); err == nil {
		t.Fatal("tool create accepted null")
	}
	old, err := db.CreateTask(ctx, domain.Task{ProjectID: project, AuthorID: owner, Title: "Old", Status: "todo", Priority: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	oldTask, err := db.TaskByID(ctx, old)
	if err != nil || oldTask.AcceptanceCriteria != "" {
		t.Fatalf("empty default: %+v %v", oldTask, err)
	}
	if got := request(owner, "PATCH", path, `{"acceptance_criteria":""}`); got.Code != 200 {
		t.Fatalf("clear: %d", got.Code)
	}
	due := time.Now().UTC().AddDate(0, 0, -1)
	master, err := db.CreateTask(ctx, domain.Task{ProjectID: project, AuthorID: owner, Title: "Recurring", Status: "todo", Priority: "medium", Recurring: "daily", DueDate: &due, AcceptanceCriteria: "повторить"})
	if err != nil {
		t.Fatal(err)
	}
	occurrence, err := db.CreateScheduledOccurrence(ctx, master, time.Now().UTC())
	if err != nil || occurrence == nil || occurrence.AcceptanceCriteria != "повторить" {
		t.Fatalf("recurring snapshot: %+v %v", occurrence, err)
	}
	storedOccurrence, err := db.TaskByID(ctx, occurrence.ID)
	if err != nil || storedOccurrence.AcceptanceCriteria != "повторить" {
		t.Fatalf("recurring stored criteria: %+v %v", storedOccurrence, err)
	}
}
