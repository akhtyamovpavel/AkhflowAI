package server

import (
	"bufio"
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
	"akhflow.ai/akhflow/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSSEAuthentication(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_SSE_TEST_DSN")
	if dsn == "" {
		t.Skip("set AKHFLOW_SSE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("sse_auth_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	params := parsed.Query()
	params.Set("search_path", schema)
	parsed.RawQuery = params.Encode()
	db, err := store.New(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateUser(ctx, "owner@example.invalid", "Owner", "unused")
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := db.CreateUser(ctx, "other@example.invalid", "Other", "unused")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "private", "Private", owner)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateProject(ctx, "other", "Other", outsider)
	if err != nil {
		t.Fatal(err)
	}
	auth := &Auth{Store: db, Secret: []byte("sse-test-signing-secret"), TokenTTL: time.Hour}
	ownerToken, err := auth.CreateToken(owner)
	if err != nil {
		t.Fatal(err)
	}
	otherToken, err := auth.CreateToken(outsider)
	if err != nil {
		t.Fatal(err)
	}
	expiredAuth := &Auth{Store: db, Secret: auth.Secret, TokenTTL: -time.Hour}
	expiredToken, err := expiredAuth.CreateToken(owner)
	if err != nil {
		t.Fatal(err)
	}
	ghostToken, err := auth.CreateToken(99999999)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	server := httptest.NewServer(New(db, auth, nil, bus).Handler())
	defer server.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	request := func(t *testing.T, path, bearer string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	query := func(token string) string { return "?token=" + url.QueryEscape(token) }
	for _, test := range []struct {
		name, path, bearer string
		status             int
	}{
		{"missing", "/api/projects/private/events", "", 401},
		{"invalid query", "/api/projects/private/events" + query("invalid"), "", 401},
		{"expired query", "/api/projects/private/events" + query(expiredToken), "", 401},
		{"deleted user", "/api/projects/private/events" + query(ghostToken), "", 401},
		{"outsider query", "/api/projects/private/events" + query(otherToken), "", 403},
		{"bearer wins over query", "/api/projects/private/events" + query(ownerToken), otherToken, 403},
		{"invalid bearer wins over query", "/api/projects/private/events" + query(ownerToken), "invalid", 401},
		{"query not accepted elsewhere", "/api/projects/private/tasks" + query(ownerToken), "", 401},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := request(t, test.path, test.bearer)
			defer response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("status %d, want %d", response.StatusCode, test.status)
			}
		})
	}
	for _, test := range []struct {
		name, path, bearer string
	}{
		{"query", "/api/projects/private/events" + query(ownerToken), ""},
		{"bearer", "/api/projects/private/events", ownerToken},
		{"bearer wins", "/api/projects/private/events" + query(otherToken), ownerToken},
	} {
		t.Run(test.name, func(t *testing.T) {
			stop := make(chan struct{})
			defer close(stop)
			go func() {
				ticker := time.NewTicker(25 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-stop:
						return
					case <-ticker.C:
						bus.Publish(events.Event{ProjectID: other, Type: "foreign", Title: "not visible"})
						bus.Publish(events.Event{ProjectID: project, Type: "expected", Title: "visible"})
					}
				}
			}()
			response := request(t, test.path, test.bearer)
			defer response.Body.Close()
			if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
				t.Fatalf("unexpected SSE response: %d", response.StatusCode)
			}
			line, err := bufio.NewReader(response.Body).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			var event events.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			if event.Type != "expected" || event.ProjectID != project {
				t.Fatalf("wrong project event: %+v", event)
			}
		})
	}
	t.Run("headers before first event and disconnect cleanup", func(t *testing.T) {
		finished := make(chan struct{})
		handler := New(db, auth, nil, bus).Handler()
		stream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(finished)
			handler.ServeHTTP(w, r)
		}))
		defer stream.Close()
		req, err := http.NewRequest(http.MethodGet, stream.URL+"/api/projects/private/events", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+ownerToken)
		response, err := client.Do(req)
		if err != nil {
			t.Fatalf("headers not received before an event: %v", err)
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			t.Fatalf("unexpected status: %d", response.StatusCode)
		}
		bus.Publish(events.Event{ProjectID: project, Type: "after_connect"})
		line, err := bufio.NewReader(response.Body).ReadString('\n')
		if err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		if !strings.Contains(line, "after_connect") {
			response.Body.Close()
			t.Fatalf("event missing after headers: %q", line)
		}
		response.Body.Close()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Fatal("SSE handler did not stop after disconnect")
		}
	})
}
