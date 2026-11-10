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

	"akhflow.ai/akhflow/internal/events"
	"akhflow.ai/akhflow/internal/llm"
	"akhflow.ai/akhflow/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAgentChatPublishesProjectEvents(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_ISOLATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set AKHFLOW_ISOLATION_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("agent_events_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
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
	project, err := db.CreateProject(ctx, "events-project", "Events project", owner)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateProject(ctx, "other-project", "Other project", owner)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(ctx, project, owner)
	if err != nil {
		t.Fatal(err)
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.ChatRequest
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected model request", http.StatusBadRequest)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
			http.Error(w, "invalid model request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if req.Messages[len(req.Messages)-1].Role == "tool" {
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Created"}}]}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"create_task","arguments":"{\"project\":\"events-project\",\"title\":\"Created by agent\"}"}}]}}]}`)
	}))
	defer model.Close()
	bus := events.NewBus()
	auth := &Auth{Store: db, Secret: []byte("agent-events-test-signing-key"), TokenTTL: time.Hour}
	token, err := auth.CreateToken(owner)
	if err != nil {
		t.Fatal(err)
	}
	api := New(db, auth, llm.NewClient(model.URL, "fake"), bus).Handler()
	for _, tc := range []struct {
		name string
		path string
	}{
		{"stateless", "/api/agent/chat"},
		{"stored", fmt.Sprintf("/api/chats/%d/messages", chat)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub := bus.Subscribe(project)
			defer bus.Unsubscribe(sub)
			otherSub := bus.Subscribe(other)
			defer bus.Unsubscribe(otherSub)
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"message":"Create a task"}`))
			req.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			api.ServeHTTP(response, req)
			if response.Code != http.StatusOK {
				t.Fatalf("chat returned %d: %s", response.Code, response.Body.String())
			}
			select {
			case event := <-sub.Ch:
				if event.Type != "task_created" || event.ProjectID != project || event.TaskID == 0 || event.Title != "Created by agent" || event.Actor != "owner@example.invalid" {
					t.Fatalf("unexpected event: %+v", event)
				}
			default:
				t.Fatal("project subscriber did not receive task_created")
			}
			select {
			case event := <-otherSub.Ch:
				t.Fatalf("event leaked to another project: %+v", event)
			default:
			}
		})
	}
}
