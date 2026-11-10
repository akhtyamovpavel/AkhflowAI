package store

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/domain"
)

// Set this only to an isolated test database; migrations are applied to it.
func TestBitrixImportConcurrentAndProjectIsolation(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated test database not configured")
	}
	ctx := context.Background()
	st, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	userID, err := st.CreateUser(ctx, fmt.Sprintf("test-%d@example.org", suffix), "Test", "not-used")
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := st.CreateProject(ctx, fmt.Sprintf("test-%d", suffix), "Test", userID)
	if err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ProjectID: projectID, AuthorID: userID, Title: "Imported task", Status: domain.StatusTodo, Priority: domain.PriorityMedium}
	var wg sync.WaitGroup
	results := make(chan bool, 8)
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, err := st.ImportBitrixTask(ctx, "example.bitrix24.test", "42", task)
			results <- created
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	created := 0
	for result := range results {
		if result {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created %d tasks, want 1", created)
	}
	tasks, err := st.ListTasks(ctx, projectID, "")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%v err=%v", tasks, err)
	}
	otherID, err := st.CreateProject(ctx, fmt.Sprintf("other-%d", suffix), "Other", userID)
	if err != nil {
		t.Fatal(err)
	}
	task.ProjectID = otherID
	otherCreated, err := st.ImportBitrixTask(ctx, "example.bitrix24.test", "42", task)
	if err != nil || !otherCreated {
		t.Fatalf("other project created=%v err=%v", otherCreated, err)
	}
}
