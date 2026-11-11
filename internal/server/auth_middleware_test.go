package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"akhflow.ai/akhflow/internal/domain"
)

type fakeUserLookup struct {
	user  *domain.User
	err   error
	calls int
}

func (f *fakeUserLookup) UserByID(_ context.Context, _ int64) (*domain.User, error) {
	f.calls++
	return f.user, f.err
}

func TestAuthMiddlewareLookupFailurePreservesValidToken(t *testing.T) {
	lookup := &fakeUserLookup{err: errors.New("sensitive database detail")}
	auth := &Auth{Store: lookup, Secret: []byte("test-only-signing-key"), TokenTTL: time.Hour}
	token, err := auth.CreateToken(42)
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		id, err := UserFrom(r.Context())
		if err != nil || id != 42 {
			t.Errorf("authenticated user = %d, error = %v", id, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := func(bearer string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}

	response := request(token)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("lookup failure status = %d, want 503", response.Code)
	}
	if called != 0 || strings.Contains(response.Body.String(), "sensitive database detail") {
		t.Fatalf("lookup failure leaked details or called handler: %q", response.Body.String())
	}

	lookup.err = nil
	lookup.user = &domain.User{ID: 42}
	response = request(token)
	if response.Code != http.StatusNoContent || called != 1 {
		t.Fatalf("valid token after recovery: status = %d, handler calls = %d", response.Code, called)
	}

	lookup.user = nil
	response = request(token)
	if response.Code != http.StatusUnauthorized || called != 1 {
		t.Fatalf("deleted user: status = %d, handler calls = %d", response.Code, called)
	}

	lookup.calls = 0
	for _, bearer := range []string{"", "invalid-token"} {
		response = request(bearer)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("invalid or missing token: status = %d", response.Code)
		}
	}
	expiredAuth := &Auth{Store: lookup, Secret: auth.Secret, TokenTTL: -time.Hour}
	expiredToken, err := expiredAuth.CreateToken(42)
	if err != nil {
		t.Fatal(err)
	}
	response = request(expiredToken)
	if response.Code != http.StatusUnauthorized || lookup.calls != 0 {
		t.Fatalf("expired token: status = %d, lookup calls = %d", response.Code, lookup.calls)
	}
}
