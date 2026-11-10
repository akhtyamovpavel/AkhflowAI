package store

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/akhcheck"
)

func TestAkhcheckImportDatabase(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AKHFLOW_TEST_DATABASE_URL must point to a disposable database")
	}
	ctx := context.Background()
	db, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var userID, projectID, otherProjectID int64
	suffix := time.Now().UnixNano()
	if err := db.db.QueryRow(ctx, `INSERT INTO users(email,password_hash,display_name) VALUES($1,'unused','Test') RETURNING id`, fmt.Sprintf("store-%d@example.invalid", suffix)).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(ctx, `INSERT INTO projects(slug,name,owner_id) VALUES($1,'Test',$2) RETURNING id`, fmt.Sprintf("store-%d", suffix), userID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(ctx, `INSERT INTO projects(slug,name,owner_id) VALUES($1,'Other',$2) RETURNING id`, fmt.Sprintf("store-other-%d", suffix), userID).Scan(&otherProjectID); err != nil {
		t.Fatal(err)
	}
	course := akhcheck.Course{ID: 11, Tasks: []akhcheck.Assignment{{ID: 1, Name: "First"}, {ID: 2, Name: "Second"}}}
	results := make([]AkhcheckImportResult, 2)
	errors := make([]error, 2)
	var group sync.WaitGroup
	for index := range results {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			results[index], errors[index] = db.ImportAkhcheck(ctx, projectID, userID, course)
		}(index)
	}
	group.Wait()
	for _, err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if results[0].Created+results[1].Created != 2 || results[0].Skipped+results[1].Skipped != 2 {
		t.Fatalf("unexpected concurrent results: %+v", results)
	}
	result, err := db.ImportAkhcheck(ctx, otherProjectID, userID, course)
	if err != nil || result.Created != 2 {
		t.Fatalf("project isolation: %+v %v", result, err)
	}
	var count int
	if err := db.db.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE project_id=$1`, projectID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("created %d tasks, want 2", count)
	}
}
