package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/events"
	"akhflow.ai/akhflow/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAPIEventsAcrossProcesses(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_API_EVENTS_TEST_DSN")
	if dsn == "" {
		t.Skip("AKHFLOW_API_EVENTS_TEST_DSN is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("api_events_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	scoped := parsed.String()
	db, err := store.New(scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateUser(ctx, "api-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := db.CreateUser(ctx, "api-outsider@example.invalid", "Outsider", "hash")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := db.CreateUser(ctx, "api-viewer@example.invalid", "Viewer", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "shared-project", "Project", owner)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.SetProjectMemberRole(ctx, project, owner, viewer, "viewer"); err != nil || !ok {
		t.Fatalf("add viewer: %v", err)
	}
	if _, err := db.CreateProject(ctx, "other-project", "Other", outsider); err != nil {
		t.Fatal(err)
	}
	auth := &Auth{Store: db, Secret: []byte("api-events-test-key"), TokenTTL: time.Hour}
	ownerToken, err := auth.CreateToken(owner)
	if err != nil {
		t.Fatal(err)
	}
	outsiderToken, err := auth.CreateToken(outsider)
	if err != nil {
		t.Fatal(err)
	}
	viewerToken, err := auth.CreateToken(viewer)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "akhflow")
	build := exec.Command("go", "build", "-o", binary, "./cmd/akhflow")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	var processes []*exec.Cmd
	defer func() {
		for _, command := range processes {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	startAPI := func() string {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		command := exec.Command(binary, "serve", "api")
		command.Dir = t.TempDir()
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "DATABASE_URL=") && !strings.HasPrefix(entry, "API_PORT=") && !strings.HasPrefix(entry, "JWT_SECRET=") && !strings.HasPrefix(entry, "SMTP_") {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, "DATABASE_URL="+scoped, fmt.Sprintf("API_PORT=%d", port), "JWT_SECRET=api-events-test-key")
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		processes = append(processes, command)
		base := fmt.Sprintf("http://127.0.0.1:%d", port)
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			response, err := http.Get(base + "/api/health")
			if err == nil {
				response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return base
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("API did not start")
		return ""
	}
	first := startAPI()
	second := startAPI()
	stream := func(base, token, slug string) (*http.Response, <-chan events.Event) {
		t.Helper()
		request, err := http.NewRequest("GET", base+"/api/projects/"+slug+"/events", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 {
			t.Fatalf("stream: %d", response.StatusCode)
		}
		ch := make(chan events.Event, 16)
		go func() {
			defer close(ch)
			scanner := bufio.NewScanner(response.Body)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data: ") {
					var event events.Event
					if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) == nil {
						ch <- event
					}
				}
			}
		}()
		return response, ch
	}
	firstResponse, firstEvents := stream(first, ownerToken, "shared-project")
	defer firstResponse.Body.Close()
	secondResponse, secondEvents := stream(second, ownerToken, "shared-project")
	defer secondResponse.Body.Close()
	otherResponse, otherEvents := stream(second, outsiderToken, "other-project")
	defer otherResponse.Body.Close()
	create := func(base, title string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"title": title})
		request, err := http.NewRequest("POST", base+"/api/projects/shared-project/tasks", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+ownerToken)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("task: %d", response.StatusCode)
		}
	}
	check := func(ch <-chan events.Event, title string) {
		t.Helper()
		select {
		case event, ok := <-ch:
			if !ok || event.ProjectID != project || event.Type != "task_created" || event.Title != title {
				t.Fatalf("event: %+v", event)
			}
		case <-time.After(4 * time.Second):
			t.Fatalf("API event %q did not reach the other process", title)
		}
	}
	noExtras := func() {
		t.Helper()
		select {
		case event := <-firstEvents:
			t.Fatalf("duplicate at first API: %+v", event)
		case event := <-secondEvents:
			t.Fatalf("duplicate at second API: %+v", event)
		case event := <-otherEvents:
			t.Fatalf("project isolation failed: %+v", event)
		case <-time.After(400 * time.Millisecond):
		}
	}
	create(first, "from-first")
	check(firstEvents, "from-first")
	check(secondEvents, "from-first")
	noExtras()
	create(second, "from-second")
	check(firstEvents, "from-second")
	check(secondEvents, "from-second")
	noExtras()

	// A pre-upgrade MCP process emits an event without an origin marker.
	legacyID := "0123456789abcdef0123456789abcdef"
	legacy, err := json.Marshal(events.Event{Type: "task_created", ProjectID: project, Title: "legacy-mcp"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = admin.Exec(ctx, "INSERT INTO "+schema+".mcp_events(id,event) VALUES ($1,$2::jsonb)", legacyID, string(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "SELECT pg_notify('akhflow_mcp_events', $1)", legacyID); err != nil {
		t.Fatal(err)
	}
	check(firstEvents, "legacy-mcp")
	check(secondEvents, "legacy-mcp")
	noExtras()

	viewerResponse, viewerEvents := stream(second, viewerToken, "shared-project")
	defer viewerResponse.Body.Close()
	create(first, "viewer-visible")
	check(firstEvents, "viewer-visible")
	check(secondEvents, "viewer-visible")
	check(viewerEvents, "viewer-visible")
	noExtras()
	_, err = admin.Exec(ctx, "DELETE FROM "+schema+".project_members WHERE project_id=$1 AND user_id=$2", project, viewer)
	if err != nil {
		t.Fatal(err)
	}
	create(first, "after-revocation")
	check(firstEvents, "after-revocation")
	check(secondEvents, "after-revocation")
	select {
	case event, ok := <-viewerEvents:
		if ok {
			t.Fatalf("revoked viewer received an event: %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revoked viewer stream remained open")
	}
	noExtras()

	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "create_task",
			"arguments": map[string]any{"project": "shared-project", "title": "from-mcp"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mcp := exec.Command(binary, "serve", "mcp")
	mcp.Dir = t.TempDir()
	mcp.Env = append(os.Environ(), "DATABASE_URL="+scoped, fmt.Sprintf("MCP_USER_ID=%d", owner), "MCP_USER_EMAIL=api-owner@example.invalid")
	mcp.Stdin = strings.NewReader(string(request) + "\n")
	output, err := mcp.CombinedOutput()
	if err != nil || strings.Contains(string(output), `"isError":true`) {
		t.Fatalf("MCP: %v %s", err, output)
	}
	check(firstEvents, "from-mcp")
	check(secondEvents, "from-mcp")
	noExtras()
}
