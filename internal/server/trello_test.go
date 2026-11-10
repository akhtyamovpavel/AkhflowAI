package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

type trelloTestTransport struct {
	base      *url.URL
	transport http.RoundTripper
}

func (t trelloTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.base.Scheme
	clone.URL.Host = t.base.Host
	clone.Host = t.base.Host
	return t.transport.RoundTrip(clone)
}

func TestTrelloImportHTTP(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_TRELLO_TEST_DSN")
	if dsn == "" {
		t.Skip("AKHFLOW_TRELLO_TEST_DSN is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("trello_http_%d", time.Now().UnixNano())
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
	st, err := store.New(dsnURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	userID, err := st.CreateUser(ctx, "owner@example.com", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := st.CreateProject(ctx, "import-test", "Import", userID)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := st.CreateUser(ctx, "other@example.com", "Other", "hash")
	if err != nil {
		t.Fatal(err)
	}
	auth := &Auth{Store: st, Secret: []byte("test-key"), TokenTTL: time.Hour}
	ownerToken, err := auth.CreateToken(userID)
	if err != nil {
		t.Fatal(err)
	}
	otherToken, err := auth.CreateToken(otherID)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRELLO_IMPORT_USER_ID", fmt.Sprint(userID))
	t.Setenv("TRELLO_API_KEY", "example-key")
	t.Setenv("TRELLO_API_TOKEN", "example-token")
	largeCards := make([]map[string]string, 1004)
	for i := range largeCards {
		second := 2_000_000 - i
		if i == 999 || i == 1000 {
			second = 2_000_000 - 998
		}
		largeCards[i] = map[string]string{"id": fmt.Sprintf("%08x%016x", second, len(largeCards)-i), "idList": "list2", "name": "Card"}
	}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/1/boards/short123":
			fmt.Fprint(w, `{"id":"board123"}`)
		case "/1/boards/board123/lists":
			fmt.Fprint(w, `[{"id":"list1","name":"Queue"}]`)
		case "/1/boards/board123/cards":
			fmt.Fprint(w, `[{"id":"card1","idList":"list1","name":"Example","desc":"Details"}]`)
		case "/1/boards/large123":
			fmt.Fprint(w, `{"id":"large123"}`)
		case "/1/boards/large123/lists":
			fmt.Fprint(w, `[{"id":"list2","name":"Large"}]`)
		case "/1/boards/large123/cards":
			before := time.Time{}
			if value := r.URL.Query().Get("before"); value != "" {
				var err error
				before, err = time.Parse(time.RFC3339, value)
				if err != nil {
					t.Errorf("invalid cursor: %v", err)
				}
			}
			page := []map[string]string{}
			for _, card := range largeCards {
				seconds, _ := strconv.ParseInt(card["id"][:8], 16, 64)
				if !before.IsZero() && !time.Unix(seconds, 0).Before(before) {
					continue
				}
				page = append(page, card)
				if len(page) == 1000 {
					break
				}
			}
			_ = json.NewEncoder(w).Encode(page)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()
	fakeURL, _ := url.Parse(fake.URL)
	previous := http.DefaultTransport
	http.DefaultTransport = trelloTestTransport{base: fakeURL, transport: previous}
	defer func() { http.DefaultTransport = previous }()
	api := New(st, auth, nil, nil).Handler()
	request := func(token, boardID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/projects/import-test/imports/trello", strings.NewReader(fmt.Sprintf(`{"board_id":%q}`, boardID)))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		api.ServeHTTP(response, req)
		return response
	}
	if response := request(otherToken, "short123"); response.Code != http.StatusForbidden {
		t.Fatalf("nonmember received %d", response.Code)
	}
	_, err = admin.Exec(ctx, "INSERT INTO "+schema+".project_members(project_id,user_id) VALUES($1,$2)", projectID, otherID)
	if err != nil {
		t.Fatal(err)
	}
	if response := request(otherToken, "short123"); response.Code != http.StatusForbidden {
		t.Fatalf("non-authorized member received %d", response.Code)
	}
	first := request(ownerToken, "short123")
	if first.Code != http.StatusOK {
		t.Fatalf("first import: %d %s", first.Code, first.Body.String())
	}
	second := request(ownerToken, "short123")
	if second.Code != http.StatusOK {
		t.Fatalf("second import: %d %s", second.Code, second.Body.String())
	}
	var result struct {
		Data struct {
			TasksSkipped int `json:"tasks_skipped"`
		} `json:"data"`
	}
	if err := json.NewDecoder(bytes.NewReader(second.Body.Bytes())).Decode(&result); err != nil || result.Data.TasksSkipped != 1 {
		t.Fatalf("repeat result: %+v %v", result, err)
	}
	tasks, err := st.ListTasks(ctx, projectID, "")
	if err != nil || len(tasks) != 1 || tasks[0].Title != "Example" || tasks[0].EpicID == nil {
		t.Fatalf("tasks: %+v %v", tasks, err)
	}
	first = request(ownerToken, "large123")
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"tasks_created":1004`) {
		t.Fatalf("large import: %d %s", first.Code, first.Body.String())
	}
	second = request(ownerToken, "large123")
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"tasks_skipped":1004`) {
		t.Fatalf("large repeat: %d %s", second.Code, second.Body.String())
	}
	tasks, err = st.ListTasks(ctx, projectID, "")
	if err != nil || len(tasks) != 1005 {
		t.Fatalf("large import task count=%d error=%v", len(tasks), err)
	}
}
