package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/llm"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/testdb"
	"github.com/jackc/pgx/v5"
)

func TestAgentInstructionsAccessConcurrencyAndSnapshot(t *testing.T) {
	dsn := testdb.Schema(t, "AKHFLOW_TEST_DATABASE_URL")
	ctx := context.Background()
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	create := func(email string) int64 {
		id, err := db.CreateUser(ctx, email, email, "hash")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	owner := create("owner-instructions@example.invalid")
	admin := create("admin-instructions@example.invalid")
	viewer := create("viewer-instructions@example.invalid")
	editor := create("editor-instructions@example.invalid")
	outsider := create("outsider-instructions@example.invalid")
	project, err := db.CreateProject(ctx, "instructions", "Instructions", owner)
	if err != nil {
		t.Fatal(err)
	}
	for id, role := range map[int64]string{viewer: "viewer", editor: "editor"} {
		ok, err := db.SetProjectMemberRole(ctx, project, owner, id, role)
		if err != nil || !ok {
			t.Fatalf("role: %v", err)
		}
	}
	connection, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(ctx)
	_, err = connection.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES($1,$2,'admin')`, project, admin)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(ctx, project, owner)
	if err != nil {
		t.Fatal(err)
	}
	auth := &Auth{Store: db, Secret: []byte("test-secret"), TokenTTL: time.Hour}
	token := make(map[int64]string)
	for _, id := range []int64{owner, admin, viewer, editor, outsider} {
		token[id], err = auth.CreateToken(id)
		if err != nil {
			t.Fatal(err)
		}
	}
	handler := New(db, auth, nil, nil).Handler()
	path := "/api/projects/instructions/agent/instructions"
	request := func(id int64, method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token[id])
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	for _, id := range []int64{owner, admin, viewer, editor} {
		if got := request(id, "GET", "").Code; got != 200 {
			t.Fatalf("member read: %d", got)
		}
	}
	if got := request(outsider, "GET", "").Code; got != 403 {
		t.Fatalf("outsider read: %d", got)
	}
	for _, id := range []int64{viewer, editor, outsider} {
		if got := request(id, "PUT", `{"instructions":"x","version":0}`).Code; got != 403 {
			t.Fatalf("non-admin write: %d", got)
		}
	}
	invalid := []string{`{}`, `{"instructions":null,"version":0}`, `{"instructions":3,"version":0}`, `{"instructions":"x"}`, `{"instructions":"x","version":null}`, `{"instructions":"x","version":1.5}`, `{"instructions":"x","version":-1}`, `{"instructions":"x","version":0} {}`}
	for _, body := range invalid {
		if got := request(owner, "PUT", body).Code; got != 400 {
			t.Fatalf("invalid %s: %d", body, got)
		}
	}
	large, _ := json.Marshal(map[string]any{"instructions": strings.Repeat("я", 8193), "version": 0})
	if got := request(owner, "PUT", string(large)).Code; got != 400 {
		t.Fatalf("large: %d", got)
	}
	maximal, _ := json.Marshal(map[string]any{"instructions": strings.Repeat("я", 8192), "version": 0})
	if got := request(admin, "PUT", string(maximal)).Code; got != 200 {
		t.Fatalf("admin and boundary: %d", got)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, text := range []string{"first", "second"} {
		wg.Add(1)
		go func(text string) {
			defer wg.Done()
			body := fmt.Sprintf(`{"instructions":%q,"version":1}`, text)
			codes <- request(owner, "PUT", body).Code
		}(text)
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent results: %v", counts)
	}
	current, err := db.AgentInstructions(ctx, project)
	if err != nil || current.Version != 2 {
		t.Fatalf("current: %+v %v", current, err)
	}
	run, err := db.EnqueueRun(ctx, chat, owner, "00000000-0000-4000-8000-000000000001", "hello", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if run.InstructionsSnapshot != current.Instructions || run.InstructionsVersion != 2 {
		t.Fatalf("snapshot: %+v", run)
	}
	if got := request(owner, "PUT", `{"instructions":"new instructions","version":2}`).Code; got != 200 {
		t.Fatalf("update: %d", got)
	}
	replay, err := db.EnqueueRun(ctx, chat, owner, "00000000-0000-4000-8000-000000000001", "hello", "hello")
	if err != nil || replay.InstructionsSnapshot != current.Instructions || replay.InstructionsVersion != 2 {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	observed := make(chan string, 1)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		observed <- input.Messages[0].Content
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	}))
	defer model.Close()
	worker := New(db, auth, llm.NewClient(model.URL, "fixture"), nil)
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); worker.RunAgentWorker(workerCtx) }()
	defer func() { stop(); <-done }()
	select {
	case prompt := <-observed:
		if !strings.Contains(prompt, current.Instructions) || strings.Contains(prompt, "new instructions") {
			t.Fatalf("worker did not use snapshot")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not run")
	}
}
