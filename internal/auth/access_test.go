package auth_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Web-managed instance access at the auth tier (ADR-0065): Platform Admin
// identity, the join cookie, the callback's admission walk, the public
// invitation preview, and the two GetCurrentUser fields the SPA gates on.

func TestPlatformAdmins(t *testing.T) {
	t.Parallel()
	listed := storage.User{DiscordUserID: "42"}
	stranger := storage.User{DiscordUserID: "999"}
	dev := storage.User{DiscordUserID: storage.DevOperatorDiscordID}

	var zero auth.PlatformAdmins
	for _, u := range []storage.User{listed, stranger, dev, {}} {
		if zero.Is(u) {
			t.Errorf("zero PlatformAdmins recognises %q — it must recognise nobody", u.DiscordUserID)
		}
	}

	prod := auth.PlatformAdmins{Allowlist: auth.ParseOperatorAllowlist("42, 77")}
	if !prod.Is(listed) || !prod.IsDiscordUser("77") {
		t.Error("a Discord User on GLYPHOXA_OPERATOR_IDS is not a Platform Admin")
	}
	if prod.Is(stranger) || prod.IsDiscordUser("") {
		t.Error("a Discord User off the list is a Platform Admin")
	}
	// A leftover dev users row in a production database must not be an admin.
	if prod.Is(dev) {
		t.Error("the dev operator is a Platform Admin outside dev mode")
	}

	devMode := auth.PlatformAdmins{Dev: true}
	if !devMode.Is(dev) {
		t.Error("the dev operator is not a Platform Admin in dev mode")
	}
	if devMode.Is(stranger) {
		t.Error("dev mode made a stranger a Platform Admin")
	}
}

// fakeAccess is a scripted AccessStore that records the order of its calls, so
// a test can assert not only who was admitted but what was (not) consulted.
type fakeAccess struct {
	grants map[string]storage.AccessGrant
	// links maps a token to the grant its redemption creates; a token absent
	// from it is ErrInvitationInvalid, like every dead link.
	links     map[string]storage.AccessGrant
	suspended map[string]bool
	grantErr  error
	redeemErr error
	calls     []string
}

func (f *fakeAccess) GetAccessGrant(_ context.Context, id string) (storage.AccessGrant, error) {
	f.calls = append(f.calls, "grant:"+id)
	if f.grantErr != nil {
		return storage.AccessGrant{}, f.grantErr
	}
	g, ok := f.grants[id]
	if !ok {
		return storage.AccessGrant{}, storage.ErrNotFound
	}
	return g, nil
}

func (f *fakeAccess) RedeemAccessInvitation(_ context.Context, token, id string) (storage.AccessGrant, error) {
	f.calls = append(f.calls, "redeem:"+token+":"+id)
	if f.redeemErr != nil {
		return storage.AccessGrant{}, f.redeemErr
	}
	g, ok := f.links[token]
	if !ok {
		return storage.AccessGrant{}, storage.ErrInvitationInvalid
	}
	return g, nil
}

func (f *fakeAccess) GetUserByDiscordID(_ context.Context, id string) (storage.User, error) {
	f.calls = append(f.calls, "user:"+id)
	if f.suspended[id] {
		now := time.Now()
		return storage.User{DiscordUserID: id, SuspendedAt: &now}, nil
	}
	return storage.User{}, storage.ErrNotFound
}

func (f *fakeAccess) redeemed() bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, "redeem:") {
			return true
		}
	}
	return false
}

// accessRound is one OAuth callback: who Discord says the visitor is, and the
// two cookies the Login start may have set.
type accessRound struct {
	du   auth.DiscordUser
	aup  bool   // the state cookie carries the acknowledgment
	join string // the join cookie's token, "" for an ordinary login
}

func (a accessRound) run(t *testing.T, adm auth.Admission, store *fakeOAuthStore) *httptest.ResponseRecorder {
	t.Helper()
	o := auth.NewOAuth(store, &fakeDiscord{user: a.du}, "/", adm, auth.EdgeConfig{}, nil)
	form := url.Values{"code": {"the-code"}, "state": {"st-1"}}
	req := httptest.NewRequest(http.MethodGet, "/auth/discord/callback?"+form.Encode(), nil)
	state := "st-1"
	if a.aup {
		state += ".aup"
	}
	req.AddCookie(&http.Cookie{Name: "glyphoxa_oauth_state", Value: state})
	if a.join != "" {
		req.AddCookie(&http.Cookie{Name: "glyphoxa_join", Value: a.join})
	}
	rec := httptest.NewRecorder()
	o.Callback(rec, req)
	return rec
}

