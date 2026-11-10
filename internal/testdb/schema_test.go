package testdb

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestSchemaIsolationAndCleanup(t *testing.T) {
	var scoped [2]string
	t.Run("parallel schemas", func(t *testing.T) {
		for index := range scoped {
			t.Run(fmt.Sprintf("schema-%d", index), func(t *testing.T) {
				t.Parallel()
				dsn := Schema(t, "AKHFLOW_TEST_DATABASE_URL")
				scoped[index] = dsn
				ctx := context.Background()
				conn, err := pgx.Connect(ctx, dsn)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close(ctx)
				if _, err := conn.Exec(ctx, "CREATE TABLE schema_migrations (name TEXT PRIMARY KEY)"); err != nil {
					t.Fatal(err)
				}
				if _, err := conn.Exec(ctx, "INSERT INTO schema_migrations VALUES ('test')"); err != nil {
					t.Fatal(err)
				}
			})
		}
	})
	if scoped[0] == "" || scoped[1] == "" {
		t.Skip("integration database is not configured")
	}
	if scoped[0] == scoped[1] {
		t.Fatal("parallel tests received the same schema")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, scoped[0])
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, dsn := range scoped {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		schema := parsed.Query().Get("search_path")
		var exists bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)", schema).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Errorf("schema %s survived test cleanup", schema)
		}
	}
}
