package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/testdb"
	"github.com/jackc/pgx/v5"
)

func TestConcurrentRegistration(t *testing.T) {
	dsn := testdb.Schema(t, "AKHFLOW_TEST_DATABASE_URL")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("application_name", "registration-"+query.Get("search_path"))
	parsed.RawQuery = query.Encode()
	dsn = parsed.String()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	_, err = conn.Exec(ctx, `CREATE FUNCTION gate_registration() RETURNS trigger AS $$
		BEGIN PERFORM pg_advisory_xact_lock(789001); RETURN NEW; END $$ LANGUAGE plpgsql;
		CREATE TRIGGER gate_registration BEFORE INSERT ON users FOR EACH ROW EXECUTE FUNCTION gate_registration()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(789001)`); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(789001)`)

	auth := &Auth{Store: db, Secret: []byte("test-secret"), TokenTTL: time.Hour}
	handler := New(db, auth, nil, nil).Handler()
	responses := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func() {
			body := `{"email":"race@example.invalid","password":"test-password","display_name":"Test"}`
			req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(body))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			responses <- recorder
		}()
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		var waiting int
		err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname=current_database() AND application_name=$1 AND wait_event='advisory'`, query.Get("application_name")).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("both registration requests did not reach the insert gate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock(789001)`); err != nil {
		t.Fatal(err)
	}

	statuses := map[int]int{}
	for i := 0; i < 2; i++ {
		select {
		case response := <-responses:
			statuses[response.Code]++
			if response.Code == http.StatusConflict && strings.TrimSpace(response.Body.String()) != `{"error":"user exists"}` {
				t.Fatalf("unexpected conflict response: %s", response.Body.String())
			}
		case <-ctx.Done():
			t.Fatal("registration requests did not finish")
		}
	}
	if statuses[http.StatusCreated] != 1 || statuses[http.StatusConflict] != 1 {
		t.Fatalf("expected one created and one conflict, got %v", statuses)
	}
	var count int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM users WHERE email=$1`, "race@example.invalid").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one user, got %d", count)
	}
}
