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

func TestMemoryEndpointsAndTools(t *testing.T) {
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
	owner, err := db.CreateUser(ctx, "memory-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := db.CreateUser(ctx, "memory-viewer@example.invalid", "Viewer", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "memory", "Memory", owner)
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

	// REST: create project memory.
	create := request(owner, "POST", "/api/projects/memory/memories", `{"scope":"project","content":"prefer short replies"}`)
	if create.Code != 201 {
		t.Fatalf("create: %d %s", create.Code, create.Body)
	}
	var created map[string]any
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	memoryID := int64(created["id"].(float64))

	// REST: list includes it.
	list := request(owner, "GET", "/api/projects/memory/memories", "")
	if list.Code != 200 {
		t.Fatalf("list: %d", list.Code)
	}
	var memories []map[string]any
	if err := json.Unmarshal(list.Body.Bytes(), &memories); err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 {
		t.Fatalf("list: got %d memories", len(memories))
	}

	// REST: viewer cannot create (write role).
	blocked := request(viewer, "POST", "/api/projects/memory/memories", `{"scope":"project","content":"nope"}`)
	if blocked.Code != 403 {
		t.Errorf("viewer create: got %d, want 403", blocked.Code)
	}

	// REST: viewer cannot delete someone else's project memory.
	blocked = request(viewer, "DELETE", fmt.Sprintf("/api/projects/memory/memories/%d", memoryID), "")
	if blocked.Code != 403 {
		t.Errorf("viewer delete: got %d, want 403", blocked.Code)
	}

	// REST: owner can delete someone else's project memory.
	deleted := request(owner, "DELETE", fmt.Sprintf("/api/projects/memory/memories/%d", memoryID), "")
	if deleted.Code != 204 {
		t.Errorf("owner delete: got %d, want 204", deleted.Code)
	}

	// Tool: add + list + delete.
	tool := toolbox.ByName("add_memory")
	deps := toolbox.Deps{Store: db, ActorID: owner, Actor: "owner@example.invalid"}
	args, _ := json.Marshal(map[string]any{"project": "memory", "scope": "project", "content": "tool memory"})
	res, err := tool.Handler(ctx, deps, args)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := res.Data.(map[string]any)
	toolMemoryID := data["id"].(int64)

	listTool := toolbox.ByName("list_memories")
	args, _ = json.Marshal(map[string]any{"project": "memory"})
	res, err = listTool.Handler(ctx, deps, args)
	if err != nil {
		t.Fatal(err)
	}
	listed, ok := res.Data.([]domain.Memory)
	if !ok {
		t.Fatalf("unexpected list data: %T", res.Data)
	}
	if len(listed) != 1 || listed[0].Content != "tool memory" {
		t.Errorf("unexpected list: %+v", listed)
	}

	deleteTool := toolbox.ByName("delete_memory")
	viewerDeps := toolbox.Deps{Store: db, ActorID: viewer, Actor: "viewer@example.invalid"}
	args, _ = json.Marshal(map[string]any{"project": "memory", "memory_id": toolMemoryID})
	_, err = deleteTool.Handler(ctx, viewerDeps, args)
	if err == nil {
		t.Error("viewer delete_memory: expected error")
	}

	// Owner deletes someone else's project memory via tool.
	args, _ = json.Marshal(map[string]any{"project": "memory", "memory_id": toolMemoryID})
	res, err = deleteTool.Handler(ctx, deps, args)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ok {
		t.Errorf("owner delete_memory: %s", res.Message)
	}
}
