package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func testPolicy() *auth.Policy {
	return auth.NewPolicy(
		fakeAuthN{users: map[string]storage.User{validToken: operator()}},
		fakeTenant{id: uuid.New()},
	)
}

// mustPanic asserts fn panics — the "loudly fails at startup" contract of the
// mount table (#446): an under-declared row must kill the boot, not serve.
func mustPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: MustGuardMounts did not panic", name)
		}
	}()
	fn()
}

func TestMustGuardMounts_RejectsUnderDeclaredRows(t *testing.T) {
	t.Parallel()
	p := testPolicy()

	mustPanic(t, "undeclared tenant mode", func() {
		auth.MustGuardMounts(p, []auth.GuardedMount{
			{Pattern: "GET /x", Handler: okHandler()}, // Tenant omitted — the #408 shape
		})
	})
	mustPanic(t, "nil handler", func() {
		auth.MustGuardMounts(p, []auth.GuardedMount{
			{Pattern: "GET /x", Tenant: auth.TenantNone},
		})
	})
	mustPanic(t, "empty pattern", func() {
		auth.MustGuardMounts(p, []auth.GuardedMount{
			{Tenant: auth.TenantNone, Handler: okHandler()},
		})
	})
	mustPanic(t, "pattern without method", func() {
		auth.MustGuardMounts(p, []auth.GuardedMount{
			{Pattern: "/api/v1/x", Tenant: auth.TenantNone, Handler: okHandler()},
		})
	})
	mustPanic(t, "duplicate pattern", func() {
		auth.MustGuardMounts(p, []auth.GuardedMount{
			{Pattern: "GET /x", Tenant: auth.TenantNone, Handler: okHandler()},
			{Pattern: "GET /x", Tenant: auth.TenantRequired, Handler: okHandler()},
		})
	})
	mustPanic(t, "nil policy", func() {
		auth.MustGuardMounts(nil, []auth.GuardedMount{
			{Pattern: "GET /x", Tenant: auth.TenantNone, Handler: okHandler()},
		})
	})
}

// TestGuardedMount_CSRFDerivesFromMethod proves the table's CSRF rule is
// structural, not per-row: a state-changing method demands the double-submit
// pair (the plain-HTTP spelling of the Connect NO_SIDE_EFFECTS exemption,
// ADR-0016) without any mount having to remember to compose it — the class of
// omission the hand-built wrapper chains allowed.
func TestGuardedMount_CSRFDerivesFromMethod(t *testing.T) {
	t.Parallel()
	guarded := auth.MustGuardMounts(testPolicy(), []auth.GuardedMount{
		{Pattern: "GET /read", Tenant: auth.TenantNone, Handler: okHandler()},
		{Pattern: "POST /write", Tenant: auth.TenantNone, Handler: okHandler()},
	})
	read, write := guarded[0].Handler, guarded[1].Handler

	authedReq := func(method, path string) *http.Request {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: validToken})
		return r
	}

	// GET passes with a session alone — reads are CSRF-exempt.
	rec := httptest.NewRecorder()
	read.ServeHTTP(rec, authedReq(http.MethodGet, "/read"))
	if rec.Code != http.StatusOK {
		t.Errorf("GET without CSRF pair: status = %d, want 200", rec.Code)
	}

	// POST with a session but no CSRF pair is 403 — no row declared this;
	// the method did. The body text is pinned: it is the policy's single
	// denial message, shared verbatim with the Connect transport.
	rec = httptest.NewRecorder()
	write.ServeHTTP(rec, authedReq(http.MethodPost, "/write"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST without CSRF pair: status = %d, want 403", rec.Code)
	}
	if got := rec.Body.String(); got != "csrf check failed, retry\n" {
		t.Errorf("POST without CSRF pair: body = %q, want the pinned denial text", got)
	}

	// POST with the double-submit pair passes.
	req := authedReq(http.MethodPost, "/write")
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "tok123"})
	req.Header.Set("X-CSRF-Token", "tok123")
	rec = httptest.NewRecorder()
	write.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("POST with CSRF pair: status = %d, want 200", rec.Code)
	}
}

// TestGuardedMount_SessionAlwaysRequired: every table row is operator-only
// (ADR-0041) — there is no way to declare a session-exempt guarded mount.
func TestGuardedMount_SessionAlwaysRequired(t *testing.T) {
	t.Parallel()
	guarded := auth.MustGuardMounts(testPolicy(), []auth.GuardedMount{
		{Pattern: "GET /read", Tenant: auth.TenantNone, Handler: okHandler()},
	})

	rec := httptest.NewRecorder()
	guarded[0].Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/read", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("cookieless request: status = %d, want 401", rec.Code)
	}
	if got := rec.Body.String(); got != "please sign in\n" {
		t.Errorf("cookieless request: body = %q, want the pinned denial text", got)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/read", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "nope"})
	guarded[0].Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("invalid session: status = %d, want 401", rec.Code)
	}
}