func respCookies(rec *httptest.ResponseRecorder) map[string]*http.Cookie {
	out := map[string]*http.Cookie{}
	for _, c := range rec.Result().Cookies() {
		out[c.Name] = c
	}
	return out
}

func assertNoSession(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	c := respCookies(rec)
	if c["glyphoxa_session"] != nil || c["glyphoxa_csrf"] != nil {
		t.Error("a session was issued to a rejected visitor")
	}
}

func assertJoinCookieCleared(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	c := respCookies(rec)["glyphoxa_join"]
	if c == nil || c.MaxAge >= 0 || c.Value != "" || c.Path != "/auth/discord" {
		t.Errorf("join cookie after the callback = %+v, want it cleared on its own path", c)
	}
}

// allowlistAdmission is a fully wired allowlist-mode policy over the fakes.
func allowlistAdmission(sp *fakeSignup, ac *fakeAccess) auth.Admission {
	return auth.Admission{
		Mode:      auth.AdmissionAllowlist,
		Allowlist: auth.ParseOperatorAllowlist("42"),
		Signup:    sp,
		Access:    ac,
	}
}

var granted = auth.DiscordUser{ID: "555", Username: "rin", GlobalName: "Rin Okabe", AvatarURL: "https://cdn/r.png"}

// A join start stashes the token in its own short-lived HttpOnly cookie — and
// nowhere in what travels to Discord.
func TestOAuthLogin_JoinStashesTokenInItsOwnCookie(t *testing.T) {
	t.Parallel()
	const token = "join-token_for-test-01"
	for _, mode := range []auth.AdmissionMode{auth.AdmissionAllowlist, auth.AdmissionOpen} {
		for _, secure := range []bool{false, true} {
			now := time.Now()
			o := auth.NewOAuth(&fakeOAuthStore{}, &fakeDiscord{authBase: "https://discord/authorize"}, "/",
				auth.Admission{Mode: mode}, auth.EdgeConfig{SecureCookies: secure}, nil)
			rec := httptest.NewRecorder()
			o.Login(rec, httptest.NewRequest(http.MethodGet, "/auth/discord/login?join="+token+"&aup=1", nil))

			loc := rec.Header().Get("Location")
			if rec.Code != http.StatusFound || !strings.HasPrefix(loc, "https://discord/authorize?state=") {
				t.Fatalf("%s: status = %d, Location = %q; want the Discord redirect", mode, rec.Code, loc)
			}
			if strings.Contains(loc, token) {
				t.Errorf("%s: the invitation token leaked into the authorize URL: %q", mode, loc)
			}
			cookies := respCookies(rec)
			state := cookies["glyphoxa_oauth_state"]
			if state == nil || strings.Contains(state.Value, token) {
				t.Fatalf("%s: state cookie = %+v, must not carry the invitation token", mode, state)
			}
			if !strings.HasSuffix(state.Value, ".aup") {
				t.Errorf("%s: state cookie %q lacks the acknowledgment suffix", mode, state.Value)
			}
			join := cookies["glyphoxa_join"]
			if join == nil {
				t.Fatalf("%s: no join cookie set", mode)
			}
			if join.Value != token || !join.HttpOnly || join.SameSite != http.SameSiteLaxMode ||
				join.Secure != secure || join.Path != "/auth/discord" {
				t.Errorf("%s: join cookie = %+v", mode, join)
			}
			if ttl := join.Expires.Sub(now); ttl < 9*time.Minute || ttl > 11*time.Minute {
				t.Errorf("%s: join cookie lives %v, want about 10 minutes", mode, ttl)
			}
		}
	}
}

// A join REQUIRES the acknowledgment in every Admission Mode, and bounces back
// to the join page it came from rather than the login screen.
func TestOAuthLogin_JoinRequiresAcknowledgment(t *testing.T) {
	t.Parallel()
	for _, mode := range []auth.AdmissionMode{auth.AdmissionAllowlist, auth.AdmissionOpen} {
		for _, q := range []string{"join=tok-1", "join=tok-1&aup=0", "join=tok-1&aup=true"} {
			o := auth.NewOAuth(&fakeOAuthStore{}, &fakeDiscord{authBase: "https://discord/authorize"}, "/",
				auth.Admission{Mode: mode}, auth.EdgeConfig{}, nil)
			rec := httptest.NewRecorder()
			o.Login(rec, httptest.NewRequest(http.MethodGet, "/auth/discord/login?"+q, nil))
			if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/join/tok-1?error=aup_required" {
				t.Errorf("%s ?%s: status = %d, Location = %q; want /join/tok-1?error=aup_required", mode, q, rec.Code, loc)
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Errorf("%s ?%s: cookies set on a refused join start: %v", mode, q, rec.Result().Cookies())
			}
		}
	}
}

