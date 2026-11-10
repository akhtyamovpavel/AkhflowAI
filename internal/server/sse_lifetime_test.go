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
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type testStream struct {
	events <-chan events.Event
	closed <-chan error
	cancel context.CancelFunc
}

func openTestStream(t *testing.T, client *http.Client, url, token string, bus *events.Bus, project int64) testStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response := make(chan *http.Response, 1)
	failures := make(chan error, 1)
	go func() {
		res, err := client.Do(req)
		if err != nil {
			failures <- err
			return
		}
		response <- res
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	pulse := time.NewTicker(20 * time.Millisecond)
	defer pulse.Stop()
	var res *http.Response
	for res == nil {
		select {
		case res = <-response:
		case err := <-failures:
			cancel()
			t.Fatal(err)
		case <-pulse.C:
			bus.Publish(events.Event{Type: "task_updated", ProjectID: project, Title: "probe"})
		case <-deadline.C:
			cancel()
			t.Fatal("SSE did not start")
		}
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		cancel()
		t.Fatalf("SSE status: %d", res.StatusCode)
	}
	received := make(chan events.Event, 128)
	closed := make(chan error, 1)
	go func() {
		defer res.Body.Close()
		reader := bufio.NewReader(res.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				closed <- err
				return
			}
			if strings.HasPrefix(line, "data: ") {
				var event events.Event
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
					closed <- err
					return
				}
				received <- event
			}
		}
	}()
	return testStream{events: received, closed: closed, cancel: cancel}
}

func awaitTestEvent(t *testing.T, stream testStream, title string, forbidden ...string) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-stream.events:
			for _, denied := range forbidden {
				if event.Title == denied {
					t.Fatalf("received event from another project: %q", denied)
				}
			}
			if event.Title == title {
				return
			}
		case err := <-stream.closed:
			t.Fatalf("stream closed before event %q: %v", title, err)
		case <-timer.C:
			t.Fatalf("event %q not delivered", title)
		}
	}
}

func awaitTestClosed(t *testing.T, stream testStream, forbidden string) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-stream.events:
			if event.Title == forbidden {
				t.Errorf("delivered %q after access expired or was revoked", forbidden)
				return
			}
		case <-stream.closed:
			for {
				select {
				case event := <-stream.events:
					if event.Title == forbidden {
						t.Errorf("delivered %q after access expired or was revoked", forbidden)
					}
				default:
					return
				}
			}
		case <-timer.C:
			t.Error("stream remained open after access ended")
			return
		}
	}
}

func TestSSEClosesAfterExpiryAndRevocation(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_ISOLATION_TEST_DSN")
	if dsn == "" {
		t.Skip("AKHFLOW_ISOLATION_TEST_DSN is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("sse_lifetime_%d", time.Now().UnixNano())
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
	db, err := store.New(parsed.String())
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
	viewer, err := db.CreateUser(ctx, "viewer@example.invalid", "Viewer", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "alpha", "Alpha", owner)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateProject(ctx, "beta", "Beta", owner)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.SetProjectMemberRole(ctx, project, owner, viewer, "viewer"); err != nil || !ok {
		t.Fatalf("viewer membership: %v", err)
	}
	bus := events.NewBus()
	auth := &Auth{Store: db, Secret: []byte("test-only-signing-key"), TokenTTL: time.Hour}
	server := httptest.NewServer(New(db, auth, nil, bus).Handler())
	defer server.Close()
	ownerToken, err := auth.CreateToken(owner)
	if err != nil {
		t.Fatal(err)
	}
	viewerToken, err := auth.CreateToken(viewer)
	if err != nil {
		t.Fatal(err)
	}
	ownerStream := openTestStream(t, server.Client(), server.URL+"/api/projects/alpha/events", ownerToken, bus, project)
	defer ownerStream.cancel()
	betaStream := openTestStream(t, server.Client(), server.URL+"/api/projects/beta/events", ownerToken, bus, other)
	defer betaStream.cancel()
	viewerStream := openTestStream(t, server.Client(), server.URL+"/api/projects/alpha/events", viewerToken, bus, project)
	defer viewerStream.cancel()
	bus.Publish(events.Event{Type: "task_updated", ProjectID: project, Title: "before-revocation"})
	awaitTestEvent(t, viewerStream, "before-revocation")
	if _, err := admin.Exec(ctx, "DELETE FROM "+schema+".project_members WHERE project_id=$1 AND user_id=$2", project, viewer); err != nil {
		t.Fatal(err)
	}
	bus.Publish(events.Event{Type: "task_updated", ProjectID: project, Title: "after-revocation"})
	awaitTestClosed(t, viewerStream, "after-revocation")
	awaitTestEvent(t, ownerStream, "after-revocation")
	bus.Publish(events.Event{Type: "task_updated", ProjectID: other, Title: "other-project"})
	awaitTestEvent(t, betaStream, "other-project", "before-revocation", "after-revocation")

	expires := time.Now().Add(3 * time.Second).Truncate(time.Second)
	shortToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": fmt.Sprint(owner), "exp": expires.Unix()}).SignedString(auth.Secret)
	if err != nil {
		t.Fatal(err)
	}
	shortStream := openTestStream(t, server.Client(), server.URL+"/api/projects/alpha/events", shortToken, bus, project)
	defer shortStream.cancel()
	bus.Publish(events.Event{Type: "task_updated", ProjectID: project, Title: "before-expiry"})
	awaitTestEvent(t, shortStream, "before-expiry")
	time.Sleep(time.Until(expires) + 100*time.Millisecond)
	awaitTestClosed(t, shortStream, "after-expiry")
	bus.Publish(events.Event{Type: "task_updated", ProjectID: project, Title: "after-expiry"})
	awaitTestEvent(t, ownerStream, "after-expiry")
	db.Close()
	bus.Publish(events.Event{Type: "task_updated", ProjectID: project, Title: "after-database-failure"})
	awaitTestClosed(t, ownerStream, "after-database-failure")
}
