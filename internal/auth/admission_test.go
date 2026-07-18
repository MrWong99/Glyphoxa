package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

func TestParseAdmissionMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    auth.AdmissionMode
		wantErr bool
	}{
		{"allowlist", auth.AdmissionAllowlist, false},
		{"open", auth.AdmissionOpen, false},
		{"  Open \n", auth.AdmissionOpen, false},
		{"ALLOWLIST", auth.AdmissionAllowlist, false},
		{"", "", true},
		{"opeen", "", true},
		{"closed", "", true},
	}
	for _, c := range cases {
		got, err := auth.ParseAdmissionMode(c.in)
		if c.wantErr != (err != nil) || got != c.want {
			t.Errorf("ParseAdmissionMode(%q) = %q, %v; want %q, err=%v", c.in, got, err, c.want, c.wantErr)
		}
	}
}

// fakeSignup is a scripted SignupProvisioner recording the params it was
// handed.
type fakeSignup struct {
	got     storage.SignupParams
	calls   int
	created bool
	err     error
}

func (f *fakeSignup) ProvisionSignup(_ context.Context, p storage.SignupParams) (storage.SignupResult, error) {
	f.calls++
	f.got = p
	if f.err != nil {
		return storage.SignupResult{}, f.err
	}
	return storage.SignupResult{
		User:    storage.User{ID: uuid.New(), DiscordUserID: p.User.DiscordUserID},
		Tenant:  storage.Tenant{ID: uuid.New(), Name: p.TenantName},
		Session: storage.Session{Token: p.Session.Token},
		Created: f.created,
	}, nil
}

// signupCallback drives a valid state+code callback through an OAuth built
// with the given admission policy and returns the recorder.
func signupCallback(t *testing.T, adm auth.Admission, store *fakeOAuthStore, du auth.DiscordUser) *httptest.ResponseRecorder {
	t.Helper()
	disc := &fakeDiscord{user: du}
	o := auth.NewOAuth(store, disc, "/", adm, nil)
	form := url.Values{"code": {"the-code"}, "state": {"st-1"}}
	req := httptest.NewRequest(http.MethodGet, "/auth/discord/callback?"+form.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "glyphoxa_oauth_state", Value: "st-1"})
	rec := httptest.NewRecorder()
	o.Callback(rec, req)
	return rec
}

// A stranger completing OAuth in open mode is admitted via the create-only
// signup transaction (ADR-0055): identity + default plan slug reach the
// provisioner, the minted token becomes the session cookie, and a FRESH
// founder lands on the onboarding step.
func TestOAuthCallback_OpenMode_SignupProvisions(t *testing.T) {
	t.Parallel()
	sp := &fakeSignup{created: true}
	store := &fakeOAuthStore{}
	adm := auth.Admission{
		Mode:           auth.AdmissionOpen,
		Allowlist:      auth.ParseOperatorAllowlist("42"),
		SignupPlanSlug: "byok-free",
		Signup:         sp,
	}
	du := auth.DiscordUser{ID: "999", Username: "rin", GlobalName: "Rin Okabe", AvatarURL: "https://cdn/r.png"}
	rec := signupCallback(t, adm, store, du)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/onboarding/create-tenant" {
		t.Errorf("Location = %q, want /onboarding/create-tenant", loc)
	}
	if sp.calls != 1 {
		t.Fatalf("ProvisionSignup calls = %d, want 1", sp.calls)
	}
	if sp.got.User.DiscordUserID != "999" || sp.got.User.Name != "Rin Okabe" {
		t.Errorf("provisioned user = %+v", sp.got.User)
	}
	if sp.got.PlanSlug != "byok-free" {
		t.Errorf("plan slug = %q, want byok-free", sp.got.PlanSlug)
	}
	if sp.got.TenantName == "" {
		t.Error("tenant name must be pre-filled for the onboarding rename")
	}
	// The legacy claim-or-create path must NOT run for a signup.
	if store.upsertCalls != 0 || store.resolveN != 0 || store.createCalls != 0 {
		t.Errorf("allowlist login path ran during signup: %+v", store)
	}

	cookies := map[string]*http.Cookie{}
	for _, c := range rec.Result().Cookies() {
		cookies[c.Name] = c
	}
	sess := cookies["glyphoxa_session"]
	if sess == nil || sess.Value != sp.got.Session.Token {
		t.Fatalf("session cookie = %+v, want the provisioned token", sess)
	}
	if cookies["glyphoxa_csrf"] == nil {
		t.Fatal("csrf cookie missing after signup")
	}
}