// A token that is not token-shaped is never echoed into a redirect or a cookie.
func TestOAuthLogin_MalformedJoinTokenRefused(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"../../evil", "a b", "tok/../x", "tok?x=1", "tok%0d%0aSet-Cookie:x", strings.Repeat("a", 129)} {
		o := auth.NewOAuth(&fakeOAuthStore{}, &fakeDiscord{authBase: "https://discord/authorize"}, "/",
			auth.Admission{}, auth.EdgeConfig{}, nil)
		rec := httptest.NewRecorder()
		q := url.Values{"join": {raw}, "aup": {"1"}}
		o.Login(rec, httptest.NewRequest(http.MethodGet, "/auth/discord/login?"+q.Encode(), nil))
		if loc := rec.Header().Get("Location"); loc != "/login?error=not_authorized" {
			t.Errorf("join=%q: Location = %q, want /login?error=not_authorized", raw, loc)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Errorf("join=%q: cookies set: %v", raw, rec.Result().Cookies())
		}
	}
}

// An ordinary login drops a join cookie left over from an abandoned join, so
// the next person to sign in on that browser does not redeem someone's link.
func TestOAuthLogin_PlainLoginDropsStaleJoinCookie(t *testing.T) {
	t.Parallel()
	o := auth.NewOAuth(&fakeOAuthStore{}, &fakeDiscord{authBase: "https://discord/authorize"}, "/",
		auth.Admission{}, auth.EdgeConfig{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/auth/discord/login", nil)
	req.AddCookie(&http.Cookie{Name: "glyphoxa_join", Value: "stale-token"})
	rec := httptest.NewRecorder()
	o.Login(rec, req)
	assertJoinCookieCleared(t, rec)

	// Without one there is nothing to clear, and the allowlist-mode start
	// stays exactly what it was: one state cookie.
	rec = httptest.NewRecorder()
	o.Login(rec, httptest.NewRequest(http.MethodGet, "/auth/discord/login", nil))
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].Name != "glyphoxa_oauth_state" {
		t.Errorf("plain allowlist login set cookies %v, want only the state cookie", c)
	}
}

// (a) The holder of an Access Grant is admitted on an allowlist deployment:
// the provisioning transaction runs with NO Plan and the grant's Platform Keys
// choice; a fresh founder lands on onboarding.
func TestOAuthCallback_Allowlist_AccessGrantAdmits(t *testing.T) {
	t.Parallel()
	for _, keys := range []bool{false, true} {
		sp := &fakeSignup{created: true}
		ac := &fakeAccess{grants: map[string]storage.AccessGrant{"555": {DiscordUserID: "555", PlatformKeys: keys}}}
		store := &fakeOAuthStore{}
		rec := accessRound{du: granted}.run(t, allowlistAdmission(sp, ac), store)

		if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/onboarding/create-tenant" {
			t.Fatalf("keys=%v: status = %d, Location = %q; want the onboarding step", keys, rec.Code, loc)
		}
		if sp.calls != 1 {
			t.Fatalf("keys=%v: ProvisionSignup calls = %d, want 1", keys, sp.calls)
		}
		if sp.got.PlanSlug != "" {
			t.Errorf("keys=%v: plan slug = %q, want none (an allowlist deployment has no Plans)", keys, sp.got.PlanSlug)
		}
		if sp.got.PlatformKeys == nil || *sp.got.PlatformKeys != keys {
			t.Errorf("keys=%v: PlatformKeys = %v, want the grant's value", keys, sp.got.PlatformKeys)
		}
		if sp.got.User.DiscordUserID != "555" || sp.got.User.Name != "Rin Okabe" || sp.got.TenantName == "" {
			t.Errorf("keys=%v: provisioned %+v / tenant %q", keys, sp.got.User, sp.got.TenantName)
		}
		// An ordinary allowlist login asks for no acknowledgment, so none is
		// recorded — and none is invented.
		if !sp.got.AUPAcceptedAt.IsZero() {
			t.Errorf("keys=%v: AUPAcceptedAt stamped without an acknowledgment", keys)
		}
		// Create-only: the Platform Admin's claim-or-create path must not run.
		if store.upsertCalls != 0 || store.resolveN != 0 || store.createCalls != 0 {
			t.Errorf("keys=%v: the claim-or-create path ran for a grant holder: %+v", keys, store)
		}
		if ac.redeemed() {
			t.Errorf("keys=%v: a grant holder triggered a redemption: %v", keys, ac.calls)
		}
		c := respCookies(rec)
		if c["glyphoxa_session"] == nil || c["glyphoxa_session"].Value != sp.got.Session.Token || c["glyphoxa_csrf"] == nil {
			t.Errorf("keys=%v: session/csrf cookies = %v", keys, c)
		}
	}
}

