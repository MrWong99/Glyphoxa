package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// guarded wraps a 200-OK handler in RequireSession backed by a one-token authN.
func guarded() http.Handler {
	authN := fakeAuthN{users: map[string]storage.User{
		validToken: {ID: uuid.New(), Name: "op", Role: "operator"},
	}}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("served"))
	})
	return auth.RequireSession(authN, ok)
}

func TestRequireSession_ValidCookiePasses(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/abc/events", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: validToken})

	guarded().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "served" {
		t.Fatalf("valid cookie: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestRequireSession_MissingCookie401(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/abc/events", nil)

	guarded().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing cookie: code=%d, want 401", rec.Code)
	}
}

func TestRequireSession_InvalidCookie401(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/abc/events", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "nope"})

	guarded().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid cookie: code=%d, want 401", rec.Code)
	}
}

// guard against an accidentally-blocking next call: ensure the downstream
// handler is never invoked without auth.
func TestRequireSession_DoesNotCallNextUnauthed(t *testing.T) {
	called := false
	h := auth.RequireSession(
		fakeAuthN{users: map[string]storage.User{}},
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }),
	)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if called {
		t.Fatal("next handler ran without a valid session")
	}
}
