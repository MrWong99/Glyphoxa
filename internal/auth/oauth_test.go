package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeDiscord is a DiscordOAuth stand-in: AuthCodeURL echoes the state and
// Exchange returns a canned user — no live Discord call.
type fakeDiscord struct {
	user     auth.DiscordUser
	gotCode  string
	authBase string
}

func (f *fakeDiscord) AuthCodeURL(state string) string {
	return f.authBase + "?state=" + state
}

func (f *fakeDiscord) Exchange(_ context.Context, code string) (auth.DiscordUser, error) {
	f.gotCode = code
	return f.user, nil
}

// fakeOAuthStore records the writes the callback performs, counting each call so
// a rejected login can be asserted to have written NOTHING.
type fakeOAuthStore struct {
	upserted    storage.UpsertUserParams
	resolved    uuid.UUID
	created     storage.NewSession
	userID      uuid.UUID
	tenantID    uuid.UUID
	upsertCalls int
	resolveN    int
	createCalls int
}

func (f *fakeOAuthStore) UpsertUser(_ context.Context, p storage.UpsertUserParams) (storage.User, error) {
	f.upsertCalls++
	f.upserted = p
	return storage.User{ID: f.userID, DiscordUserID: p.DiscordUserID, Name: p.Name, Avatar: p.Avatar, Role: "operator"}, nil
}

func (f *fakeOAuthStore) ResolveOperatorTenant(_ context.Context, userID uuid.UUID) (storage.Tenant, error) {
	f.resolveN++
	f.resolved = userID
	return storage.Tenant{ID: f.tenantID, Name: "Glyphoxa"}, nil
}

func (f *fakeOAuthStore) CreateSession(_ context.Context, n storage.NewSession) (storage.Session, error) {
	f.createCalls++
	f.created = n
	return storage.Session{ID: uuid.New(), UserID: n.UserID, Token: n.Token, ExpiresAt: n.ExpiresAt}, nil
}

func TestOAuthLogin_RedirectsAndSetsState(t *testing.T) {
	t.Parallel()
	disc := &fakeDiscord{authBase: "https://discord/authorize"}
	o := auth.NewOAuth(&fakeOAuthStore{}, disc, "/", auth.Admission{Allowlist: auth.ParseOperatorAllowlist("")}, nil)

	rec := httptest.NewRecorder()
	o.Login(rec, httptest.NewRequest(http.MethodGet, "/auth/discord/login", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://discord/authorize?state=") {
		t.Fatalf("Location = %q", loc)
	}
	state := strings.TrimPrefix(loc, "https://discord/authorize?state=")

	// The state cookie is set and matches the state in the redirect URL.
	var stateCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "glyphoxa_oauth_state" {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("no state cookie set")
	}
	if stateCookie.Value != state {
		t.Errorf("state cookie %q != redirect state %q", stateCookie.Value, state)
	}
	if !stateCookie.HttpOnly {
		t.Error("state cookie must be HttpOnly")
	}
}

func TestOAuthCallback_IssuesSessionCookie(t *testing.T) {
	t.Parallel()
	disc := &fakeDiscord{user: auth.DiscordUser{ID: "77", Username: "sora", GlobalName: "Sora Vance", AvatarURL: "https://cdn/a.png"}}
	store := &fakeOAuthStore{userID: uuid.New(), tenantID: uuid.New()}
	// The fake user's snowflake IS on the allowlist (multi-value, whitespace-tolerant).
	o := auth.NewOAuth(store, disc, "/", auth.Admission{Allowlist: auth.ParseOperatorAllowlist("42, 77 99")}, nil)

	// A valid callback presents the state cookie matching the state param + a code.
	form := url.Values{"code": {"the-code"}, "state": {"st-1"}}
	req := httptest.NewRequest(http.MethodGet, "/auth/discord/callback?"+form.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "glyphoxa_oauth_state", Value: "st-1"})

	rec := httptest.NewRecorder()
	o.Callback(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want /", loc)
	}

	// The Discord identity was upserted (display name preferred) and the tenant bound.
	if store.upserted.DiscordUserID != "77" || store.upserted.Name != "Sora Vance" || store.upserted.Avatar != "https://cdn/a.png" {
		t.Errorf("upserted = %+v", store.upserted)
	}
	if store.resolved != store.userID {
		t.Errorf("ResolveOperatorTenant got %s, want %s", store.resolved, store.userID)
	}
	if disc.gotCode != "the-code" {
		t.Errorf("exchanged code = %q", disc.gotCode)
	}

	// The session + CSRF cookies are issued; the session cookie value matches the
	// persisted session token; HttpOnly only on the session cookie.
	cookies := map[string]*http.Cookie{}
	for _, c := range rec.Result().Cookies() {
		cookies[c.Name] = c
	}
	sess := cookies["glyphoxa_session"]
	csrf := cookies["glyphoxa_csrf"]
	if sess == nil || csrf == nil {
		t.Fatalf("missing session/csrf cookies: %v", cookies)
	}
	if sess.Value != store.created.Token {
		t.Errorf("session cookie %q != stored token %q", sess.Value, store.created.Token)
	}
	if !sess.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if csrf.HttpOnly {
		t.Error("csrf cookie must NOT be HttpOnly (double-submit needs script access)")
	}
	if sess.SameSite != http.SameSiteLaxMode {
		t.Error("session cookie must be SameSite=Lax")
	}
}

