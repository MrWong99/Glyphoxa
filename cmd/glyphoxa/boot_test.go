package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeOAuthStore is an in-memory auth.OAuthStore recording what seedDevSession
// wrote, so the dev-mode auto-auth boot is testable without Postgres.
type fakeOAuthStore struct {
	upsertedDiscordID string
	tenantForUser     uuid.UUID
	sessions          []storage.NewSession
	userID            uuid.UUID
}

func (f *fakeOAuthStore) UpsertUser(_ context.Context, p storage.UpsertUserParams) (storage.User, error) {
	f.upsertedDiscordID = p.DiscordUserID
	if f.userID == uuid.Nil {
		f.userID = uuid.New()
	}
	return storage.User{ID: f.userID, DiscordUserID: p.DiscordUserID, Name: p.Name, Role: "operator"}, nil
}

func (f *fakeOAuthStore) ResolveOperatorTenant(_ context.Context, userID uuid.UUID) (storage.Tenant, error) {
	f.tenantForUser = userID
	return storage.Tenant{ID: uuid.New()}, nil
}

func (f *fakeOAuthStore) CreateSession(_ context.Context, n storage.NewSession) (storage.Session, error) {
	f.sessions = append(f.sessions, n)
	return storage.Session{ID: uuid.New(), UserID: n.UserID, Token: n.Token, ExpiresAt: n.ExpiresAt}, nil
}

// anyTokenAuth accepts any non-empty session token as the seeded dev operator, so
// devAuthMiddleware can be exercised through the REAL auth.RequireSession guard
// without a DB.
type anyTokenAuth struct{ id uuid.UUID }

func (a anyTokenAuth) AuthenticateSession(_ context.Context, token string) (storage.User, error) {
	if token == "" {
		return storage.User{}, storage.ErrNotFound
	}
	return storage.User{ID: a.id, Name: "Dev Operator"}, nil
}

