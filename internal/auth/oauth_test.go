package auth_test

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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
	suspendedAt *time.Time
	upsertCalls int
	resolveN    int
	createCalls int
}

func (f *fakeOAuthStore) UpsertUser(_ context.Context, p storage.UpsertUserParams) (storage.User, error) {
	f.upsertCalls++
	f.upserted = p
	return storage.User{ID: f.userID, DiscordUserID: p.DiscordUserID, Name: p.Name, Avatar: p.Avatar, Role: "operator", SuspendedAt: f.suspendedAt}, nil
}

func (f *fakeOAuthStore) ResolveOperatorTenant(_ context.Context, userID uuid.UUID) (storage.Tenant, error) {
	f.resolveN++
	f.resolved = userID
	return storage.Tenant{ID: f.tenantID, Name: "Glyphoxa"}, nil
}

func (f *fakeOAuthStore) CreateSession(_ context.Context, n storage.NewSession) (storage.Session, error) {
	f.createCalls++
	f.created = n
	return storage.Session{ID: uuid.New(), UserID: n.UserID, ExpiresAt: n.ExpiresAt}, nil
}

func TestOAuthLogin_RedirectsAndSetsState(t *testing.T) {
	t.Parallel()
	disc := &fakeDiscord{authBase: "https://discord/authorize"}
	o := auth.NewOAuth(&fakeOAuthStore{}, disc, "/", auth.Admission{Allowlist: auth.ParseOperatorAllowlist("")}, auth.EdgeConfig{}, nil)

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
	o := auth.NewOAuth(store, disc, "/", auth.Admission{Allowlist: auth.ParseOperatorAllowlist("42, 77 99")}, auth.EdgeConfig{}, nil)

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
	o := auth.NewOAuth(store, &fakeDiscord{}, "/", auth.Admission{Allowlist: auth.ParseOperatorAllowlist("")}, auth.EdgeConfig{}, nil)

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
	o := auth.NewOAuth(store, disc, "/", auth.Admission{Allowlist: auth.ParseOperatorAllowlist("42, 77")}, auth.EdgeConfig{}, nil)

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

// TestOAuthCallback_SecureFromConfigNotRequest pins the production shape of
// ADR-0016's Secure attribute (#662): it is fixed at boot from the configured
// public URL ([auth.SecureCookies]), NOT read off each request. A request
// claiming `X-Forwarded-Proto: https` must not upgrade a cleartext deployment,
// and — the dangerous direction — a request WITHOUT the header must not
// downgrade an https deployment's session cookie to non-Secure.
func TestOAuthCallback_SecureFromConfigNotRequest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		redirectURL string
		forwarded   string
		want        bool
	}{
		{"https deployment, proxy header absent", "https://glyphoxa.example/auth/discord/callback", "", true},
		{"https deployment, proxy header says http", "https://glyphoxa.example/auth/discord/callback", "http", true},
		{"cleartext dev, client claims https", "http://localhost:8080/auth/discord/callback", "https", false},
		{"unset redirect URL (dev mode)", "", "https", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			disc := &fakeDiscord{user: auth.DiscordUser{ID: "77", Username: "sora"}}
			store := &fakeOAuthStore{userID: uuid.New(), tenantID: uuid.New()}
			o := auth.NewOAuth(store, disc, "/", auth.Admission{Allowlist: auth.ParseOperatorAllowlist("77")},
				auth.EdgeConfig{SecureCookies: auth.SecureCookies(tc.redirectURL)}, nil)

			// Login issues the state cookie with the same attribute.
			loginReq := httptest.NewRequest(http.MethodGet, "/auth/discord/login", nil)
			if tc.forwarded != "" {
				loginReq.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			loginRec := httptest.NewRecorder()
			o.Login(loginRec, loginReq)
			for _, c := range loginRec.Result().Cookies() {
				if c.Secure != tc.want {
					t.Errorf("login cookie %s Secure=%v, want %v", c.Name, c.Secure, tc.want)
				}
			}

			form := url.Values{"code": {"the-code"}, "state": {"st-1"}}
			req := httptest.NewRequest(http.MethodGet, "/auth/discord/callback?"+form.Encode(), nil)
			req.AddCookie(&http.Cookie{Name: "glyphoxa_oauth_state", Value: "st-1"})
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			rec := httptest.NewRecorder()
			o.Callback(rec, req)
			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
			}
			cookies := rec.Result().Cookies()
			if len(cookies) == 0 {
				t.Fatal("no cookies issued")
			}
			for _, c := range cookies {
				if c.Secure != tc.want {
					t.Errorf("cookie %s Secure=%v, want %v", c.Name, c.Secure, tc.want)
				}
			}
		})
	}
}

