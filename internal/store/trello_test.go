package store

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/trello"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Set AKHFLOW_TRELLO_TEST_DSN to run against a disposable schema in PostgreSQL.
func TestTrelloImportDatabase(t *testing.T) {
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
	schema := fmt.Sprintf("trello_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := &Store{db: pool}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	userID, err := store.CreateUser(ctx, "import@example.com", "Tester", "hash")
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := store.CreateProject(ctx, "import-test", "Import", userID)
	if err != nil {
		t.Fatal(err)
	}
	board := &trello.Board{ID: "board123", Lists: []trello.List{{ID: "list1", Name: "Queue"}}, Cards: []trello.Card{{ID: "card1", ListID: "list1", Name: "Original", Desc: "Details"}}}
	result, err := store.ImportTrello(ctx, projectID, userID, board)
	if err != nil || result.TasksCreated != 1 || result.ListsCreated != 1 {
		t.Fatalf("first import: %+v %v", result, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tasks SET title='Local edit'`); err != nil {
		t.Fatal(err)
	}
	board.Cards = append(board.Cards, trello.Card{ID: "card2", ListID: "list1", Name: "Concurrent"})
	var wg sync.WaitGroup
	results := make(chan TrelloImportResult, 2)
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := store.ImportTrello(ctx, projectID, userID, board)
			results <- result
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
		if result.TasksCreated+result.TasksSkipped != 2 || result.ListsCreated != 0 {
			t.Fatalf("repeat import: %+v", result)
		}
		created += result.TasksCreated
	}
	if created != 1 {
		t.Fatalf("expected one concurrent creation, got %d", created)
	}
	tasks, err := store.ListTasks(ctx, projectID, "")
	if err != nil || len(tasks) != 2 {
		t.Fatalf("unexpected tasks: %+v %v", tasks, err)
	}
	var foundLocalEdit bool
	for _, task := range tasks {
		if task.Title == "Local edit" && task.EpicID != nil {
			foundLocalEdit = true
		}
	}
	if !foundLocalEdit {
		t.Fatal("repeat import did not preserve local edit and list")
	}
	// The API can return open cards in archived lists, or a card moved to a list
	// created after the list request. Previously imported list mappings remain useful.
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/boards/board123":
			fmt.Fprint(w, `{"id":"board123"}`)
		case "/boards/board123/lists":
			fmt.Fprint(w, `[]`)
		case "/boards/board123/cards":
			// The first list is archived; a second list was created after /lists.
			fmt.Fprint(w, `[{"id":"card1","idList":"list1","name":"Original"},{"id":"card2","idList":"list1","name":"Concurrent"},{"id":"card3","idList":"missing","name":"Unknown list"},{"id":"card4","idList":"list1","name":"Known archived list"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()
	client := trello.New("example-key", "example-token")
	client.BaseURL = remote.URL
	board, err = client.FetchBoard(ctx, "board123")
	if err != nil {
		t.Fatal(err)
	}
	result, err = store.ImportTrello(ctx, projectID, userID, board)
	if err != nil || result.TasksCreated != 2 || result.TasksSkipped != 2 || result.TasksUngrouped != 1 {
		t.Fatalf("import cards outside list response: %+v %v", result, err)
	}
	tasks, err = store.ListTasks(ctx, projectID, "")
	if err != nil || len(tasks) != 4 {
		t.Fatalf("expected all open cards: %+v %v", tasks, err)
	}
	for _, task := range tasks {
		if task.Title == "Unknown list" && task.EpicID != nil {
			t.Fatal("unknown list must not refer to a nonexistent epic")
		}
		if task.Title == "Known archived list" && task.EpicID == nil {
			t.Fatal("existing list mapping should be retained")
		}
	}
	board.Lists = []trello.List{{ID: "list1", Name: "Queue"}, {ID: "missing", Name: "New list"}}
	result, err = store.ImportTrello(ctx, projectID, userID, board)
	if err != nil || result.TasksCreated != 0 || result.TasksSkipped != 4 || result.TasksUngrouped != 0 {
		t.Fatalf("repeat import must not duplicate or overwrite: %+v %v", result, err)
	}
	tasks, err = store.ListTasks(ctx, projectID, "")
	if err != nil || len(tasks) != 4 {
		t.Fatalf("repeat import changed task count: %+v %v", tasks, err)
	}
	for _, task := range tasks {
		if task.Title == "Unknown list" && task.EpicID != nil {
			t.Fatal("repeat import must not change an existing task")
		}
	}
	board.Cards = append(board.Cards, trello.Card{ID: "card5", Name: "Invalid"})
	if _, err := store.ImportTrello(ctx, projectID, userID, board); err == nil || !strings.Contains(err.Error(), "карточку") {
		t.Fatalf("expected validation error, got %v", err)
	}
}