// A returning grant holder goes straight to the app, and an acknowledgment
// carried by the round trip is recorded.
func TestOAuthCallback_Allowlist_ReturningGrantHolder(t *testing.T) {
	t.Parallel()
	sp := &fakeSignup{created: false}
	ac := &fakeAccess{grants: map[string]storage.AccessGrant{"555": {DiscordUserID: "555"}}}
	rec := accessRound{du: granted, aup: true}.run(t, allowlistAdmission(sp, ac), &fakeOAuthStore{})
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want / for a returning grant holder", loc)
	}
	if sp.got.AUPAcceptedAt.IsZero() {
		t.Error("an acknowledged round trip did not record the acceptance time")
	}
}

// (b) A join cookie whose token redeems as an ACCESS invitation admits, with
// the Platform Keys choice the invitation carried and the acknowledgment
// recorded; the join cookie is cleared.
func TestOAuthCallback_Allowlist_AccessInvitationAdmits(t *testing.T) {
	t.Parallel()
	sp := &fakeSignup{created: true}
	ac := &fakeAccess{links: map[string]storage.AccessGrant{"good-link": {DiscordUserID: "555", PlatformKeys: true}}}
	rec := accessRound{du: granted, aup: true, join: "good-link"}.run(t, allowlistAdmission(sp, ac), &fakeOAuthStore{})

	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/onboarding/create-tenant" {
		t.Fatalf("status = %d, Location = %q; want the onboarding step", rec.Code, loc)
	}
	want := []string{"grant:555", "user:555", "redeem:good-link:555"}
	if strings.Join(ac.calls, " ") != strings.Join(want, " ") {
		t.Errorf("access store calls = %v, want %v (grant first, redemption last)", ac.calls, want)
	}
	if sp.calls != 1 || sp.got.PlanSlug != "" || sp.got.PlatformKeys == nil || !*sp.got.PlatformKeys {
		t.Errorf("provisioned with %+v, want no Plan and the invitation's Platform Keys", sp.got)
	}
	if sp.got.AUPAcceptedAt.IsZero() {
		t.Error("the join's acknowledgment was not recorded")
	}
	if respCookies(rec)["glyphoxa_session"] == nil {
		t.Error("no session cookie after a successful join")
	}
	assertJoinCookieCleared(t, rec)
}

// (c) Everyone else is rejected as before — and when they came from a link
// that did not admit them, they go back to that link's join page.
func TestOAuthCallback_Allowlist_Rejections(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		round    accessRound
		access   *fakeAccess
		wantLoc  string
		noRedeem bool
	}{
		"no grant, no link": {
			round: accessRound{du: granted}, access: &fakeAccess{},
			wantLoc: "/login?error=not_authorized", noRedeem: true,
		},
		"dead link": {
			round: accessRound{du: granted, aup: true, join: "dead-link"}, access: &fakeAccess{},
			wantLoc: "/join/dead-link?error=invalid",
		},
		// A CAMPAIGN invitation never redeems as an ACCESS one. This policy has
		// no Guests store wired, so the token is simply a join that failed
		// (TestOAuthCallback_CampaignJoin_* cover the wired case).
		"campaign link": {
			round: accessRound{du: granted, aup: true, join: "campaign-link"}, access: &fakeAccess{},
			wantLoc: "/join/campaign-link?error=invalid",
		},
		// A hand-crafted round trip: join cookie present, no acknowledgment in
		// the state cookie. Refused BEFORE the redemption, so no use is spent.
		"link without acknowledgment": {
			round:   accessRound{du: granted, join: "good-link"},
			access:  &fakeAccess{links: map[string]storage.AccessGrant{"good-link": {}}},
			wantLoc: "/join/good-link?error=aup_required", noRedeem: true,
		},
		// A suspended user must not spend a use of the link either.
		"suspended redeemer": {
			round: accessRound{du: granted, aup: true, join: "good-link"},
			access: &fakeAccess{
				links:     map[string]storage.AccessGrant{"good-link": {}},
				suspended: map[string]bool{"555": true},
			},
			wantLoc: "/login?error=not_authorized", noRedeem: true,
		},
	}
	for name, tc := range cases {
		sp := &fakeSignup{created: true}
		store := &fakeOAuthStore{}
		rec := tc.round.run(t, allowlistAdmission(sp, tc.access), store)

		if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != tc.wantLoc {
			t.Errorf("%s: status = %d, Location = %q; want %q", name, rec.Code, loc, tc.wantLoc)
		}
		if sp.calls != 0 || store.upsertCalls != 0 || store.resolveN != 0 || store.createCalls != 0 {
			t.Errorf("%s: a rejected visitor caused writes (signup=%d, store=%+v)", name, sp.calls, store)
		}
		if tc.noRedeem && tc.access.redeemed() {
			t.Errorf("%s: a redemption was attempted: %v", name, tc.access.calls)
		}
		assertNoSession(t, rec)
		if tc.round.join != "" {
			assertJoinCookieCleared(t, rec)
		}
	}
}

