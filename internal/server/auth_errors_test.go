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

	"akhflow.ai/akhflow/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuthenticationDatabaseUnavailable(t *testing.T) {
	db, err := store.New("postgres://test:test@127.0.0.1:1/test")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	srv := &Server{Store: db, Auth: &Auth{}}

	for _, path := range []string{"/api/auth/register", "/api/auth/login"} {
		t.Run(path, func(t *testing.T) {
			body := `{"email":"test@example.invalid","password":"password"}`
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			response := httptest.NewRecorder()
			srv.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("got status %d, want 503", response.Code)
			}
			want := `{"error":"authentication temporarily unavailable"}` + "\n"
			if response.Body.String() != want {
				t.Fatalf("unexpected response: %q", response.Body.String())
			}
		})
	}
}

func TestAuthenticationRegistrationAndLogin(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AKHFLOW_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("auth_test_%d", time.Now().UnixNano())
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
	auth := &Auth{Store: db, Secret: []byte("test-auth-key"), TokenTTL: time.Hour}
	srv := &Server{Store: db, Auth: auth}
	body := `{"email":"test@example.invalid","password":"password","display_name":"Test"}`
	function := "CREATE FUNCTION " + schema + ".reject_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private-db-detail'; END $$"
	if _, err := admin.Exec(ctx, function); err != nil {
		t.Fatal(err)
	}
	trigger := "CREATE TRIGGER reject_insert BEFORE INSERT ON " + schema + ".users FOR EACH ROW EXECUTE FUNCTION " + schema + ".reject_insert()"
	if _, err := admin.Exec(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(body))
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "private-db-detail") {
		t.Fatalf("insert error exposed or misclassified: status %d, body %q", response.Code, response.Body.String())
	}
	if _, err := admin.Exec(ctx, "DROP TRIGGER reject_insert ON "+schema+".users"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/auth/register", http.StatusCreated},
		{"/api/auth/register", http.StatusConflict},
		{"/api/auth/login", http.StatusOK},
	} {
		request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body))
		response := httptest.NewRecorder()
		srv.Handler().ServeHTTP(response, request)
		if response.Code != tc.want {
			t.Fatalf("%s: got status %d, want %d", tc.path, response.Code, tc.want)
		}
		if tc.want == http.StatusCreated || tc.want == http.StatusOK {
			var result struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Token == "" {
				t.Fatalf("%s: missing token, error: %v", tc.path, err)
			}
			if _, err := auth.ParseToken(result.Token); err != nil {
				t.Fatalf("%s: invalid token: %v", tc.path, err)
			}
		}
	}
}
