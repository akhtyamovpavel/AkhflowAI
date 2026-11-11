package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/domain"
	"akhflow.ai/akhflow/internal/mcp"
	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/testdb"
	"github.com/jackc/pgx/v5"
)

func TestDependencyCycles(t *testing.T) {
	dsn := testdb.Schema(t, "AKHFLOW_ISOLATION_TEST_DSN")
	ctx := context.Background()
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateUser(ctx, "cycle-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "cycle-test", "Cycle", owner)
	if err != nil {
		t.Fatal(err)
	}
	otherProject, err := db.CreateProject(ctx, "cycle-other", "Other", owner)
	if err != nil {
		t.Fatal(err)
	}
	create := func(projectID int64) int64 {
		t.Helper()
		id, err := db.CreateTask(ctx, domain.Task{ProjectID: projectID, AuthorID: owner, Title: "Task", Status: "todo", Priority: "medium"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b, c, d := create(project), create(project), create(project), create(project)
	other := create(otherProject)
	for _, edge := range [][2]int64{{a, b}, {b, c}, {a, c}, {c, d}, {a, b}} {
		if err := db.AddDependency(ctx, edge[0], edge[1]); err != nil {
			t.Fatalf("valid edge %v: %v", edge, err)
		}
	}
	for _, edge := range [][2]int64{{b, a}, {d, a}, {a, a}} {
		if err := db.AddDependency(ctx, edge[0], edge[1]); !errors.Is(err, store.ErrDependencyCycle) {
			t.Errorf("cycle %v: %v", edge, err)
		}
	}
	if err := db.AddDependency(ctx, a, other); !errors.Is(err, store.ErrDependencyDifferentProject) {
		t.Errorf("cross-project dependency: %v", err)
	}
	if err := db.AddDependency(ctx, a, 999999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing task: %v", err)
	}
	viewer, err := db.CreateUser(ctx, "cycle-viewer@example.invalid", "Viewer", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.SetProjectMemberRole(ctx, project, owner, viewer, "viewer"); err != nil || !ok {
		t.Fatalf("set viewer role: %v", err)
	}
	auth := &Auth{Store: db, Secret: []byte("test-signing-key"), TokenTTL: time.Hour}
	token, err := auth.CreateToken(viewer)
	if err != nil {
		t.Fatal(err)
	}
	restRequest := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/tasks/%d/dependencies", d), strings.NewReader(fmt.Sprintf(`{"depends_on":%d}`, b)))
	restRequest.Header.Set("Authorization", "Bearer "+token)
	restResponse := httptest.NewRecorder()
	New(db, auth, nil, nil).Handler().ServeHTTP(restResponse, restRequest)
	if restResponse.Code != http.StatusForbidden {
		t.Errorf("viewer REST status = %d", restResponse.Code)
	}
	rpc := &mcp.Server{Store: db, UserID: viewer, Actor: "viewer@example.invalid", APIKey: "fixture-key"}
	rpcBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"add_dependency","arguments":{"task_id":%d,"depends_on":%d}}}`, d, b)
	rpcRequest := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(rpcBody))
	rpcRequest.Header.Set("Authorization", "Bearer fixture-key")
	rpcResponse := httptest.NewRecorder()
	rpc.HTTPHandler().ServeHTTP(rpcResponse, rpcRequest)
	if !strings.Contains(rpcResponse.Body.String(), `"isError":true`) {
		t.Errorf("viewer MCP response: %s", rpcResponse.Body.String())
	}
	// Widen the interval between a cycle check and insertion so the test catches
	// implementations that check and insert without a shared database lock.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `CREATE FUNCTION delay_dependency_insert() RETURNS trigger AS $$
		BEGIN PERFORM pg_sleep(0.1); RETURN NEW; END;
		$$ LANGUAGE plpgsql`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `CREATE TRIGGER delay_dependency_insert BEFORE INSERT ON task_dependencies FOR EACH ROW EXECUTE FUNCTION delay_dependency_insert()`)
	if err != nil {
		t.Fatal(err)
	}
	// Separate processes use REST and MCP handlers against the same database.
	for iteration := 0; iteration < 8; iteration++ {
		left, right := create(project), create(project)
		nodes := []int64{left, right}
		type edge struct {
			kind           string
			source, target int64
		}
		edges := []edge{{"rest", left, right}, {"mcp", right, left}}
		if iteration%2 == 1 {
			third := create(project)
			nodes = append(nodes, third)
			edges = []edge{{"rest", left, right}, {"mcp", right, third}, {"rest", third, left}}
		}
		type worker struct {
			command *exec.Cmd
			input   interface {
				Write([]byte) (int, error)
				Close() error
			}
			output *bytes.Buffer
		}
		workers := make([]worker, 0, len(edges))
		for _, item := range edges {
			command := exec.Command(os.Args[0], "-test.run=^TestDependencyProcessWorker$")
			command.Env = append(os.Environ(), "DEP_WORKER_DSN="+dsn, "DEP_WORKER_KIND="+item.kind, fmt.Sprintf("DEP_WORKER_OWNER=%d", owner), fmt.Sprintf("DEP_WORKER_SOURCE=%d", item.source), fmt.Sprintf("DEP_WORKER_TARGET=%d", item.target))
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output := &bytes.Buffer{}
			command.Stdout = output
			command.Stderr = output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			workers = append(workers, worker{command: command, input: input, output: output})
		}
		for _, item := range workers {
			if _, err := item.input.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			item.input.Close()
		}
		successes := 0
		for _, item := range workers {
			if err := item.command.Wait(); err != nil {
				t.Fatalf("worker failed: %v %s", err, item.output.String())
			}
			if strings.Contains(item.output.String(), "RESULT success") {
				successes++
			} else if !strings.Contains(item.output.String(), "RESULT cycle") {
				t.Fatalf("unexpected worker output: %s", item.output.String())
			}
		}
		if successes != len(edges)-1 {
			t.Fatalf("round %d: expected %d successes, got %d", iteration, len(edges)-1, successes)
		}
		count := 0
		for _, node := range nodes {
			dependencies, err := db.DependenciesDetail(ctx, node)
			if err != nil {
				t.Fatal(err)
			}
			count += len(dependencies)
		}
		if count != len(edges)-1 {
			t.Fatalf("round %d: stored %d edges, expected %d", iteration, count, len(edges)-1)
		}
	}
}

func TestDependencyProcessWorker(t *testing.T) {
	dsn := os.Getenv("DEP_WORKER_DSN")
	if dsn == "" {
		return
	}
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := strconv.ParseInt(os.Getenv("DEP_WORKER_OWNER"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	source := os.Getenv("DEP_WORKER_SOURCE")
	target := os.Getenv("DEP_WORKER_TARGET")
	var signal [1]byte
	if _, err := os.Stdin.Read(signal[:]); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DEP_WORKER_KIND") == "rest" {
		auth := &Auth{Store: db, Secret: []byte("test-signing-key"), TokenTTL: time.Hour}
		token, err := auth.CreateToken(owner)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/tasks/"+source+"/dependencies", strings.NewReader(`{"depends_on":`+target+`}`))
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		New(db, auth, nil, nil).Handler().ServeHTTP(response, request)
		switch response.Code {
		case http.StatusCreated:
			fmt.Println("RESULT success")
		case http.StatusConflict:
			fmt.Println("RESULT cycle")
		default:
			t.Fatalf("unexpected REST status %d: %s", response.Code, response.Body.String())
		}
		return
	}
	rpc := &mcp.Server{Store: db, UserID: owner, Actor: "owner@example.invalid", APIKey: "fixture-key"}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"add_dependency","arguments":{"task_id":` + source + `,"depends_on":` + target + `}}}`
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer fixture-key")
	response := httptest.NewRecorder()
	rpc.HTTPHandler().ServeHTTP(response, request)
	var result struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK {
		t.Fatalf("invalid MCP response: %d %s", response.Code, response.Body.String())
	}
	if result.Result.IsError {
		if !strings.Contains(response.Body.String(), "dependency would create a cycle") {
			t.Fatalf("unexpected MCP error: %s", response.Body.String())
		}
		fmt.Println("RESULT cycle")
	} else {
		fmt.Println("RESULT success")
	}
}
