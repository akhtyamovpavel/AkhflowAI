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
			Start  int            `json:"start"`
			Filter map[string]int `json:"filter"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Filter["GROUP_ID"] != 14 {
			t.Errorf("group filter: %v", payload.Filter)
		}
		if payload.Start == 0 {
			fmt.Fprint(w, `{"result":{"tasks":[{"id":"123","title":"First","description":"Text","status":"3","priority":"2","deadline":"2026-11-11T10:00:00+03:00"}]},"next":50}`)
			return
		}
		if payload.Start != 50 {
			t.Errorf("bad pagination %d", payload.Start)
		}
		fmt.Fprint(w, `{"result":{"tasks":[{"id":124,"title":"Second","status":"5","priority":"1","deadline":null}]}}`)
	}))
	defer server.Close()
	client := Client{URL: server.URL + "/rest/4/example/", HTTP: server.Client()}
	portal, tasks, err := client.Fetch(context.Background(), 14)
	if err != nil {
		t.Fatal(err)
	}
	if portal == "" || requests != 2 || len(tasks) != 2 {
		t.Fatalf("portal=%q requests=%d tasks=%d", portal, requests, len(tasks))
	}
	if tasks[0].ID != "123" || tasks[0].Status != "in_progress" || tasks[0].Priority != "high" || tasks[0].DueDate == nil {
		t.Fatalf("bad first task: %+v", tasks[0])
	}
	if tasks[1].ID != "124" || tasks[1].Status != "done" || tasks[1].DueDate != nil {
		t.Fatalf("bad second task: %+v", tasks[1])
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

func TestRejectRepeatedNext(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"result":{"tasks":[]},"next":0}`)
	}))
	defer server.Close()
	_, _, err := (Client{URL: server.URL, HTTP: server.Client()}).Fetch(context.Background(), 0)
	if err == nil {
		t.Fatal("repeated page accepted")
	}
}
