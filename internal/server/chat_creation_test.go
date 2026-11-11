package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/store"
	"akhflow.ai/akhflow/internal/testdb"
)

func TestChatCreationIdempotency(t *testing.T) {
	dsn := testdb.Schema(t, "AKHFLOW_TEST_DATABASE_URL")
	ctx := context.Background()
	db, err := store.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateUser(ctx, "chat-owner@example.invalid", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "chat-editor@example.invalid", "Editor", "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "chat-a", "A", owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateProject(ctx, "chat-b", "B", owner); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.SetProjectMemberRole(ctx, project, owner, other, "editor"); err != nil || !ok {
		t.Fatalf("set editor role: %v", err)
	}
	auth := &Auth{Store: db, Secret: []byte("test-secret"), TokenTTL: time.Hour}
	handler := New(db, auth, nil, nil).Handler()
	token := func(user int64) string {
		t.Helper()
		value, err := auth.CreateToken(user)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	ownerToken := token(owner)
	otherToken := token(other)
	const key = "00000000-0000-4000-8000-000000000001"
	request := func(userToken, slug, requestKey string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+slug+"/chats", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+userToken)
		if requestKey != "" {
			req.Header.Set("Idempotency-Key", requestKey)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	chatID := func(response *httptest.ResponseRecorder) int64 {
		t.Helper()
		if response.Code != http.StatusCreated {
			t.Fatalf("create returned %d: %s", response.Code, response.Body.String())
		}
		var result struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.ID == 0 {
			t.Fatalf("invalid create result: %s (%v)", response.Body.String(), err)
		}
		return result.ID
	}
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 8)
	for i := 0; i < cap(responses); i++ {
		go func() {
			<-start
			responses <- request(ownerToken, "chat-a", key)
		}()
	}
	close(start)
	firstID := chatID(<-responses)
	for i := 1; i < cap(responses); i++ {
		if id := chatID(<-responses); id != firstID {
			t.Fatalf("same key created different chats: %d and %d", firstID, id)
		}
	}
	if id := chatID(request(ownerToken, "chat-a", key)); id != firstID {
		t.Fatal("retry did not return the original chat")
	}
	if id := chatID(request(otherToken, "chat-a", key)); id == firstID {
		t.Fatal("key leaked between users")
	}
	if id := chatID(request(ownerToken, "chat-b", key)); id == firstID {
		t.Fatal("key leaked between projects")
	}
	firstLegacy := chatID(request(ownerToken, "chat-a", ""))
	secondLegacy := chatID(request(ownerToken, "chat-a", ""))
	if firstLegacy == secondLegacy {
		t.Fatal("requests without a key must create separate chats")
	}
	if response := request(ownerToken, "chat-a", "not-a-uuid"); response.Code != http.StatusBadRequest {
		t.Fatalf("bad key returned %d", response.Code)
	}
	if ok, err := db.SetProjectMemberRole(ctx, project, owner, other, "viewer"); err != nil || !ok {
		t.Fatalf("set viewer role: %v", err)
	}
	if response := request(otherToken, "chat-a", key); response.Code != http.StatusForbidden {
		t.Fatalf("retry after losing write access returned %d", response.Code)
	}
}
