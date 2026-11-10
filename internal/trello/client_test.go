package trello

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchBoard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != `OAuth oauth_consumer_key="example-key", oauth_token="example-token"` {
			t.Error("authorization header missing")
		}
		if r.URL.Query().Get("key") != "" || r.URL.Query().Get("token") != "" {
			t.Error("credentials leaked into URL")
		}
		switch r.URL.Path {
		case "/boards/short123":
			fmt.Fprint(w, `{"id":"123456789012345678901234"}`)
		case "/boards/123456789012345678901234/lists":
			fmt.Fprint(w, `[{"id":"list1","name":"Inbox"}]`)
		case "/boards/123456789012345678901234/cards":
			fmt.Fprint(w, `[{"id":"card1","idList":"list1","name":"Example","desc":"Details","due":"2026-10-12T12:00:00Z"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := New("example-key", "example-token")
	client.BaseURL = server.URL
	board, err := client.FetchBoard(context.Background(), "short123")
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Lists) != 1 || len(board.Cards) != 1 || board.Cards[0].Due == nil {
		t.Fatalf("unexpected board: %+v", board)
	}
}

func TestFetchBoardRejectsInvalidIDAndRedactsErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "remote response containing private content", http.StatusUnauthorized)
	}))
	defer server.Close()
	client := New("example-key", "example-token")
	client.BaseURL = server.URL
	if _, err := client.FetchBoard(context.Background(), "../../example"); err == nil {
		t.Fatal("expected invalid board ID to be rejected")
	}
	_, err := client.FetchBoard(context.Background(), "short123")
	if err == nil || strings.Contains(err.Error(), "example-token") || strings.Contains(err.Error(), "private content") {
		t.Fatalf("unexpected error: %v", err)
	}
}
