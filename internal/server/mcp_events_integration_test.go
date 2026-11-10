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
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/events"
	"akhflow.ai/akhflow/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This exercises the real MCP stdio binary in a different process from the API.
func TestMCPEventsAcrossProcesses(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_MCP_EVENTS_TEST_DSN")
	if dsn == "" {
		t.Skip("AKHFLOW_MCP_EVENTS_TEST_DSN is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("mcp_events_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	scoped := parsed.String()
	db, err := store.New(scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = admin.Exec(ctx, "INSERT INTO "+schema+`.mcp_events(id,event,created_at)
		SELECT md5(i::text), '{"type":"fixture","project_id":1}'::jsonb,
		CASE WHEN i=1 THEN now() - interval '10 minutes' ELSE now() END
		FROM generate_series(1,10005) AS i`)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateUser(ctx, "mcp-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := db.CreateUser(ctx, "mcp-outsider@example.invalid", "Outsider", "hash")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := db.CreateUser(ctx, "mcp-viewer@example.invalid", "Viewer", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "event-project", "Project", owner)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.SetProjectMemberRole(ctx, project, owner, viewer, "viewer"); err != nil || !ok {
		t.Fatalf("add viewer: %v", err)
	}
	_, err = db.CreateProject(ctx, "other-project", "Other", outsider)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	listenCtx, stop := context.WithCancel(ctx)
	defer stop()
	if err := events.Listen(listenCtx, scoped, bus, ""); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		var count, expired int
		err := admin.QueryRow(ctx, "SELECT count(*), count(*) FILTER (WHERE created_at < now() - interval '5 minutes') FROM "+schema+".mcp_events").Scan(&count, &expired)
		if err == nil && count <= 10000 && expired == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("event cleanup: count=%d expired=%d error=%v", count, expired, err)
		}
	}
	auth := &Auth{Store: db, Secret: []byte("mcp-events-test-only-key"), TokenTTL: time.Hour}
	api := httptest.NewServer(New(db, auth, nil, bus).Handler())
	defer api.Close()
	stream := func(user int64, slug string) (*http.Response, <-chan events.Event) {
		t.Helper()
		token, e := auth.CreateToken(user)
		if e != nil {
			t.Fatal(e)
		}
		req, _ := http.NewRequest("GET", api.URL+"/api/projects/"+slug+"/events", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		if response.StatusCode != 200 {
			t.Fatalf("stream status %d", response.StatusCode)
		}
		ch := make(chan events.Event, 16)
		go func() {
			defer close(ch)
			scanner := bufio.NewScanner(response.Body)
			scanner.Buffer(make([]byte, 4096), 65536)
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
	ownerResponse, ownerEvents := stream(owner, "event-project")
	defer ownerResponse.Body.Close()
	otherResponse, otherEvents := stream(outsider, "other-project")
	defer otherResponse.Body.Close()
	token, _ := auth.CreateToken(outsider)
	req, _ := http.NewRequest("GET", api.URL+"/api/projects/event-project/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 && resp.StatusCode != 404 {
		t.Fatalf("outsider stream: %d", resp.StatusCode)
	}
	observer, err := pgx.Connect(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close(ctx)
	if _, err = observer.Exec(ctx, "LISTEN akhflow_mcp_events"); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "akhflow")
	build := exec.Command("go", "build", "-o", binary, "./cmd/akhflow")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	create := func(title string) {
		t.Helper()
		request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "create_task", "arguments": map[string]any{"project": "event-project", "title": title}}})
		command := exec.Command(binary, "serve", "mcp")
		command.Dir = t.TempDir()
		command.Env = append(os.Environ(), "DATABASE_URL="+scoped, fmt.Sprintf("MCP_USER_ID=%d", owner), "MCP_USER_EMAIL=mcp-owner@example.invalid")
		command.Stdin = strings.NewReader(string(request) + "\n")
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("MCP: %v %s", err, out)
		}
		if strings.Contains(string(out), `"isError":true`) {
			t.Fatalf("MCP tool failed: %s", out)
		}
	}
	receive := func(title string) {
		t.Helper()
		select {
		case event := <-ownerEvents:
			if event.Type != "task_created" || event.ProjectID != project || event.Title != title {
				t.Fatalf("event: %+v", event)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("MCP event did not reach SSE")
		}
		select {
		case event := <-ownerEvents:
			t.Fatalf("duplicate: %+v", event)
		case event := <-otherEvents:
			t.Fatalf("cross-project leak: %+v", event)
		case <-time.After(300 * time.Millisecond):
		}
	}
	create("private-title")
	receive("private-title")
	ownerToken, _ := auth.CreateToken(owner)
	localRequest, _ := http.NewRequest("POST", api.URL+"/api/projects/event-project/tasks", strings.NewReader(`{"title":"local-task"}`))
	localRequest.Header.Set("Authorization", "Bearer "+ownerToken)
	localRequest.Header.Set("Content-Type", "application/json")
	localResponse, err := http.DefaultClient.Do(localRequest)
	if err != nil {
		t.Fatal(err)
	}
	localResponse.Body.Close()
	if localResponse.StatusCode != http.StatusCreated {
		t.Fatalf("local create: %d", localResponse.StatusCode)
	}
	receive("local-task")
	viewerResponse, viewerEvents := stream(viewer, "event-project")
	defer viewerResponse.Body.Close()
	create("viewer-visible")
	receive("viewer-visible")
	select {
	case event, ok := <-viewerEvents:
		if !ok || event.Title != "viewer-visible" {
			t.Fatalf("viewer event: %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("viewer did not receive MCP event")
	}
	if _, err := admin.Exec(ctx, "DELETE FROM "+schema+".project_members WHERE project_id=$1 AND user_id=$2", project, viewer); err != nil {
		t.Fatal(err)
	}
	create("after-revocation")
	receive("after-revocation")
	select {
	case event, ok := <-viewerEvents:
		if ok {
			t.Fatalf("revoked viewer received MCP event: %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revoked viewer stream remained open")
	}
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	notification, err := observer.WaitForNotification(waitCtx)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(notification.Payload) {
		t.Fatalf("notification leaked data: %q", notification.Payload)
	}
	// A duplicate notification for the same ID must not reach SSE twice.
	if _, err = observer.Exec(ctx, "SELECT pg_notify('akhflow_mcp_events', $1)", notification.Payload); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-ownerEvents:
		t.Fatalf("replayed event: %+v", event)
	case <-time.After(300 * time.Millisecond):
	}
	// Terminate only this test schema's listener and wait for a fresh connection.
	var oldPID int32
	err = admin.QueryRow(ctx, "SELECT pid FROM pg_stat_activity WHERE application_name=$1 AND datname=current_database() LIMIT 1", "akhflow-events-"+schema).Scan(&oldPID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "SELECT pg_terminate_backend($1)", oldPID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		var count int
		_ = admin.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE application_name=$1 AND pid <> $2 AND datname=current_database()", "akhflow-events-"+schema, oldPID).Scan(&count)
		if count > 0 {
			time.Sleep(200 * time.Millisecond)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listener did not reconnect")
		}
		time.Sleep(100 * time.Millisecond)
	}
	create("after-reconnect")
	receive("after-reconnect")
	create(strings.Repeat("x", 7000))
	receive("")
	ownerResponse.Body.Close()
	otherResponse.Body.Close()
	create("without-subscribers")
}
