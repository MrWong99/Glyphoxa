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

// TestRequireSession_InjectsUser proves the guard now puts the resolved operator
// into the request context (a pure addition — the relay handlers that ignore it
// are unaffected), so a downstream plain handler (ServeImport) can read the
// tenant off the session without re-authenticating.
func TestRequireSession_InjectsUser(t *testing.T) {
	want := storage.User{ID: uuid.New(), Name: "op", Role: "operator"}
	authN := fakeAuthN{users: map[string]storage.User{validToken: want}}

	var got storage.User
	var ok bool
	h := auth.RequireSession(authN, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, ok = auth.CurrentUser(r.Context())
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/import", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: validToken})
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !ok {
		t.Fatal("no user injected into ctx")
	}
	if got.ID != want.ID {
		t.Fatalf("user ID = %s, want %s", got.ID, want.ID)
	}
}

// TestRequireCSRF drives the plain-HTTP double-submit mirror of the Connect CSRF
// interceptor (ADR-0016): the glyphoxa_csrf cookie must constant-time-match the
// X-CSRF-Token header, else 403; RequireSession alone does not gate a plain POST.
func TestRequireCSRF(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("served"))
	})
	guard := auth.RequireCSRF(ok)

	newReq := func() *http.Request {
		return httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/import", nil)
	}

	t.Run("match passes", func(t *testing.T) {
		req := newReq()
		req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "tok123"})
		req.Header.Set("X-CSRF-Token", "tok123")
		rec := httptest.NewRecorder()
		guard.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "served" {
			t.Fatalf("match: code=%d body=%q", rec.Code, rec.Body.String())
		}
	})

	t.Run("mismatch is 403", func(t *testing.T) {
		req := newReq()
		req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "tok123"})
		req.Header.Set("X-CSRF-Token", "other")
		rec := httptest.NewRecorder()
		guard.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("mismatch: code=%d, want 403", rec.Code)
		}
	})

	t.Run("missing header is 403", func(t *testing.T) {
		req := newReq()
		req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "tok123"})
		rec := httptest.NewRecorder()
		guard.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("missing header: code=%d, want 403", rec.Code)
		}
	})

	t.Run("missing cookie is 403", func(t *testing.T) {
		req := newReq()
		req.Header.Set("X-CSRF-Token", "tok123")
		rec := httptest.NewRecorder()
		guard.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("missing cookie: code=%d, want 403", rec.Code)
		}
	})
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