// envMap adapts a map into a getenv func for the boot-preflight helpers, so the
// tests never mutate the real process environment (which would race t.Parallel
// and leak between cases).
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// TestRequireWebEnv is the ADR-0041 boot preflight: web/all Mode must refuse to
// boot unless all three DISCORD_OAUTH_* vars AND a non-empty GLYPHOXA_OPERATOR_IDS
// are present, and the fatal error must NAME every missing variable so the
// operator can fix the deploy in one pass.
func TestRequireWebEnv(t *testing.T) {
	all := map[string]string{
		"DISCORD_OAUTH_CLIENT_ID":     "cid",
		"DISCORD_OAUTH_CLIENT_SECRET": "secret",
		"DISCORD_OAUTH_REDIRECT_URL":  "https://x/cb",
		"GLYPHOXA_OPERATOR_IDS":       "123",
	}

	// Fully configured → boots cleanly (AC3).
	if err := requireWebEnv(envMap(all)); err != nil {
		t.Fatalf("requireWebEnv with a full config returned %v, want nil", err)
	}

	// Each required var, when missing, must be named in the fatal error.
	for _, missing := range []string{
		"DISCORD_OAUTH_CLIENT_ID",
		"DISCORD_OAUTH_CLIENT_SECRET",
		"DISCORD_OAUTH_REDIRECT_URL",
		"GLYPHOXA_OPERATOR_IDS",
	} {
		env := map[string]string{}
		for k, v := range all {
			env[k] = v
		}
		delete(env, missing)
		err := requireWebEnv(envMap(env))
		if err == nil {
			t.Fatalf("requireWebEnv missing %s returned nil, want a fatal error", missing)
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("requireWebEnv missing %s: error %q does not name the variable", missing, err)
		}
	}

	// A blank/whitespace value counts as missing (an empty allowlist is not a gate).
	blank := map[string]string{
		"DISCORD_OAUTH_CLIENT_ID":     "cid",
		"DISCORD_OAUTH_CLIENT_SECRET": "secret",
		"DISCORD_OAUTH_REDIRECT_URL":  "https://x/cb",
		"GLYPHOXA_OPERATOR_IDS":       "   ",
	}
	if err := requireWebEnv(envMap(blank)); err == nil || !strings.Contains(err.Error(), "GLYPHOXA_OPERATOR_IDS") {
		t.Errorf("requireWebEnv with a whitespace allowlist returned %v, want an error naming GLYPHOXA_OPERATOR_IDS", err)
	}

	// Present but useless allowlists fail the parse-based check (the same parser
	// as the #103 runtime gate): separators only parses to zero entries, and a
	// non-snowflake entry can never match a login — either way the deploy would
	// look healthy while nobody can log in, the exact state #112 prevents.
	sepOnly := map[string]string{}
	for k, v := range all {
		sepOnly[k] = v
	}
	sepOnly["GLYPHOXA_OPERATOR_IDS"] = " , ,, "
	if err := requireWebEnv(envMap(sepOnly)); err == nil || !strings.Contains(err.Error(), "GLYPHOXA_OPERATOR_IDS") {
		t.Errorf("requireWebEnv with a separators-only allowlist returned %v, want an error naming GLYPHOXA_OPERATOR_IDS", err)
	}
	malformed := map[string]string{}
	for k, v := range all {
		malformed[k] = v
	}
	malformed["GLYPHOXA_OPERATOR_IDS"] = "MrWong99, 770000000000000000"
	if err := requireWebEnv(envMap(malformed)); err == nil || !strings.Contains(err.Error(), "MrWong99") {
		t.Errorf("requireWebEnv with a non-snowflake entry returned %v, want an error naming the bad entry", err)
	}

	// Nothing set → every variable is named.
	err := requireWebEnv(envMap(map[string]string{}))
	if err == nil {
		t.Fatal("requireWebEnv with an empty env returned nil, want a fatal error")
	}
	for _, want := range []string{
		"DISCORD_OAUTH_CLIENT_ID",
		"DISCORD_OAUTH_CLIENT_SECRET",
		"DISCORD_OAUTH_REDIRECT_URL",
		"GLYPHOXA_OPERATOR_IDS",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("empty-env error %q does not name %s", err, want)
		}
	}
}

// TestDevMode: the opt-out is on only when GLYPHOXA_DEV_MODE holds a non-blank
// value that is not an explicit falsy spelling — an operator writing
// GLYPHOXA_DEV_MODE=false to disable the auth bypass must get it disabled.
func TestDevMode(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"   ", false},
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{" no ", false},
		{"off", false},
		{"1", true},
		{"true", true},
		{"yes", true},
	}
	for _, c := range cases {
		if got := devMode(envMap(map[string]string{"GLYPHOXA_DEV_MODE": c.val})); got != c.want {
			t.Errorf("devMode(%q) = %v, want %v", c.val, got, c.want)
		}
	}
}

// TestSTTStreaming: the streaming-STT opt-in (ADR-0042, issue #180) is on only
// when GLYPHOXA_STT_STREAMING holds a non-blank, non-falsy value, so the batch
// path stays the byte-for-byte default until an operator explicitly opts in.
func TestSTTStreaming(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"   ", false},
		{"0", false},
		{"false", false},
		{"OFF", false},
		{" no ", false},
		{"1", true},
		{"true", true},
		{"yes", true},
	}
	for _, c := range cases {
		if got := sttStreaming(envMap(map[string]string{"GLYPHOXA_STT_STREAMING": c.val})); got != c.want {
			t.Errorf("sttStreaming(%q) = %v, want %v", c.val, got, c.want)
		}
	}
}

