package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/store"
)

func TestImportAkhcheckHTTP(t *testing.T) {
	dsn := os.Getenv("AKHFLOW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AKHFLOW_TEST_DATABASE_URL must point to a disposable database")
	}
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	userID, err := db.CreateUser(ctx, fmt.Sprintf("http-%d@example.invalid", suffix), "Test", "unused")
	if err != nil {
		t.Fatal(err)
	}
	slug := fmt.Sprintf("test-%d", suffix)
	projectID, err := db.CreateProject(ctx, slug, "Test", userID)
	if err != nil {
		t.Fatal(err)
	}
	auth := &Auth{Store: db, Secret: []byte("test-secret"), TokenTTL: time.Hour}
	token, err := auth.CreateToken(userID)
	if err != nil {
		t.Fatal(err)
	}
	outsiderID, err := db.CreateUser(ctx, fmt.Sprintf("outsider-%d@example.invalid", suffix), "Other", "unused")
	if err != nil {
		t.Fatal(err)
	}
	outsiderToken, err := auth.CreateToken(outsiderID)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(db, auth, nil, nil).Handler()
	request := func(body string, authToken string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+slug+"/imports/akhcheck", bytes.NewBufferString(body))
		if authToken != "" {
			req.Header.Set("Authorization", "Bearer "+authToken)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	body := `{"id":117,"tasks":[{"id":11,"name":"Task","deadline":"2026-10-12T12:00:00+03:00"}]}`
	if response := request(body, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status: %d", response.Code)
	}
	if response := request(body, outsiderToken); response.Code != http.StatusForbidden {
		t.Fatalf("outsider status: %d", response.Code)
	}
	if response := request(`{"id":117}`, token); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid export status: %d", response.Code)
	}
	for attempt := 0; attempt < 2; attempt++ {
		response := request(body, token)
		if response.Code != http.StatusOK {
			t.Fatalf("status: %d, body: %s", response.Code, response.Body.String())
		}
		var result store.AkhcheckImportResult
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if attempt == 0 && (result.Created != 1 || result.Skipped != 0) {
			t.Fatalf("first import: %+v", result)
		}
		if attempt == 1 && (result.Created != 0 || result.Skipped != 1) {
			t.Fatalf("repeat import: %+v", result)
		}
	}
	tasks, err := db.ListTasks(ctx, projectID, "")
	if err != nil || len(tasks) != 1 || tasks[0].Title != "Task" {
		t.Fatalf("tasks: %+v, err: %v", tasks, err)
	}
}
