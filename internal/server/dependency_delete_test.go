package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestDependencyDeleteProcesses(t *testing.T) {
	dsn := testdb.Schema(t, "AKHFLOW_ISOLATION_TEST_DSN")
	ctx := context.Background()
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateUser(ctx, "delete-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "delete-test", "Delete", owner)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `CREATE FUNCTION gate_dependency_delete() RETURNS trigger AS $$ BEGIN PERFORM pg_advisory_xact_lock(784001); RETURN OLD; END $$ LANGUAGE plpgsql;
      CREATE TRIGGER gate_dependency_delete BEFORE DELETE ON tasks FOR EACH ROW EXECUTE FUNCTION gate_dependency_delete();
      CREATE FUNCTION gate_dependency_add() RETURNS trigger AS $$ BEGIN PERFORM pg_advisory_xact_lock(784002); RETURN NEW; END $$ LANGUAGE plpgsql;
      CREATE TRIGGER gate_dependency_add BEFORE INSERT ON task_dependencies FOR EACH ROW EXECUTE FUNCTION gate_dependency_add()`)
	if err != nil {
		t.Fatal(err)
	}
	create := func(title string) int64 {
		t.Helper()
		id, err := db.CreateTask(ctx, domain.Task{ProjectID: project, AuthorID: owner, Title: title, Status: "todo", Priority: "medium"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	for _, tc := range []struct {
		first, addAPI, deleteAPI string
		key                      int64
	}{
		{"delete", "mcp", "rest", 784001}, {"delete", "rest", "mcp", 784001},
		{"add", "mcp", "rest", 784002}, {"add", "rest", "mcp", 784002},
	} {
		t.Run(tc.first+"-"+tc.addAPI+"-"+tc.deleteAPI, func(t *testing.T) {
			source, target := create("source"), create("target")
			if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, tc.key); err != nil {
				t.Fatal(err)
			}
			defer conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, tc.key)
			type worker struct {
				cmd *exec.Cmd
				in  interface {
					Write([]byte) (int, error)
					Close() error
				}
				out         *bytes.Buffer
				application string
				done        <-chan error
			}
			start := func(action, api string) worker {
				command := exec.Command(os.Args[0], "-test.run=^TestDependencyDeleteWorker$")
				application := fmt.Sprintf("dependency-delete-%s-%d", action, target)
				workerURL, err := url.Parse(dsn)
				if err != nil {
					t.Fatal(err)
				}
				query := workerURL.Query()
				query.Set("application_name", application)
				workerURL.RawQuery = query.Encode()
				command.Env = append(os.Environ(), "DELETE_WORKER_DSN="+workerURL.String(), "DELETE_WORKER_ACTION="+action, "DELETE_WORKER_API="+api, fmt.Sprintf("DELETE_WORKER_OWNER=%d", owner), fmt.Sprintf("DELETE_WORKER_SOURCE=%d", source), fmt.Sprintf("DELETE_WORKER_TARGET=%d", target))
				input, err := command.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				out := &bytes.Buffer{}
				command.Stdout = out
				command.Stderr = out
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- command.Wait() }()
				t.Cleanup(func() { _ = command.Process.Kill() })
				return worker{command, input, out, application, done}
			}
			add, del := start("add", tc.addAPI), start("delete", tc.deleteAPI)
			signal := func(w worker) {
				t.Helper()
				if _, err := w.in.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
				w.in.Close()
			}
			first, second := del, add
			if tc.first == "add" {
				first, second = add, del
			}
			signal(first)
			deadline := time.Now().Add(10 * time.Second)
			for {
				var waiting int
				err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name=$1 AND wait_event='advisory'`, first.application).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("worker did not reach the database gate")
				}
				time.Sleep(10 * time.Millisecond)
			}
			signal(second)
			// Keep the first operation inside its trigger until the other process
			// actually waits for a database lock or has completed. With the old
			// implementation it can complete while the first is still paused.
			deadline = time.Now().Add(10 * time.Second)
			secondFinished := false
			var secondErr error
		waitSecond:
			for {
				select {
				case secondErr = <-second.done:
					secondFinished = true
					break waitSecond
				default:
				}
				var waiting int
				if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity waiting
                    WHERE waiting.datname=current_database() AND waiting.application_name=$1 AND waiting.wait_event_type='Lock'
                    AND EXISTS (SELECT 1 FROM pg_stat_activity blocker WHERE blocker.application_name=$2 AND blocker.pid=ANY(pg_blocking_pids(waiting.pid)))`, second.application, first.application).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("second worker did not wait or finish")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, tc.key); err != nil {
				t.Fatal(err)
			}
			if err := <-first.done; err != nil {
				t.Fatalf("first worker: %v: %s", err, first.out)
			}
			if !secondFinished {
				secondErr = <-second.done
			}
			if secondErr != nil {
				t.Fatalf("second worker: %v: %s", secondErr, second.out)
			}
			var wantAdd, wantDelete string
			if tc.first == "delete" {
				wantAdd, wantDelete = "RESULT missing", "RESULT success"
			} else {
				wantAdd, wantDelete = "RESULT success", "RESULT blocked"
			}
			if !strings.Contains(add.out.String(), wantAdd) || !strings.Contains(del.out.String(), wantDelete) {
				t.Fatalf("add=%s delete=%s", add.out, del.out)
			}
			task, err := db.TaskByID(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			deps, err := db.DependenciesDetail(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			if tc.first == "delete" && (task != nil || len(deps) != 0) {
				t.Fatalf("delete won but target/edge remain: %v %v", task, deps)
			}
			if tc.first == "add" && (task == nil || len(deps) != 1 || deps[0].ID != target) {
				t.Fatalf("add won but target/edge lost: %v %v", task, deps)
			}
		})
	}
}

func TestDependencyDeleteWorker(t *testing.T) {
	dsn := os.Getenv("DELETE_WORKER_DSN")
	if dsn == "" {
		return
	}
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := strconv.ParseInt(os.Getenv("DELETE_WORKER_OWNER"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	source, target := os.Getenv("DELETE_WORKER_SOURCE"), os.Getenv("DELETE_WORKER_TARGET")
	action, api := os.Getenv("DELETE_WORKER_ACTION"), os.Getenv("DELETE_WORKER_API")
	var signal [1]byte
	if _, err := os.Stdin.Read(signal[:]); err != nil {
		t.Fatal(err)
	}
	if api == "rest" {
		auth := &Auth{Store: db, Secret: []byte("test-signing-key"), TokenTTL: time.Hour}
		token, err := auth.CreateToken(owner)
		if err != nil {
			t.Fatal(err)
		}
		method, path, body := http.MethodDelete, "/api/tasks/"+target, ""
		if action == "add" {
			method, path, body = http.MethodPost, "/api/tasks/"+source+"/dependencies", `{"depends_on":`+target+`}`
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		New(db, auth, nil, nil).Handler().ServeHTTP(response, req)
		switch response.Code {
		case http.StatusOK, http.StatusCreated:
			fmt.Println("RESULT success")
		case http.StatusConflict:
			if !strings.Contains(response.Body.String(), "source") {
				t.Fatal(response.Body.String())
			}
			fmt.Println("RESULT blocked")
		case http.StatusNotFound:
			fmt.Println("RESULT missing")
		default:
			t.Fatalf("REST %d: %s", response.Code, response.Body.String())
		}
		return
	}
	name, args := "delete_task", `{"task_id":`+target+`}`
	if action == "add" {
		name, args = "add_dependency", `{"task_id":`+source+`,"depends_on":`+target+`}`
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer fixture-key")
	response := httptest.NewRecorder()
	(&mcp.Server{Store: db, UserID: owner, Actor: "owner@example.invalid", APIKey: "fixture-key"}).HTTPHandler().ServeHTTP(response, req)
	var result struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK {
		t.Fatalf("MCP %d: %s", response.Code, response.Body.String())
	}
	if !result.Result.IsError {
		fmt.Println("RESULT success")
	} else if strings.Contains(response.Body.String(), "blocks other tasks") && strings.Contains(response.Body.String(), "source") {
		fmt.Println("RESULT blocked")
	} else if strings.Contains(response.Body.String(), "not found") {
		fmt.Println("RESULT missing")
	} else {
		t.Fatalf("MCP error: %s", response.Body.String())
	}
}
