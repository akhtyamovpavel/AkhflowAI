package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/testdb"
	"akhflow.ai/akhflow/internal/toolbox"
)

func TestRunInjectsMemoryIntoSystemPrompt(t *testing.T) {
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
	owner, err := db.CreateUser(ctx, "memory-agent-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "memory-agent", "Memory Agent", owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddMemory(ctx, &project, owner, "project", "prefer short replies"); err != nil {
		t.Fatal(err)
	}
	var captured string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if messages, ok := body["messages"].([]any); ok && len(messages) > 0 {
			if first, ok := messages[0].(map[string]any); ok {
				if content, ok := first["content"].(string); ok {
					captured = content
				}
			}
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer model.Close()
	agent := Agent{
		Client: NewClient(model.URL, "fixture"),
		Deps:   toolbox.Deps{Store: db, ActorID: owner, Actor: "owner@example.invalid"},
	}
	result, err := agent.Run(ctx, "hello", "memory-agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("no result")
	}
	if !strings.Contains(captured, "prefer short replies") {
		t.Errorf("system prompt missing memory note: %s", captured)
	}
	if !strings.Contains(captured, "[project]") {
		t.Errorf("system prompt missing memory scope: %s", captured)
	}
}
