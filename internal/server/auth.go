package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"akhflow.ai/akhflow/internal/domain"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/argon2"
)

type userLookup interface {
	UserByID(ctx context.Context, id int64) (*domain.User, error)
}

type Auth struct {
	Store    userLookup
	Secret   []byte
	TokenTTL time.Duration
}

func (a *Auth) HashPassword(password string) string {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	h := argon2.IDKey([]byte(password), salt, 1, 64*1024, 1, 32)
	return "argon2id$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(h)
}

func (a *Auth) VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 3 || parts[0] != "argon2id" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 1, 64*1024, 1, 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (a *Auth) CreateToken(userID int64) (string, error) {
	claims := jwt.MapClaims{
		"sub": fmt.Sprint(userID),
		"exp": time.Now().Add(a.TokenTTL).Unix(),
		"iat": time.Now().Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(a.Secret)
}

func (a *Auth) ParseToken(tokenStr string) (int64, error) {
	id, _, err := a.parseToken(tokenStr)
	return id, err
}

func (a *Auth) parseToken(tokenStr string) (int64, time.Time, error) {
	tok, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return a.Secret, nil
	}, jwt.WithExpirationRequired())
	if err != nil {
		return 0, time.Time{}, err
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok || !tok.Valid {
		return 0, time.Time{}, errors.New("invalid token")
	}
	sub, ok := claims["sub"].(string)
	if !ok {
		return 0, time.Time{}, errors.New("missing subject")
	}
	var id int64
	if _, err := fmt.Sscanf(sub, "%d", &id); err != nil {
		return 0, time.Time{}, err
	}
	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil {
		return 0, time.Time{}, errors.New("missing expiration")
	}
	return id, exp.Time, nil
}

type contextKey string

const userCtxKey contextKey = "user"
const tokenExpiryCtxKey contextKey = "token_expiry"

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			http.Error(w, `{"error":"missing token"}`, http.StatusUnauthorized)
			return
		}
		id, expiry, err := a.parseToken(strings.TrimPrefix(h, "Bearer "))
		if err != nil {
			http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
			return
		}
		u, err := a.Store.UserByID(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication temporarily unavailable"})
			return
		}
		if u == nil {
			http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, id)
		ctx = context.WithValue(ctx, tokenExpiryCtxKey, expiry)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func UserFrom(ctx context.Context) (int64, error) {
	id, ok := ctx.Value(userCtxKey).(int64)
	if !ok {
		return 0, errors.New("no user in context")
	}
	return id, nil
}

func (a *Auth) ActorName(ctx context.Context) string {
	id, err := UserFrom(ctx)
	if err != nil {
		return "anonymous"
	}
	u, err := a.Store.UserByID(ctx, id)
	if err != nil || u == nil {
		return fmt.Sprint(id)
	}
	return u.Email
}

var _ = domain.StatusBacklog
