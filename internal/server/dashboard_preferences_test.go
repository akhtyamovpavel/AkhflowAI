package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDashboardPreferences(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_ROLES_TEST_DSN")
	if dsn == "" {
		t.Skip("AKHFLOW_ROLES_TEST_DSN is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("dashboard_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
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
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var migrations int
	if err = admin.QueryRow(ctx, "SELECT count(*) FROM "+schema+".schema_migrations WHERE name IN ('010_attachment_content.sql', '015_dashboard_preferences.sql')").Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	if migrations != 2 {
		t.Fatalf("attachment and dashboard migrations: got %d, want 2", migrations)
	}
	createUser := func(email string) int64 {
		t.Helper()
		id, err := db.CreateUser(ctx, email, email, "hash")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	owner := createUser("owner@example.invalid")
	viewer := createUser("viewer@example.invalid")
	outsider := createUser("outsider@example.invalid")
	alpha, err := db.CreateProject(ctx, "alpha", "Alpha", owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateProject(ctx, "beta", "Beta", owner); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.SetProjectMemberRole(ctx, alpha, owner, viewer, "viewer"); err != nil || !ok {
		t.Fatalf("viewer: %v", err)
	}
	auth := &Auth{Store: db, Secret: []byte("test-only-signing-secret"), TokenTTL: time.Hour}
	handler := New(db, auth, nil, nil).Handler()
	request := func(userID int64, method, slug, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/projects/"+slug+"/dashboard-preferences", strings.NewReader(body))
		if userID != 0 {
			token, err := auth.CreateToken(userID)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	check := func(userID int64, slug string, want []string) {
		t.Helper()
		response := request(userID, http.MethodGet, slug, "")
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", slug, response.Code)
		}
		var result struct {
			HiddenWidgets []string `json:"hidden_widgets"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(result.HiddenWidgets, want) {
			t.Fatalf("GET %s: got %v want %v", slug, result.HiddenWidgets, want)
		}
	}
	check(owner, "alpha", []string{})
	if got := request(owner, http.MethodPut, "alpha", `{"hidden_widgets":["statuses","epics"]}`).Code; got != 200 {
		t.Fatalf("owner save: %d", got)
	}
	check(owner, "alpha", []string{"statuses", "epics"})
	check(owner, "beta", []string{})
	check(viewer, "alpha", []string{})
	if got := request(viewer, http.MethodPut, "alpha", `{"hidden_widgets":["priorities"]}`).Code; got != 200 {
		t.Fatalf("viewer save: %d", got)
	}
	check(viewer, "alpha", []string{"priorities"})
	check(owner, "alpha", []string{"statuses", "epics"})
	for _, body := range []string{`{}`, `{"hidden_widgets":null}`, `{"hidden_widgets":["unknown"]}`, `{"hidden_widgets":["epics","epics"]}`, `{"hidden_widgets":"epics"}`} {
		if got := request(owner, http.MethodPut, "alpha", body).Code; got != 400 {
			t.Fatalf("invalid %s: %d", body, got)
		}
	}
	if got := request(outsider, http.MethodGet, "alpha", "").Code; got != 403 {
		t.Fatalf("outsider read: %d", got)
	}
	if got := request(outsider, http.MethodPut, "alpha", `{"hidden_widgets":[]}`).Code; got != 403 {
		t.Fatalf("outsider write: %d", got)
	}
	if got := request(0, http.MethodGet, "alpha", "").Code; got != 401 {
		t.Fatalf("anonymous read: %d", got)
	}
	if got := request(viewer, http.MethodPut, "alpha", `{"hidden_widgets":[]}`).Code; got != 200 {
		t.Fatalf("viewer reset: %d", got)
	}
	check(viewer, "alpha", []string{})
}
