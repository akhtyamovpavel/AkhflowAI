package server

import (
	"context"
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

func TestImportEventsAfterCommit(t *testing.T) {
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
	schema := fmt.Sprintf("import_events_%d", time.Now().UnixNano())
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
	owner, err := db.CreateUser(ctx, "import@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "imports", "Imports", owner)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateProject(ctx, "other", "Other", owner)
	if err != nil {
		t.Fatal(err)
	}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/1/boards/board123":
			fmt.Fprint(w, `{"id":"board123"}`)
		case "/1/boards/board123/lists":
			fmt.Fprint(w, `[{"id":"list1","name":"List"}]`)
		case "/1/boards/board123/cards":
			fmt.Fprint(w, `[{"id":"card1","idList":"list1","name":"Trello task"}]`)
		case "/1/boards/empty123":
			fmt.Fprint(w, `{"id":"empty123"}`)
		case "/1/boards/empty123/lists":
			fmt.Fprint(w, `[{"id":"list2","name":"Empty list"}]`)
		case "/1/boards/empty123/cards":
			fmt.Fprint(w, `[]`)
		case "/1/boards/reject12":
			fmt.Fprint(w, `{"id":"reject12"}`)
		case "/1/boards/reject12/lists":
			fmt.Fprint(w, `[{"id":"list3","name":"Rejected list"}]`)
		case "/1/boards/reject12/cards":
			fmt.Fprint(w, `[{"id":"card3","idList":"list3","name":"Rejected"}]`)
		case "/rest/1/test/tasks.task.list.json":
			fmt.Fprint(w, `{"result":{"tasks":[{"id":"1","title":"Bitrix first"},{"id":"2","title":"Rejected"}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()
	fakeURL, err := url.Parse(fake.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport
	http.DefaultTransport = trelloTestTransport{base: fakeURL, transport: transport}
	defer func() { http.DefaultTransport = transport }()
	t.Setenv("TRELLO_IMPORT_USER_ID", fmt.Sprint(owner))
	t.Setenv("TRELLO_API_KEY", "fixture-key")
	t.Setenv("TRELLO_API_TOKEN", "fixture-token")
	t.Setenv("BITRIX24_ALLOWED_USER_ID", fmt.Sprint(owner))
	t.Setenv("BITRIX24_WEBHOOK_URL", "https://bitrix.example.invalid/rest/1/test")
	forwarded := []int{}
	bus := events.NewForwardingBus(func(event events.Event) {
		tasks, err := db.ListTasks(ctx, project, "")
		if err != nil {
			t.Errorf("read committed tasks: %v", err)
			return
		}
		forwarded = append(forwarded, len(tasks))
	})
	sub := bus.Subscribe(project)
	defer bus.Unsubscribe(sub)
	otherSub := bus.Subscribe(other)
	defer bus.Unsubscribe(otherSub)
	auth := &Auth{Store: db, Secret: []byte("import-events-fixture"), TokenTTL: time.Hour}
	token, err := auth.CreateToken(owner)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(db, auth, nil, bus).Handler()
	request := func(path, body string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/projects/imports/imports/"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response.Code
	}
	check := func(wantEvent bool, wantCount int) {
		t.Helper()
		if wantEvent {
			select {
			case event := <-sub.Ch:
				if event.Type != "tasks_imported" || event.ProjectID != project || event.Actor != "import@example.invalid" {
					t.Errorf("unexpected event: %+v", event)
				}
			default:
				t.Error("missing import event")
			}
			if len(forwarded) == 0 || forwarded[len(forwarded)-1] != wantCount {
				t.Errorf("event delivered before commit: counts=%v want=%d", forwarded, wantCount)
			}
		}
		select {
		case event := <-sub.Ch:
			t.Errorf("unexpected event: %+v", event)
		default:
		}
		select {
		case event := <-otherSub.Ch:
			t.Errorf("event leaked to another project: %+v", event)
		default:
		}
	}
	reject := func() {
		t.Helper()
		statement := "CREATE OR REPLACE FUNCTION " + schema + ".reject_import() RETURNS trigger AS $$ BEGIN IF NEW.title = 'Rejected' THEN RAISE EXCEPTION 'injected failure'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql"
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, "CREATE TRIGGER reject_import BEFORE INSERT ON "+schema+".tasks FOR EACH ROW EXECUTE FUNCTION "+schema+".reject_import()"); err != nil {
			t.Fatal(err)
		}
	}
	allow := func() {
		t.Helper()
		if _, err := admin.Exec(ctx, "DROP TRIGGER reject_import ON "+schema+".tasks"); err != nil {
			t.Fatal(err)
		}
	}
	reject()
	badCourse := `{"id":1,"tasks":[{"id":1,"name":"Rolled back"},{"id":2,"name":"Rejected"}]}`
	if got := request("akhcheck", badCourse); got == http.StatusOK {
		t.Fatal("Akhcheck insertion unexpectedly succeeded")
	}
	check(false, 0)
	if got := request("trello", `{"board_id":"reject12"}`); got == http.StatusOK {
		t.Fatal("Trello insertion unexpectedly succeeded")
	}
	check(false, 0)
	allow()
	course := `{"id":1,"tasks":[{"id":1,"name":"Akhcheck task"}]}`
	if got := request("akhcheck", course); got != http.StatusOK {
		t.Fatalf("Akhcheck import: %d", got)
	}
	check(true, 1)
	if got := request("akhcheck", course); got != http.StatusOK {
		t.Fatalf("Akhcheck repeat: %d", got)
	}
	check(false, 1)
	if got := request("trello", `{"board_id":"board123"}`); got != http.StatusOK {
		t.Fatalf("Trello import: %d", got)
	}
	check(true, 2)
	if got := request("trello", `{"board_id":"board123"}`); got != http.StatusOK {
		t.Fatalf("Trello repeat: %d", got)
	}
	check(false, 2)
	if got := request("trello", `{"board_id":"empty123"}`); got != http.StatusOK {
		t.Fatalf("empty list import: %d", got)
	}
	check(true, 2)
	if got := request("trello", `{"board_id":"empty123"}`); got != http.StatusOK {
		t.Fatalf("empty list repeat: %d", got)
	}
	check(false, 2)
	reject()
	if got := request("bitrix24", `{}`); got == http.StatusOK {
		t.Fatal("partial Bitrix import unexpectedly succeeded")
	}
	check(true, 3)
	allow()
	if got := request("bitrix24", `{}`); got != http.StatusOK {
		t.Fatalf("Bitrix retry: %d", got)
	}
	check(true, 4)
	if got := request("bitrix24", `{}`); got != http.StatusOK {
		t.Fatalf("Bitrix repeat: %d", got)
	}
	check(false, 4)
}