// A RETURNING open-mode signup (Created=false) skips onboarding and goes
// straight to the app.
func TestOAuthCallback_OpenMode_ReturningSignupSkipsOnboarding(t *testing.T) {
	t.Parallel()
	sp := &fakeSignup{created: false}
	adm := auth.Admission{Mode: auth.AdmissionOpen, SignupPlanSlug: "byok-free", Signup: sp}
	rec := signupCallback(t, adm, &fakeOAuthStore{}, auth.DiscordUser{ID: "999", Username: "rin"})

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("status/Location = %d %q, want 302 /", rec.Code, rec.Header().Get("Location"))
	}
}

// An ALLOWLISTED User keeps the claim-or-create login path in open mode too
// (ADR-0055): no signup transaction, the operator claims/keeps the seed
// Tenant.
func TestOAuthCallback_OpenMode_AllowlistedKeepsClaimOrCreate(t *testing.T) {
	t.Parallel()
	sp := &fakeSignup{}
	store := &fakeOAuthStore{userID: uuid.New(), tenantID: uuid.New()}
	adm := auth.Admission{
		Mode:           auth.AdmissionOpen,
		Allowlist:      auth.ParseOperatorAllowlist("77"),
		SignupPlanSlug: "byok-free",
		Signup:         sp,
	}
	rec := signupCallback(t, adm, store, auth.DiscordUser{ID: "77", Username: "sora"})

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("status/Location = %d %q, want 302 /", rec.Code, rec.Header().Get("Location"))
	}
	if sp.calls != 0 {
		t.Errorf("ProvisionSignup ran for an allowlisted login")
	}
	if store.resolveN != 1 {
		t.Errorf("ResolveOperatorTenant calls = %d, want 1", store.resolveN)
	}
}

// A suspended user (ADR-0055 open-mode revocation) re-completing OAuth is
// bounced with the same non-leaky signal as an allowlist rejection — no
// session cookies.
func TestOAuthCallback_OpenMode_SuspendedRejected(t *testing.T) {
	t.Parallel()
	sp := &fakeSignup{err: storage.ErrUserSuspended}
	adm := auth.Admission{Mode: auth.AdmissionOpen, SignupPlanSlug: "byok-free", Signup: sp}
	rec := signupCallback(t, adm, &fakeOAuthStore{}, auth.DiscordUser{ID: "999", Username: "rin"})

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?error=not_authorized" {
		t.Fatalf("status/Location = %d %q, want 302 not_authorized", rec.Code, rec.Header().Get("Location"))
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "glyphoxa_session" || c.Name == "glyphoxa_csrf" {
			t.Errorf("issued %s cookie to a suspended user", c.Name)
		}
	}
}

// A provisioning failure (e.g. the default plan vanished at runtime) is an
// internal error, not a silent half-login.
func TestOAuthCallback_OpenMode_ProvisionFailure(t *testing.T) {
	t.Parallel()
	sp := &fakeSignup{err: errors.New("boom")}
	adm := auth.Admission{Mode: auth.AdmissionOpen, SignupPlanSlug: "byok-free", Signup: sp}
	rec := signupCallback(t, adm, &fakeOAuthStore{}, auth.DiscordUser{ID: "999", Username: "rin"})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "glyphoxa_session" || c.Name == "glyphoxa_csrf" {
			t.Errorf("issued %s cookie on a failed signup", c.Name)
		}
	}
}

// Open mode without a provisioner is a misconfiguration that FAILS CLOSED to
// allowlist posture: strangers are rejected, not admitted unprovisioned.
func TestOAuthCallback_OpenModeMisconfigured_FailsClosed(t *testing.T) {
	t.Parallel()
	store := &fakeOAuthStore{}
	adm := auth.Admission{Mode: auth.AdmissionOpen, Allowlist: auth.ParseOperatorAllowlist("42")}
	rec := signupCallback(t, adm, store, auth.DiscordUser{ID: "999", Username: "rin"})

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?error=not_authorized" {
		t.Fatalf("status/Location = %d %q, want 302 not_authorized", rec.Code, rec.Header().Get("Location"))
	}
	if store.upsertCalls != 0 || store.createCalls != 0 {
		t.Errorf("store written for a fail-closed rejection: %+v", store)
	}
}
