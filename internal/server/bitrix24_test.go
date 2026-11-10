package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/store"
)

func TestBitrixImportHTTP(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated test database not configured")
	}
	ctx := context.Background()
	st, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	userID, err := st.CreateUser(ctx, fmt.Sprintf("http-%d@example.org", suffix), "Test", "unused")
	if err != nil {
		t.Fatal(err)
	}
	slug := fmt.Sprintf("http-%d", suffix)
	projectID, err := st.CreateProject(ctx, slug, "Test", userID)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := st.CreateUser(ctx, fmt.Sprintf("other-%d@example.org", suffix), "Other", "unused")
	if err != nil {
		t.Fatal(err)
	}
	otherSlug := fmt.Sprintf("other-%d", suffix)
	_, err = st.CreateProject(ctx, otherSlug, "Other", otherID)
	if err != nil {
		t.Fatal(err)
	}
	fake := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"result":{"tasks":[{"id":"18","title":"Source task","status":"3","priority":"2","deadline":null}]}}`)
	}))
	defer fake.Close()
	transport := http.DefaultTransport
	http.DefaultTransport = fake.Client().Transport
	defer func() { http.DefaultTransport = transport }()
	t.Setenv("BITRIX24_WEBHOOK_URL", fake.URL+"/rest/1/example/")
	t.Setenv("BITRIX24_ALLOWED_USER_ID", fmt.Sprint(userID))
	auth := &Auth{Store: st, Secret: []byte("test-signing-key"), TokenTTL: time.Hour}
	server := New(st, auth, nil, nil)
	request := func(id int64, slug string) *httptest.ResponseRecorder {
		token, err := auth.CreateToken(id)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+slug+"/imports/bitrix24", strings.NewReader(`{"group_id":1}`))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		return response
	}
	first := request(userID, slug)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"created":1`) {
		t.Fatalf("first import: %d %s", first.Code, first.Body.String())
	}
	second := request(userID, slug)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"skipped":1`) {
		t.Fatalf("repeat import: %d %s", second.Code, second.Body.String())
	}
	unauthorized := request(otherID, slug)
	if unauthorized.Code == http.StatusOK {
		t.Fatal("nonmember imported tasks")
	}
	unauthorizedOwner := request(otherID, otherSlug)
	if unauthorizedOwner.Code == http.StatusOK {
		t.Fatal("unauthorized user imported into their own project")
	}
	tasks, err := st.ListTasks(ctx, projectID, "")
	if err != nil || len(tasks) != 1 || tasks[0].Title != "Source task" || tasks[0].Status != "in_progress" {
		t.Fatalf("tasks=%v error=%v", tasks, err)
	}
}