// A suspended grant holder is refused at the callback exactly like a suspended
// signup: the provisioning transaction reports it and nothing is minted.
func TestOAuthCallback_Allowlist_SuspendedGrantHolderRejected(t *testing.T) {
	t.Parallel()
	sp := &fakeSignup{err: storage.ErrUserSuspended}
	ac := &fakeAccess{grants: map[string]storage.AccessGrant{"555": {DiscordUserID: "555"}}}
	rec := accessRound{du: granted}.run(t, allowlistAdmission(sp, ac), &fakeOAuthStore{})
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/login?error=not_authorized" {
		t.Errorf("status = %d, Location = %q; want the not_authorized bounce", rec.Code, loc)
	}
	assertNoSession(t, rec)
}

// A store failure while deciding admits nobody and answers 500 — never a
// redirect that reads as "you are not invited".
func TestOAuthCallback_Allowlist_StoreFailureAdmitsNobody(t *testing.T) {
	t.Parallel()
	boom := errors.New("db down")
	for name, ac := range map[string]*fakeAccess{
		"grant read": {grantErr: boom},
		"redemption": {redeemErr: boom},
	} {
		sp := &fakeSignup{created: true}
		rec := accessRound{du: granted, aup: true, join: "good-link"}.run(t, allowlistAdmission(sp, ac), &fakeOAuthStore{})
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500", name, rec.Code)
		}
		if sp.calls != 0 {
			t.Errorf("%s: provisioned despite the failure", name)
		}
		assertNoSession(t, rec)
		assertJoinCookieCleared(t, rec)
	}
}

// A Platform Admin who happens to carry a join cookie just logs in: the
// claim-or-create path, no grant lookup, no redemption — and the cookie goes.
func TestOAuthCallback_PlatformAdminIgnoresJoinCookie(t *testing.T) {
	t.Parallel()
	for _, mode := range []auth.AdmissionMode{auth.AdmissionAllowlist, auth.AdmissionOpen} {
		sp := &fakeSignup{created: true}
		ac := &fakeAccess{links: map[string]storage.AccessGrant{"good-link": {}}}
		store := &fakeOAuthStore{userID: uuid.New(), tenantID: uuid.New()}
		adm := allowlistAdmission(sp, ac)
		adm.Mode = mode
		adm.SignupPlanSlug = "byok-free"
		rec := accessRound{du: auth.DiscordUser{ID: "42", Username: "ada"}, aup: true, join: "good-link"}.run(t, adm, store)

		if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/" {
			t.Fatalf("%s: status = %d, Location = %q; want the app", mode, rec.Code, loc)
		}
		if store.upsertCalls != 1 || store.resolveN != 1 || store.createCalls != 1 {
			t.Errorf("%s: claim-or-create path = %+v, want one call each", mode, store)
		}
		if sp.calls != 0 || len(ac.calls) != 0 {
			t.Errorf("%s: a Platform Admin touched the signup/access stores (signup=%d, access=%v)", mode, sp.calls, ac.calls)
		}
		assertJoinCookieCleared(t, rec)
	}
}

// In open mode a join cookie for an ACCESS invitation is ignored: self-signup
// admits, the Plan is bound, no grant is consulted or created.
func TestOAuthCallback_OpenMode_IgnoresAccessInvitation(t *testing.T) {
	t.Parallel()
	sp := &fakeSignup{created: true}
	ac := &fakeAccess{
		grants: map[string]storage.AccessGrant{"555": {DiscordUserID: "555", PlatformKeys: true}},
		links:  map[string]storage.AccessGrant{"good-link": {PlatformKeys: true}},
	}
	adm := allowlistAdmission(sp, ac)
	adm.Mode = auth.AdmissionOpen
	adm.SignupPlanSlug = "byok-free"
	rec := accessRound{du: granted, aup: true, join: "good-link"}.run(t, adm, &fakeOAuthStore{})

	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/onboarding/create-tenant" {
		t.Fatalf("status = %d, Location = %q; want the onboarding step", rec.Code, loc)
	}
	if len(ac.calls) != 0 {
		t.Errorf("open mode consulted the access store: %v", ac.calls)
	}
	// Byte-for-byte the pre-ADR-0065 signup: the Plan, and no Platform Keys
	// choice (the column default stands; the Plan decides in open mode).
	if sp.calls != 1 || sp.got.PlanSlug != "byok-free" || sp.got.PlatformKeys != nil || sp.got.AUPAcceptedAt.IsZero() {
		t.Errorf("open-mode signup params = %+v", sp.got)
	}
	assertJoinCookieCleared(t, rec)
}

