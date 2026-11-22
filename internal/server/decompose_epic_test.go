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

func TestDecomposeEpic(t *testing.T) {
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
	owner, err := db.CreateUser(ctx, "decompose-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := db.CreateUser(ctx, "decompose-viewer@example.invalid", "Viewer", "hash")
	if err != nil {
		t.Fatal(err)
	}
	editor, err := db.CreateUser(ctx, "decompose-editor@example.invalid", "Editor", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "decompose", "Decompose", owner)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.SetProjectMemberRole(ctx, project, owner, viewer, "viewer"); err != nil || !ok {
		t.Fatal(err)
	}
	if ok, err := db.SetProjectMemberRole(ctx, project, owner, editor, "editor"); err != nil || !ok {
		t.Fatal(err)
	}
	epicID, err := db.CreateEpic(ctx, project, "Epic", "", domain.StatusBacklog)
	if err != nil {
		t.Fatal(err)
	}
	tool := toolbox.ByName("decompose_epic")
	deps := toolbox.Deps{Store: db, ActorID: owner, Actor: "owner@example.invalid"}

	// Auto-assignment: viewer is excluded, editor (0 open tasks) wins.
	args, _ := json.Marshal(map[string]any{
		"project": "decompose",
		"epic_id": epicID,
		"tasks": []map[string]any{
			{"title": "One", "description": "first"},
			{"title": "Two"},
		},
	})
	res, err := tool.Handler(ctx, deps, args)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ok {
		t.Fatalf("decompose: %s", res.Message)
	}
	data, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("unexpected data: %T", res.Data)
	}
	ids, ok := data["task_ids"].([]int64)
	if !ok || len(ids) != 2 {
		t.Fatalf("unexpected task_ids: %v", data["task_ids"])
	}
	wantAssignees := map[int64]int64{ids[0]: owner, ids[1]: editor}
	for _, id := range ids {
		task, err := db.TaskByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if task == nil {
			t.Fatalf("task %d missing", id)
		}
		if task.EpicID == nil || *task.EpicID != epicID {
			t.Errorf("task %d not attached to epic", id)
		}
		if task.AssigneeID == nil || *task.AssigneeID != wantAssignees[id] {
			t.Errorf("task %d assigned to %v, want %d", id, task.AssigneeID, wantAssignees[id])
		}
	}

	// Explicit assignee: viewer is allowed (membership check only).
	args, _ = json.Marshal(map[string]any{
		"project": "decompose",
		"epic_id": epicID,
		"tasks": []map[string]any{
			{"title": "Explicit", "assignee": "decompose-viewer@example.invalid"},
		},
	})
	res, err = tool.Handler(ctx, deps, args)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = res.Data.(map[string]any)
	ids, _ = data["task_ids"].([]int64)
	if len(ids) != 1 {
		t.Fatalf("unexpected task_ids: %v", data["task_ids"])
	}
	task, err := db.TaskByID(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if task.AssigneeID == nil || *task.AssigneeID != viewer {
		t.Errorf("explicit assignee: got %v, want viewer %d", task.AssigneeID, viewer)
	}

	// Prevalidation: 3rd task has an unknown assignee — nothing is created.
	before, err := db.ListTasks(ctx, project, "")
	if err != nil {
		t.Fatal(err)
	}
	beforeCount := len(before)
	args, _ = json.Marshal(map[string]any{
		"project": "decompose",
		"epic_id": epicID,
		"tasks": []map[string]any{
			{"title": "Valid one"},
			{"title": "Valid two"},
			{"title": "Bad", "assignee": "ghost@example.invalid"},
		},
	})
	_, err = tool.Handler(ctx, deps, args)
	if err == nil {
		t.Fatal("expected error for unknown assignee")
	}
	after, err := db.ListTasks(ctx, project, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != beforeCount {
		t.Errorf("prevalidation failed: created %d tasks, want 0", len(after)-beforeCount)
	}

	// Foreign epic rejected.
	other, err := db.CreateProject(ctx, "other-decompose", "Other", owner)
	if err != nil {
		t.Fatal(err)
	}
	otherEpic, err := db.CreateEpic(ctx, other, "Foreign", "", domain.StatusBacklog)
	if err != nil {
		t.Fatal(err)
	}
	args, _ = json.Marshal(map[string]any{
		"project": "decompose",
		"epic_id": otherEpic,
		"tasks":   []map[string]any{{"title": "Nope"}},
	})
	_, err = tool.Handler(ctx, deps, args)
	if err == nil {
		t.Error("foreign epic accepted")
	}

}