// TestForceLoopback pins the listen host to 127.0.0.1 while preserving the port,
// so a GLYPHOXA_DEV_MODE instance is structurally unreachable from a container
// port-mapping (ADR-0041) regardless of the configured -web-addr.
func TestForceLoopback(t *testing.T) {
	cases := []struct{ in, want string }{
		{":8080", "127.0.0.1:8080"},
		{"0.0.0.0:8080", "127.0.0.1:8080"},
		{"0.0.0.0:0", "127.0.0.1:0"},
		{"[::]:9000", "127.0.0.1:9000"},
		{"example:1234", "127.0.0.1:1234"},
	}
	for _, c := range cases {
		if got := forceLoopback(c.in); got != c.want {
			t.Errorf("forceLoopback(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSeedDevSession: the dev-mode boot upserts the fixed synthetic operator,
// binds its tenant, and mints a real session — the same row the OAuth callback
// creates — so the injected cookies flow through the existing gate.
func TestSeedDevSession(t *testing.T) {
	store := &fakeOAuthStore{}
	fixed := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

	sess, csrf, err := seedDevSession(context.Background(), store, func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("seedDevSession: %v", err)
	}
	if sess == "" || csrf == "" || sess == csrf {
		t.Fatalf("tokens must be non-empty and distinct: session=%q csrf=%q", sess, csrf)
	}
	if store.upsertedDiscordID != storage.DevOperatorDiscordID {
		t.Errorf("upserted discord id = %q, want %q", store.upsertedDiscordID, storage.DevOperatorDiscordID)
	}
	if store.tenantForUser != store.userID {
		t.Errorf("ResolveOperatorTenant called with %v, want the upserted user %v", store.tenantForUser, store.userID)
	}
	if len(store.sessions) != 1 {
		t.Fatalf("created %d sessions, want 1", len(store.sessions))
	}
	got := store.sessions[0]
	if got.Token != sess {
		t.Errorf("session row token = %q, want the returned session token %q", got.Token, sess)
	}
	if got.UserID != store.userID {
		t.Errorf("session row user = %v, want the seeded operator %v", got.UserID, store.userID)
	}
	if want := fixed.Add(devSessionTTL); !got.ExpiresAt.Equal(want) {
		t.Errorf("session expiry = %v, want now+TTL %v", got.ExpiresAt, want)
	}
}

// TestDevAuthMiddleware: every request reaches the inner handler stamped with the
// session cookie AND a CSRF cookie whose value matches the X-CSRF-Token header
// (the double-submit pair), regardless of what the client sent.
func TestDevAuthMiddleware(t *testing.T) {
	var gotSess, gotCSRFCookie, gotCSRFHeader string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(auth.SessionCookieName); err == nil {
			gotSess = c.Value
		}
		if c, err := r.Cookie(auth.CSRFCookieName); err == nil {
			gotCSRFCookie = c.Value
		}
		gotCSRFHeader = r.Header.Get("X-CSRF-Token")
	})
	// A cached, still-valid pair (anyTokenAuth accepts it) is injected as-is.
	d := &devSessions{
		store: &fakeOAuthStore{}, authn: anyTokenAuth{}, now: time.Now,
		session: "sess-tok", csrf: "csrf-tok",
	}
	h := devAuthMiddleware(d, slog.New(slog.DiscardHandler), inner)

	// A request carrying a STALE session cookie must be overwritten, not merged.
	req := httptest.NewRequest(http.MethodPost, "/api/anything", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "stale"})
	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotSess != "sess-tok" {
		t.Errorf("injected session cookie = %q, want %q (stale cookie must be replaced)", gotSess, "sess-tok")
	}
	if gotCSRFCookie != "csrf-tok" {
		t.Errorf("injected csrf cookie = %q, want %q", gotCSRFCookie, "csrf-tok")
	}
	if gotCSRFHeader != gotCSRFCookie {
		t.Errorf("X-CSRF-Token header %q must match the csrf cookie %q (double-submit)", gotCSRFHeader, gotCSRFCookie)
	}
}

// deadTokenAuth rejects every session token, standing in for a session row that
// a Logout deleted or a TTL expired.
type deadTokenAuth struct{}

func (deadTokenAuth) AuthenticateSession(_ context.Context, _ string) (storage.User, error) {
	return storage.User{}, storage.ErrNotFound
}

// TestDevAuthMiddleware_ReseedsDeadSession: when the cached dev session no
// longer authenticates (the SPA's Logout deleted the row, or the 24h TTL
// expired), the middleware re-seeds a fresh session instead of 401ing every
// request until a process restart, and injects the NEW token.
func TestDevAuthMiddleware_ReseedsDeadSession(t *testing.T) {
	store := &fakeOAuthStore{}
	d := &devSessions{
		store: store, authn: deadTokenAuth{}, now: time.Now,
		session: "logged-out-tok", csrf: "old-csrf",
	}
	var gotSess string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(auth.SessionCookieName); err == nil {
			gotSess = c.Value
		}
	})
	h := devAuthMiddleware(d, slog.New(slog.DiscardHandler), inner)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/anything", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(store.sessions) != 1 {
		t.Fatalf("re-seed created %d sessions, want 1", len(store.sessions))
	}
	if gotSess == "logged-out-tok" || gotSess != store.sessions[0].Token {
		t.Errorf("injected session = %q, want the freshly minted %q (not the dead token)", gotSess, store.sessions[0].Token)
	}
}

