package trello

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestFetchBoardPaginatesWithSameSecondOverlap(t *testing.T) {
	cards := make([]Card, 1004)
	for i := range cards {
		seconds := 2_000_000 - i
		if i == 999 || i == 1000 {
			seconds = 2_000_000 - 998
		}
		cards[i] = Card{ID: fmt.Sprintf("%08x%016x", seconds, len(cards)-i), ListID: "list1", Name: "Card"}
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/boards/short123":
			fmt.Fprint(w, `{"id":"board123"}`)
		case "/boards/board123/lists":
			fmt.Fprint(w, `[{"id":"list1","name":"Inbox"}]`)
		case "/boards/board123/cards":
			requests++
			query := r.URL.Query()
			if query.Get("sort") != "-id" || query.Get("limit") != "1000" || query.Get("filter") != "open" {
				t.Errorf("unexpected pagination query: %s", r.URL.RawQuery)
			}
			before := time.Time{}
			if query.Get("before") != "" {
				var err error
				before, err = time.Parse(time.RFC3339, query.Get("before"))
				if err != nil {
					t.Errorf("bad cursor: %v", err)
				}
			}
			result := []Card{}
			for _, card := range cards {
				var seconds int64
				fmt.Sscanf(card.ID[:8], "%x", &seconds)
				if !before.IsZero() && !time.Unix(seconds, 0).Before(before) {
					continue
				}
				result = append(result, card)
				if len(result) == cardPageSize {
					break
				}
			}
			_ = json.NewEncoder(w).Encode(result)
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
	if requests != 2 || len(board.Cards) != len(cards) {
		t.Fatalf("requests=%d cards=%d, want %d", requests, len(board.Cards), len(cards))
	}
	for i, card := range board.Cards {
		if card.ID != cards[i].ID {
			t.Fatalf("card %d: got %s, want %s", i, card.ID, cards[i].ID)
		}
	}
}

func TestFetchCardsRejectsStalledPagination(t *testing.T) {
	page := make([]Card, cardPageSize)
	for i := range page {
		page[i] = Card{ID: fmt.Sprintf("%08x%016x", 2_000_000, len(page)-i), ListID: "list1", Name: "Card"}
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()
	client := New("example-key", "example-token")
	client.BaseURL = server.URL
	cards, err := client.fetchCards(context.Background(), "/boards/board123")
	if err == nil || cards != nil || requests != 2 {
		t.Fatalf("stalled pagination: cards=%d requests=%d error=%v", len(cards), requests, err)
	}
}

func TestFetchCardsRejectsFailureAfterFirstPage(t *testing.T) {
	page := make([]Card, cardPageSize)
	for i := range page {
		page[i] = Card{ID: fmt.Sprintf("%08x%016x", 2_000_000-i, len(page)-i), ListID: "list1", Name: "Card"}
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 2 {
			http.Error(w, "upstream failed", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()
	client := New("example-key", "example-token")
	client.BaseURL = server.URL
	cards, err := client.fetchCards(context.Background(), "/boards/board123")
	if err == nil || cards != nil || requests != 2 {
		t.Fatalf("failed second page: cards=%d requests=%d error=%v", len(cards), requests, err)
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
