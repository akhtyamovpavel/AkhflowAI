package server

import (
	"context"
	"encoding/json"
	"testing"

	"akhflow.ai/akhflow/internal/domain"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/testdb"
	"akhflow.ai/akhflow/internal/toolbox"
)

func TestPrioritizeEpic(t *testing.T) {
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
	owner, err := db.CreateUser(ctx, "prioritize-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "prioritize", "Prioritize", owner)
	if err != nil {
		t.Fatal(err)
	}
	epicID, err := db.CreateEpic(ctx, project, "Epic", "", domain.StatusBacklog)
	if err != nil {
		t.Fatal(err)
	}
	mkTask := func(title, status string) int64 {
		id, err := db.CreateTask(ctx, domain.Task{
			ProjectID: project, Title: title, Status: status, Priority: domain.PriorityMedium,
			AuthorID: owner, EpicID: &epicID,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	dep1 := mkTask("dep1", domain.StatusTodo)
	dep2 := mkTask("dep2", domain.StatusTodo)
	dep3 := mkTask("dep3", domain.StatusTodo)
	blocked := mkTask("blocked", domain.StatusTodo)
	ready := mkTask("ready", domain.StatusTodo)
	done := mkTask("done", domain.StatusDone)
	for _, dep := range []int64{dep1, dep2, dep3} {
		if err := db.AddDependency(ctx, blocked, dep); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AddDependency(ctx, ready, dep1); err != nil {
		t.Fatal(err)
	}
	tool := toolbox.ByName("prioritize_epic")
	deps := toolbox.Deps{Store: db, ActorID: owner, Actor: "owner@example.invalid"}
	args, _ := json.Marshal(map[string]any{"project": "prioritize", "epic_id": epicID})
	res, err := tool.Handler(ctx, deps, args)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ok {
		t.Fatalf("prioritize: %s", res.Message)
	}
	data, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("unexpected data: %T", res.Data)
	}
	tasks, ok := data["tasks"].([]map[string]any)
	if !ok {
		t.Fatalf("unexpected tasks: %v", data["tasks"])
	}
	byID := map[int64]map[string]any{}
	for _, t := range tasks {
		id, _ := t["id"].(int64)
		byID[id] = t
	}
	if len(byID) != 5 {
		t.Fatalf("expected 5 open tasks in result, got %d: %v", len(byID), byID)
	}
	if _, found := byID[done]; found {
		t.Error("done task included in result")
	}
	if got := byID[ready]["priority"]; got != domain.PriorityMedium {
		t.Errorf("ready task priority = %v, want medium (1 unfinished dep)", got)
	}
	if got := byID[blocked]["priority"]; got != domain.PriorityLow {
		t.Errorf("blocked task priority = %v, want low (3 unfinished deps)", got)
	}
	// Verify persisted priorities.
	task, err := db.TaskByID(ctx, blocked)
	if err != nil {
		t.Fatal(err)
	}
	if task.Priority != domain.PriorityLow {
		t.Errorf("persisted blocked priority = %s, want low", task.Priority)
	}
	task, err = db.TaskByID(ctx, done)
	if err != nil {
		t.Fatal(err)
	}
	if task.Priority != domain.PriorityMedium {
		t.Errorf("done task priority changed to %s, want unchanged medium", task.Priority)
	}
}