// TestOAuthCallback_SessionAuditIP pins which address the session row records
// (#662). The FIRST X-Forwarded-For entry is whatever the client typed —
// proxies append — so it must never be the answer. A client-IP header is
// believed only when the operator configured it as trusted
// (GLYPHOXA_TRUSTED_CLIENT_IP_HEADER): unconfigured, a client-sent
// CF-Connecting-IP is just another forgeable header and is ignored.
func TestOAuthCallback_SessionAuditIP(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		trusted string
		header  http.Header
		want    string
	}{
		{"direct: socket peer, port stripped", "", http.Header{}, "192.0.2.1"},
		{
			"spoofed first hop is ignored; the proxy-appended last hop is recorded", "",
			http.Header{"X-Forwarded-For": {"6.6.6.6, 203.0.113.7"}},
			"203.0.113.7",
		},
		{
			"last hop across repeated X-Forwarded-For header lines", "",
			http.Header{"X-Forwarded-For": {"6.6.6.6", "198.51.100.4, 203.0.113.9"}},
			"203.0.113.9",
		},
		{
			"no trusted header configured: a client-sent CF-Connecting-IP is NOT believed", "",
			http.Header{"Cf-Connecting-Ip": {"6.6.6.6"}, "X-Forwarded-For": {"7.7.7.7, 10.42.0.1"}},
			"10.42.0.1",
		},
		{
			"configured trusted header wins over X-Forwarded-For", "CF-Connecting-IP",
			http.Header{"Cf-Connecting-Ip": {"198.51.100.23"}, "X-Forwarded-For": {"6.6.6.6, 10.42.0.1"}},
			"198.51.100.23",
		},
		{
			"configured trusted header absent: fall back to the last hop", "CF-Connecting-IP",
			http.Header{"X-Forwarded-For": {"6.6.6.6, 10.42.0.1"}},
			"10.42.0.1",
		},
		{
			"another proxy's header can be named", "X-Real-IP",
			http.Header{"X-Real-Ip": {"198.51.100.77"}, "Cf-Connecting-Ip": {"6.6.6.6"}},
			"198.51.100.77",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			disc := &fakeDiscord{user: auth.DiscordUser{ID: "77", Username: "sora"}}
			store := &fakeOAuthStore{userID: uuid.New(), tenantID: uuid.New()}
			o := auth.NewOAuth(store, disc, "/", auth.Admission{Allowlist: auth.ParseOperatorAllowlist("77")},
				auth.EdgeConfig{ClientIPHeader: tc.trusted}, nil)

			form := url.Values{"code": {"the-code"}, "state": {"st-1"}}
			req := httptest.NewRequest(http.MethodGet, "/auth/discord/callback?"+form.Encode(), nil)
			req.RemoteAddr = "192.0.2.1:1234"
			req.AddCookie(&http.Cookie{Name: "glyphoxa_oauth_state", Value: "st-1"})
			for k, vs := range tc.header {
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}
			rec := httptest.NewRecorder()
			o.Callback(rec, req)
			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
			}
			if store.created.IP != tc.want {
				t.Errorf("session IP = %q, want %q", store.created.IP, tc.want)
			}
		})
	}
}

