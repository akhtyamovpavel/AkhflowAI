package bitrix24

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchPagesAndMapping(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/rest/4/example/tasks.task.list.json" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		var payload struct {
			Start  int               `json:"start"`
			Filter map[string]int    `json:"filter"`
			Order  map[string]string `json:"order"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Start != -1 || payload.Filter[">ID"] != 0 || payload.Filter["GROUP_ID"] != 14 || payload.Order["ID"] != "asc" {
			t.Errorf("unexpected pagination: %+v", payload)
		}
		fmt.Fprint(w, `{"result":{"tasks":[{"id":"123","title":"First","description":"Text","status":"3","priority":"2","deadline":"2026-11-11T10:00:00+03:00"},{"id":124,"title":"Second","status":"5","priority":"1","deadline":null}]}}`)
	}))
	defer server.Close()
	client := Client{URL: server.URL + "/rest/4/example/", HTTP: server.Client()}
	portal, tasks, err := client.Fetch(context.Background(), 14)
	if err != nil {
		t.Fatal(err)
	}
	if portal == "" || requests != 1 || len(tasks) != 2 {
		t.Fatalf("portal=%q requests=%d tasks=%d", portal, requests, len(tasks))
	}
	if tasks[0].ID != "123" || tasks[0].Status != "in_progress" || tasks[0].Priority != "high" || tasks[0].DueDate == nil {
		t.Fatalf("bad first task: %+v", tasks[0])
	}
	if tasks[1].ID != "124" || tasks[1].Status != "done" || tasks[1].DueDate != nil {
		t.Fatalf("bad second task: %+v", tasks[1])
	}
}

func TestFetchConcurrentDeletionAndInsertion(t *testing.T) {
	ids := make([]int, 102)
	for i := range ids {
		ids[i] = i + 1
	}
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Start  int            `json:"start"`
			Filter map[string]int `json:"filter"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Start != -1 || payload.Filter["GROUP_ID"] != 14 {
			t.Errorf("bad query: %+v", payload)
		}
		page := make([]map[string]any, 0, 50)
		for _, id := range ids {
			if id > payload.Filter[">ID"] && len(page) < 50 {
				page = append(page, map[string]any{"id": id, "title": "Task"})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"tasks": page}})
		requests++
		if requests == 1 {
			ids = append(ids[1:], 103)
		}
	}))
	defer server.Close()
	_, tasks, err := (Client{URL: server.URL, HTTP: server.Client()}).Fetch(context.Background(), 14)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 || len(tasks) != 103 {
		t.Fatalf("requests=%d tasks=%d", requests, len(tasks))
	}
	for i, task := range tasks {
		if task.ID != fmt.Sprint(i+1) {
			t.Fatalf("task %d: %s", i, task.ID)
		}
	}
}

func TestRejectFailureAndCredentialsDoNotLeak(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":"forbidden"}`)
	}))
	defer server.Close()
	client := Client{URL: server.URL + "/rest/4/private-value/", HTTP: server.Client()}
	_, _, err := client.Fetch(context.Background(), 0)
	if err == nil || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("unexpected error: %v", err)
	}
	_, _, err = (Client{URL: "http://example.com/rest/1/example/"}).Fetch(context.Background(), 0)
	if err == nil {
		t.Fatal("insecure URL accepted")
	}
}

func TestRejectUnorderedOrInvalidIDs(t *testing.T) {
	for _, ids := range [][]string{{"2", "1"}, {"1", "1"}, {"0"}, {"invalid"}} {
		t.Run(strings.Join(ids, "_"), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tasks := make([]map[string]string, 0, len(ids))
				for _, id := range ids {
					tasks = append(tasks, map[string]string{"id": id, "title": "Task"})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"tasks": tasks}})
			}))
			defer server.Close()
			_, _, err := (Client{URL: server.URL, HTTP: server.Client()}).Fetch(context.Background(), 0)
			if err == nil {
				t.Fatal("invalid page accepted")
			}
		})
	}
}

func TestCursorPaginationBounds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		count     int
		wantError bool
	}{
		{"one-full-page", 50, false},
		{"maximum-pages", 10000, false},
		{"over-page-limit", 10001, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Filter map[string]int `json:"filter"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				requests++
				page := make([]map[string]any, 0, 50)
				for id := payload.Filter[">ID"] + 1; id <= tc.count && len(page) < 50; id++ {
					page = append(page, map[string]any{"id": id, "title": "Task"})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"tasks": page}})
			}))
			defer server.Close()
			_, tasks, err := (Client{URL: server.URL, HTTP: server.Client()}).Fetch(context.Background(), 0)
			if (err != nil) != tc.wantError {
				t.Fatalf("error: %v", err)
			}
			if !tc.wantError && len(tasks) != tc.count {
				t.Fatalf("got %d tasks, want %d", len(tasks), tc.count)
			}
			if requests > 201 {
				t.Fatalf("too many requests: %d", requests)
			}
		})
	}
}

func TestRejectCursorThatDoesNotAdvance(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		page := make([]map[string]any, 0, 50)
		for id := 1; id <= 50; id++ {
			page = append(page, map[string]any{"id": id, "title": "Task"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"tasks": page}})
	}))
	defer server.Close()
	_, _, err := (Client{URL: server.URL, HTTP: server.Client()}).Fetch(context.Background(), 0)
	if err == nil || requests != 2 {
		t.Fatalf("error=%v requests=%d", err, requests)
	}
}