func TestOAuthCallback_BadState_Rejected(t *testing.T) {
	t.Parallel()
	store := &fakeOAuthStore{}
	o := auth.NewOAuth(store, &fakeDiscord{}, "/", auth.Admission{Allowlist: auth.ParseOperatorAllowlist("")}, nil)

	// State cookie does not match the state param.
	req := httptest.NewRequest(http.MethodGet, "/auth/discord/callback?code=c&state=mismatch", nil)
	req.AddCookie(&http.Cookie{Name: "glyphoxa_oauth_state", Value: "real-state"})

	rec := httptest.NewRecorder()
	o.Callback(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if store.created.Token != "" {
		t.Error("session created despite state mismatch")
	}
}

// A Discord User whose snowflake is NOT on the operator allowlist (ADR-0041) is
// denied a session and a Tenant binding: the callback rejects BEFORE any store
// write and bounces to /login?error=not_authorized. No session, no Tenant write,
// no auto-created Tenant.
func TestOAuthCallback_NotAllowlisted_Rejected(t *testing.T) {
	t.Parallel()
	disc := &fakeDiscord{user: auth.DiscordUser{ID: "999", Username: "stranger", GlobalName: "Stranger"}}
	store := &fakeOAuthStore{userID: uuid.New(), tenantID: uuid.New()}
	// Allowlist holds other snowflakes; "999" is absent.
	o := auth.NewOAuth(store, disc, "/", auth.Admission{Allowlist: auth.ParseOperatorAllowlist("42, 77")}, nil)

	form := url.Values{"code": {"the-code"}, "state": {"st-1"}}
	req := httptest.NewRequest(http.MethodGet, "/auth/discord/callback?"+form.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "glyphoxa_oauth_state", Value: "st-1"})

	rec := httptest.NewRecorder()
	o.Callback(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/login?error=not_authorized" {
		t.Errorf("Location = %q, want /login?error=not_authorized", loc)
	}

	// No session, no Tenant write, no user upsert — the gate ran first.
	if store.upsertCalls != 0 {
		t.Errorf("UpsertUser called %d times, want 0", store.upsertCalls)
	}
	if store.resolveN != 0 {
		t.Errorf("ResolveOperatorTenant called %d times, want 0", store.resolveN)
	}
	if store.createCalls != 0 {
		t.Errorf("CreateSession called %d times, want 0", store.createCalls)
	}

	// No session/csrf cookies were issued to the rejected user.
	for _, c := range rec.Result().Cookies() {
		if c.Name == "glyphoxa_session" || c.Name == "glyphoxa_csrf" {
			t.Errorf("issued %s cookie to a rejected user", c.Name)
		}
	}
}