// The zero Admission — and any allowlist policy missing a store it needs —
// fails closed: Platform Admins only, exactly ADR-0041.
func TestOAuthCallback_Allowlist_MissingStoresFailClosed(t *testing.T) {
	t.Parallel()
	ac := &fakeAccess{
		grants: map[string]storage.AccessGrant{"555": {DiscordUserID: "555"}},
		links:  map[string]storage.AccessGrant{"good-link": {}},
	}
	sp := &fakeSignup{created: true}
	for name, adm := range map[string]auth.Admission{
		"zero value":         {},
		"no access store":    {Signup: sp},
		"no provisioner":     {Access: ac},
		"open, no provision": {Mode: auth.AdmissionOpen, Access: ac},
	} {
		rec := accessRound{du: granted, aup: true}.run(t, adm, &fakeOAuthStore{})
		if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/login?error=not_authorized" {
			t.Errorf("%s: status = %d, Location = %q; want the not_authorized bounce", name, rec.Code, loc)
		}
		assertNoSession(t, rec)
	}
	if sp.calls != 0 || ac.redeemed() {
		t.Errorf("a half-wired policy admitted someone (signup=%d, access=%v)", sp.calls, ac.calls)
	}
}

// The join cookie is one-shot: it is cleared even when the callback fails
// before it ever looks at who the visitor is.
func TestOAuthCallback_ClearsJoinCookieOnBadState(t *testing.T) {
	t.Parallel()
	o := auth.NewOAuth(&fakeOAuthStore{}, &fakeDiscord{}, "/", auth.Admission{}, auth.EdgeConfig{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/auth/discord/callback?code=c&state=forged", nil)
	req.AddCookie(&http.Cookie{Name: "glyphoxa_oauth_state", Value: "real"})
	req.AddCookie(&http.Cookie{Name: "glyphoxa_join", Value: "good-link"})
	rec := httptest.NewRecorder()
	o.Callback(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertJoinCookieCleared(t, rec)
}

// A join cookie whose value is not token-shaped (the cookie is client-held) is
// treated as no join at all — never echoed into the rejection redirect.
func TestOAuthCallback_MalformedJoinCookieIsNoJoin(t *testing.T) {
	t.Parallel()
	ac := &fakeAccess{}
	rec := accessRound{du: granted, aup: true, join: "../../evil"}.run(t, allowlistAdmission(&fakeSignup{}, ac), &fakeOAuthStore{})
	if loc := rec.Header().Get("Location"); loc != "/login?error=not_authorized" {
		t.Errorf("Location = %q, want /login?error=not_authorized", loc)
	}
	if ac.redeemed() {
		t.Errorf("a malformed token reached the redemption: %v", ac.calls)
	}
}

// fakeInvitations is a scripted InvitationReader.
type fakeInvitations struct {
	byToken map[string]storage.Invitation
	users   map[uuid.UUID]storage.User
	// campaigns names what a CAMPAIGN invitation opens; a campaign missing
	// here reads as archived/gone.
	campaigns   map[uuid.UUID]storage.InvitationTarget
	campaignErr error
	err         error
}

func (f fakeInvitations) GetInvitationCampaign(_ context.Context, id uuid.UUID) (storage.InvitationTarget, error) {
	if f.campaignErr != nil {
		return storage.InvitationTarget{}, f.campaignErr
	}
	target, ok := f.campaigns[id]
	if !ok {
		return storage.InvitationTarget{}, storage.ErrNotFound
	}
	return target, nil
}

func (f fakeInvitations) GetInvitationByToken(_ context.Context, token string) (storage.Invitation, error) {
	if f.err != nil {
		return storage.Invitation{}, f.err
	}
	inv, ok := f.byToken[token]
	if !ok {
		return storage.Invitation{}, storage.ErrNotFound
	}
	return inv, nil
}

func (f fakeInvitations) GetUserByID(_ context.Context, id uuid.UUID) (storage.User, error) {
	u, ok := f.users[id]
	if !ok {
		return storage.User{}, storage.ErrNotFound
	}
	return u, nil
}

// accessAuthClient mounts an AuthServer behind the production-shaped stack
// (GetInvitation public) with the ADR-0065 seams wired as given.
func accessAuthClient(t *testing.T, authn auth.Authenticator, tr auth.TenantResolver, wire func(*auth.AuthServer)) managementv1connect.AuthServiceClient {
	t.Helper()
	stack := auth.NewPolicy(authn, tr).Stack(
		managementv1connect.AuthServiceGetCurrentUserProcedure,
		managementv1connect.AuthServiceGetAdmissionModeProcedure,
		managementv1connect.AuthServiceGetInvitationProcedure)
	server := auth.NewAuthServer(&fakeDeleter{}, fakeNamer{}, &fakeRenamer{}, auth.AdmissionAllowlist, auth.EdgeConfig{}, slog.New(slog.DiscardHandler))
	if wire != nil {
		wire(server)
	}
	mux := http.NewServeMux()
	mux.Handle(server.Handler(stack.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return managementv1connect.NewAuthServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())
}

// GetInvitation is the public join preview: a live ACCESS invitation shows
// its kind and who sent it; everything else is the same empty "not valid".
func TestGetInvitation(t *testing.T) {
	t.Parallel()
	inviter := uuid.New()
	gone := uuid.New()
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	one := 1
	campaign := uuid.New()
	inv := fakeInvitations{
		users: map[uuid.UUID]storage.User{inviter: {ID: inviter, Name: "Admin Ada"}},
		byToken: map[string]storage.Invitation{
			"live":      {Kind: storage.InvitationAccess, CreatedBy: &inviter, ExpiresAt: &future, PlatformKeys: true, Label: "secret label"},
			"orphaned":  {Kind: storage.InvitationAccess, CreatedBy: &gone},
			"anonymous": {Kind: storage.InvitationAccess},
			"pinned":    {Kind: storage.InvitationAccess, CreatedBy: &inviter, DiscordUserID: "555"},
			"revoked":   {Kind: storage.InvitationAccess, CreatedBy: &inviter, RevokedAt: &past},
			"expired":   {Kind: storage.InvitationAccess, CreatedBy: &inviter, ExpiresAt: &past},
			"spent":     {Kind: storage.InvitationAccess, CreatedBy: &inviter, MaxUses: &one, Uses: 1},
			"campaign":  {Kind: storage.InvitationCampaign, CreatedBy: &inviter, CampaignID: &campaign, Rights: []string{"wiki.read"}},
		},
	}
	// No session cookie on any call: the visitor has not signed in yet.
	client := accessAuthClient(t, fakeAuthN{}, fakeTenant{}, func(s *auth.AuthServer) { s.SetInvitations(inv) })
	preview := func(token string) *managementv1.GetInvitationResponse {
		t.Helper()
		resp, err := client.GetInvitation(context.Background(), connect.NewRequest(&managementv1.GetInvitationRequest{Token: token}))
		if err != nil {
			t.Fatalf("GetInvitation(%q): %v (must never be an error for a token that merely does not work)", token, err)
		}
		return resp.Msg
	}

	for token, wantInviter := range map[string]string{"live": "Admin Ada", "pinned": "Admin Ada", "orphaned": "", "anonymous": ""} {
		got := preview(token)
		if !got.GetValid() || got.GetKind() != managementv1.InvitationKind_INVITATION_KIND_ACCESS || got.GetInviterName() != wantInviter {
			t.Errorf("%s: preview = %+v, want valid ACCESS from %q", token, got, wantInviter)
		}
		if got.GetCampaignName() != "" || got.GetTenantName() != "" || len(got.GetRights()) != 0 {
			t.Errorf("%s: an ACCESS preview carries campaign fields: %+v", token, got)
		}
	}

	// Unknown and dead links are indistinguishable — including "campaign", a
	// link to a Campaign this fake does not know, i.e. an archived or deleted
	// one (TestGetInvitation_Campaign covers the live case).
	for _, token := range []string{"never-minted", "", "revoked", "expired", "spent", "campaign", "not a token!"} {
		got := preview(token)
		if got.GetValid() || got.GetKind() != managementv1.InvitationKind_INVITATION_KIND_UNSPECIFIED ||
			got.GetInviterName() != "" || got.GetCampaignName() != "" || got.GetTenantName() != "" || len(got.GetRights()) != 0 {
			t.Errorf("%q: preview = %+v, want valid=false with every other field empty", token, got)
		}
	}
}

// An unwired AuthServer previews nothing as valid, and a store failure is an
// internal error — not a NotFound, and not a "valid" answer.
func TestGetInvitation_FailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	req := func() *connect.Request[managementv1.GetInvitationRequest] {
		return connect.NewRequest(&managementv1.GetInvitationRequest{Token: "live"})
	}

	unwired := accessAuthClient(t, fakeAuthN{}, fakeTenant{}, nil)
	if resp, err := unwired.GetInvitation(ctx, req()); err != nil || resp.Msg.GetValid() {
		t.Errorf("unwired GetInvitation = %+v, %v; want valid=false", resp, err)
	}

	broken := accessAuthClient(t, fakeAuthN{}, fakeTenant{}, func(s *auth.AuthServer) {
		s.SetInvitations(fakeInvitations{err: errors.New("db down")})
	})
	_, err := broken.GetInvitation(ctx, req())
	if code := connect.CodeOf(err); err == nil || code != connect.CodeInternal {
		t.Errorf("GetInvitation on a store failure = %v, want CodeInternal", err)
	}
	if err != nil && strings.Contains(err.Error(), "db down") {
		t.Errorf("the store error leaked to the client: %v", err)
	}
}

// ownTenants is a TenantResolver scripted independently of the stack's, so a
// test can tell has_own_tenant apart from "the request resolved a tenant".
type ownTenants struct {
	has bool
	err error
}

func (o ownTenants) TenantForUser(context.Context, uuid.UUID) (uuid.UUID, error) {
	switch {
	case o.err != nil:
		return uuid.Nil, o.err
	case o.has:
		return uuid.New(), nil
	}
	return uuid.Nil, storage.ErrNotFound
}

// GetCurrentUser tells the SPA whether to show the Admin screen and whether
// the caller operates a Tenant of their own.
func TestGetCurrentUser_PlatformAdminAndOwnTenant(t *testing.T) {
	t.Parallel()
	admin := storage.User{ID: uuid.New(), DiscordUserID: "42", Name: "Admin Ada", Role: "operator"}
	gm := storage.User{ID: uuid.New(), DiscordUserID: "555", Name: "Rin", Role: "operator"}
	authn := fakeAuthN{users: map[string]storage.User{"tok-admin": admin, "tok-gm": gm}}
	admins := auth.PlatformAdmins{Allowlist: auth.ParseOperatorAllowlist("42")}
	me := func(client managementv1connect.AuthServiceClient, token string) (*managementv1.GetCurrentUserResponse, error) {
		req := connect.NewRequest(&managementv1.GetCurrentUserRequest{})
		req.Header().Set("Cookie", auth.SessionCookieName+"="+token)
		resp, err := client.GetCurrentUser(context.Background(), req)
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}

	client := accessAuthClient(t, authn, fakeTenant{id: uuid.New()}, func(s *auth.AuthServer) {
		s.SetPlatformAdmins(admins)
		s.SetOwnTenants(ownTenants{has: true})
	})
	if got, err := me(client, "tok-admin"); err != nil || !got.GetIsPlatformAdmin() || !got.GetHasOwnTenant() {
		t.Errorf("admin = %+v, %v; want is_platform_admin and has_own_tenant", got, err)
	}
	if got, err := me(client, "tok-gm"); err != nil || got.GetIsPlatformAdmin() || !got.GetHasOwnTenant() {
		t.Errorf("gm = %+v, %v; want has_own_tenant only", got, err)
	}

	// has_own_tenant is the Tenant the caller OPERATES — not "the request
	// resolved a tenant", which will name a host Tenant for a Campaign Guest.
	guestish := accessAuthClient(t, authn, fakeTenant{id: uuid.New()}, func(s *auth.AuthServer) {
		s.SetPlatformAdmins(admins)
		s.SetOwnTenants(ownTenants{has: false})
	})
	if got, err := me(guestish, "tok-gm"); err != nil || got.GetHasOwnTenant() || got.GetTenantId() == "" {
		t.Errorf("tenantless caller = %+v, %v; want has_own_tenant=false with the resolved tenant still reported", got, err)
	}

	// Unwired: nobody is a Platform Admin, and the resolved tenant stands in.
	unwired := accessAuthClient(t, authn, fakeTenant{id: uuid.New()}, nil)
	if got, err := me(unwired, "tok-admin"); err != nil || got.GetIsPlatformAdmin() || !got.GetHasOwnTenant() {
		t.Errorf("unwired = %+v, %v; want is_platform_admin=false, has_own_tenant from the resolved tenant", got, err)
	}
	tenantless := accessAuthClient(t, authn, fakeTenant{err: storage.ErrNotFound}, nil)
	if got, err := me(tenantless, "tok-admin"); err != nil || got.GetHasOwnTenant() {
		t.Errorf("unwired tenantless = %+v, %v; want has_own_tenant=false", got, err)
	}

	failing := accessAuthClient(t, authn, fakeTenant{id: uuid.New()}, func(s *auth.AuthServer) {
		s.SetOwnTenants(ownTenants{err: errors.New("db down")})
	})
	if _, err := me(failing, "tok-gm"); connect.CodeOf(err) != connect.CodeInternal {
		t.Errorf("own-tenant lookup failure = %v, want CodeInternal", err)
	}
}