// TestDevAuthMiddleware_RefusesProxiedRequests: a request carrying reverse-proxy
// evidence is 403'd BEFORE any session is stamped — the loopback bind stops
// container port-mappings, but a same-host proxy still dials 127.0.0.1, and
// auto-authenticating proxied traffic would hand every visitor the operator
// console (ADR-0041).
func TestDevAuthMiddleware_RefusesProxiedRequests(t *testing.T) {
	for _, header := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "Forwarded"} {
		t.Run(header, func(t *testing.T) {
			reached := false
			inner := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { reached = true })
			d := &devSessions{
				store: &fakeOAuthStore{}, authn: anyTokenAuth{}, now: time.Now,
				session: "sess-tok", csrf: "csrf-tok",
			}
			h := devAuthMiddleware(d, slog.New(slog.DiscardHandler), inner)

			req := httptest.NewRequest(http.MethodGet, "/api/anything", nil)
			req.Header.Set(header, "203.0.113.7")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403 for a request carrying %s", rec.Code, header)
			}
			if reached {
				t.Errorf("inner handler reached despite proxy evidence (%s)", header)
			}
		})
	}
}

// TestEnableDevMode ties the opt-out together: it forces the loopback bind, logs a
// loud insecure-mode Warn, and returns a wrapper that auto-authenticates a
// cookieless request through the REAL auth.RequireSession guard (AC2).
func TestEnableDevMode(t *testing.T) {
	store := &fakeOAuthStore{}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	addr, wrap, err := enableDevMode(context.Background(), store, anyTokenAuth{}, "0.0.0.0:8080", log, time.Now)
	if err != nil {
		t.Fatalf("enableDevMode: %v", err)
	}
	if addr != "127.0.0.1:8080" {
		t.Errorf("dev-mode addr = %q, want the forced loopback bind 127.0.0.1:8080", addr)
	}

	logged := buf.String()
	if !strings.Contains(logged, "level=WARN") || !strings.Contains(logged, "INSECURE") {
		t.Errorf("dev-mode must log a loud WARN insecure-mode warning; got %q", logged)
	}
	if !strings.Contains(logged, "127.0.0.1:8080") {
		t.Errorf("dev-mode warning should name the loopback bind; got %q", logged)
	}

	// The wrapper auto-authenticates: a request with NO cookies passes the real
	// RequireSession guard and reaches the protected handler.
	reached := false
	protected := auth.RequireSession(anyTokenAuth{id: store.userID}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	wrap(protected).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/x/events", nil))
	if !reached || rec.Code != http.StatusOK {
		t.Errorf("cookieless request was not auto-authenticated: reached=%v code=%d", reached, rec.Code)
	}
}