// countingBody counts the request-body bytes a handler pulls, so a test can
// assert an unauthenticated body was refused unread.
type countingBody struct {
	r io.Reader
	n int
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// The OAuth carve-out is reachable without a session or CSRF token, so it must
// never parse a request body: a multipart POST to the callback is refused with
// 405 before a single body byte is read (no in-memory form, no temp-file
// spill).
func TestOAuthCallback_PostMultipart_RefusedUnread(t *testing.T) {
	t.Parallel()
	disc := &fakeDiscord{}
	o := auth.NewOAuth(&fakeOAuthStore{}, disc, "/", auth.Admission{Allowlist: auth.ParseOperatorAllowlist("")}, auth.EdgeConfig{}, nil)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("state", "st-1")
	_ = mw.WriteField("code", "the-code")
	part, err := mw.CreateFormFile("blob", "blob.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(make([]byte, 1<<20)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	body := &countingBody{r: &buf}
	req := httptest.NewRequest(http.MethodPost, "/auth/discord/callback", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: "glyphoxa_oauth_state", Value: "st-1"})

	rec := httptest.NewRecorder()
	o.Callback(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", got, "GET, HEAD")
	}
	if got := rec.Header().Get("Connection"); got != "close" {
		t.Errorf("Connection = %q, want close (do not drain the refused body)", got)
	}
	if body.n != 0 {
		t.Errorf("read %d body bytes, want 0", body.n)
	}
	if req.MultipartForm != nil || req.PostForm != nil {
		t.Error("request body was parsed as a form")
	}
	if disc.gotCode != "" {
		t.Errorf("exchanged code %q from a POST body", disc.gotCode)
	}
}

// `state` and `code` are honoured from the query string only: a urlencoded
// POST body carrying a state that matches the cookie must not reach the
// Discord exchange or create a session.
func TestOAuthCallback_PostBodyParams_NotHonoured(t *testing.T) {
	t.Parallel()
	disc := &fakeDiscord{user: auth.DiscordUser{ID: "77"}}
	store := &fakeOAuthStore{userID: uuid.New(), tenantID: uuid.New()}
	o := auth.NewOAuth(store, disc, "/", auth.Admission{Allowlist: auth.ParseOperatorAllowlist("77")}, auth.EdgeConfig{}, nil)

	form := url.Values{"code": {"the-code"}, "state": {"st-1"}}
	req := httptest.NewRequest(http.MethodPost, "/auth/discord/callback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "glyphoxa_oauth_state", Value: "st-1"})

	rec := httptest.NewRecorder()
	o.Callback(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body=%s", rec.Code, rec.Body.String())
	}
	if disc.gotCode != "" {
		t.Errorf("exchanged code %q from a POST body", disc.gotCode)
	}
	if store.upsertCalls != 0 || store.createCalls != 0 {
		t.Errorf("store written on a refused POST: upsert=%d create=%d", store.upsertCalls, store.createCalls)
	}
	if got := rec.Result().Cookies(); len(got) != 0 {
		t.Errorf("cookies set on a refused POST: %v", got)
	}
}

// Login is GET-only too: a POST neither mints a state cookie nor redirects to
// Discord.
func TestOAuthLogin_Post_Refused(t *testing.T) {
	t.Parallel()
	o := auth.NewOAuth(&fakeOAuthStore{}, &fakeDiscord{authBase: "https://discord/authorize"}, "/", auth.Admission{Allowlist: auth.ParseOperatorAllowlist("")}, auth.EdgeConfig{}, nil)

	rec := httptest.NewRecorder()
	o.Login(rec, httptest.NewRequest(http.MethodPost, "/auth/discord/login", strings.NewReader("x=1")))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", got, "GET, HEAD")
	}
	if got := rec.Result().Cookies(); len(got) != 0 {
		t.Errorf("cookies set on a refused POST: %v", got)
	}
}
