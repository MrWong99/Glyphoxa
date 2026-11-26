//go:build integration

package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// joinRoundTrip drives a whole join through the real handlers: the Login start
// (which sets the state and join cookies), then the callback carrying exactly
// the cookies Login issued. It returns the callback's response.
func joinRoundTrip(t *testing.T, o *auth.OAuth, token string) *httptest.ResponseRecorder {
	t.Helper()
	start := "/auth/discord/login"
	if token != "" {
		start += "?join=" + token + "&aup=1"
	}
	loginRec := httptest.NewRecorder()
	o.Login(loginRec, httptest.NewRequest(http.MethodGet, start, nil))
	loc := loginRec.Header().Get("Location")
	if loginRec.Code != http.StatusFound || !strings.Contains(loc, "state=") {
		t.Fatalf("login start: status = %d, Location = %q", loginRec.Code, loc)
	}
	state := loc[strings.Index(loc, "state=")+len("state="):]

	cb := httptest.NewRequest(http.MethodGet, "/auth/discord/callback?code=c&state="+state, nil)
	for _, c := range loginRec.Result().Cookies() {
		cb.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	o.Callback(rec, cb)
	return rec
}

// TestAccessEndToEnd drives ADR-0065 instance access against a real Postgres:
// a Platform Admin's invitation link admits a stranger on an allowlist
// deployment, who founds a Tenant of their own with the link's Platform Keys
// choice; the link counts one use; a second person is refused once it is
// spent; and revoking the grant signs the first one out and keeps them out.
func TestAccessEndToEnd(t *testing.T) {
	store, pool := migratedStore(t)
	ctx := context.Background()

	admin, err := store.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: "100000000000000001", Name: "Admin Ada"})
	if err != nil {
		t.Fatalf("upsert admin: %v", err)
	}
	// The seed Tenant must stay the Platform Admin's: a grant holder never claims it.
	seedID, err := store.CreateTenant(ctx, "Seed")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	token, err := auth.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	one := 1
	if _, err := store.CreateInvitation(ctx, storage.NewInvitation{
		Kind: storage.InvitationAccess, Token: token, Label: "Con crew", MaxUses: &one, CreatedBy: admin.ID,
	}); err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}

	disc := &fakeDiscord{authBase: "https://discord/authorize", user: auth.DiscordUser{ID: "300000000000000001", Username: "rin", GlobalName: "Rin Okabe"}}
	o := auth.NewOAuth(store, disc, "/", auth.Admission{
		Mode:      auth.AdmissionAllowlist,
		Allowlist: auth.ParseOperatorAllowlist(admin.DiscordUserID),
		Signup:    store,
		Access:    store,
	}, auth.EdgeConfig{}, nil)

	// The public preview, over the real store, with no session.
	stack := auth.NewPolicy(store, store).Stack(
		managementv1connect.AuthServiceGetCurrentUserProcedure,
		managementv1connect.AuthServiceGetInvitationProcedure)
	server := auth.NewAuthServer(store, store, store, auth.AdmissionAllowlist, auth.EdgeConfig{}, nil)
	server.SetPlatformAdmins(auth.PlatformAdmins{Allowlist: auth.ParseOperatorAllowlist(admin.DiscordUserID)})
	server.SetOwnTenants(store)
	server.SetInvitations(store)
	mux := http.NewServeMux()
	mux.Handle(server.Handler(stack.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := managementv1connect.NewAuthServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())
	preview := func() *managementv1.GetInvitationResponse {
		t.Helper()
		resp, err := client.GetInvitation(ctx, connect.NewRequest(&managementv1.GetInvitationRequest{Token: token}))
		if err != nil {
			t.Fatalf("GetInvitation: %v", err)
		}
		return resp.Msg
	}
	if p := preview(); !p.GetValid() || p.GetKind() != managementv1.InvitationKind_INVITATION_KIND_ACCESS || p.GetInviterName() != "Admin Ada" {
		t.Fatalf("preview of a live link = %+v", p)
	}

	// Without the link the stranger is nobody.
	if rec := joinRoundTrip(t, o, ""); rec.Header().Get("Location") != "/login?error=not_authorized" {
		t.Fatalf("stranger without a link: Location = %q", rec.Header().Get("Location"))
	}

	// 1. The join admits them and founds their own Tenant.
	rec := joinRoundTrip(t, o, token)
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/onboarding/create-tenant" {
		t.Fatalf("join: status = %d, Location = %q, body = %s", rec.Code, loc, rec.Body)
	}
	sess := cookieByName(rec.Result().Cookies(), auth.SessionCookieName)
	if sess == nil {
		t.Fatal("join issued no session cookie")
	}
	rin, err := store.AuthenticateSession(ctx, sess.Value)
	if err != nil {
		t.Fatalf("the join's session does not authenticate: %v", err)
	}
	if rin.AUPAcceptedAt == nil {
		t.Error("the join's acknowledgment was not recorded on the user row")
	}
	tenantID, err := store.TenantForUser(ctx, rin.ID)
	if err != nil || tenantID == seedID {
		t.Fatalf("TenantForUser = %s, %v; want a fresh tenant, never the seed", tenantID, err)
	}
	// The link granted no Platform Keys, so the new Tenant must not spend them.
	if ok, err := store.TenantPlatformKeys(ctx, tenantID); err != nil || ok {
		t.Errorf("founded tenant platform_keys = %v, %v, want false (the invitation's choice)", ok, err)
	}
	if g, err := store.GetAccessGrant(ctx, rin.DiscordUserID); err != nil || g.Note != "Con crew" {
		t.Errorf("grant = %+v, %v", g, err)
	}
	var uses int
	if err := pool.QueryRow(ctx, `SELECT uses FROM invitation`).Scan(&uses); err != nil || uses != 1 {
		t.Fatalf("invitation uses = %d, %v, want 1", uses, err)
	}

	// GetCurrentUser over the real stack: a GM with a Tenant, not an admin.
	meReq := connect.NewRequest(&managementv1.GetCurrentUserRequest{})
	meReq.Header().Set("Cookie", auth.SessionCookieName+"="+sess.Value)
	me, err := client.GetCurrentUser(ctx, meReq)
	if err != nil || me.Msg.GetIsPlatformAdmin() || !me.Msg.GetHasOwnTenant() || me.Msg.GetTenantId() != tenantID.String() {
		t.Errorf("GetCurrentUser = %+v, %v; want has_own_tenant, not a Platform Admin", me, err)
	}

	// 2. Signing in again is an ordinary login now: the grant admits, no link
	// needed, no second Tenant, no second use.
	if rec := joinRoundTrip(t, o, ""); rec.Header().Get("Location") != "/" {
		t.Fatalf("returning grant holder: Location = %q, want /", rec.Header().Get("Location"))
	}
	if again, err := store.TenantForUser(ctx, rin.ID); err != nil || again != tenantID {
		t.Errorf("returning login changed the tenant: %s, %v", again, err)
	}

	// 3. The single-use link is spent: the next person is sent back to its
	// join page, and the preview says it no longer works.
	disc.user = auth.DiscordUser{ID: "300000000000000002", Username: "mo"}
	if rec := joinRoundTrip(t, o, token); rec.Header().Get("Location") != "/join/"+token+"?error=invalid" {
		t.Fatalf("second redeemer: Location = %q, want the join page with error=invalid", rec.Header().Get("Location"))
	}
	if _, err := store.GetUserByDiscordID(ctx, "300000000000000002"); err == nil {
		t.Error("a refused redeemer got a users row")
	}
	if p := preview(); p.GetValid() || p.GetInviterName() != "" {
		t.Errorf("preview of a spent link = %+v, want valid=false and nothing else", p)
	}

	// 4. Revoking signs Rin out at once and keeps them out; their Tenant stays.
	if err := store.RevokeAccess(ctx, rin.DiscordUserID); err != nil {
		t.Fatalf("RevokeAccess: %v", err)
	}
	if _, err := store.AuthenticateSession(ctx, sess.Value); err == nil {
		t.Error("the session survived the revoke")
	}
	disc.user = auth.DiscordUser{ID: rin.DiscordUserID, Username: "rin"}
	if rec := joinRoundTrip(t, o, ""); rec.Header().Get("Location") != "/login?error=not_authorized" {
		t.Fatalf("revoked user: Location = %q, want the not_authorized bounce", rec.Header().Get("Location"))
	}
	if kept, err := store.TenantForUser(ctx, rin.ID); err != nil || kept != tenantID {
		t.Errorf("the revoke lost the tenant: %s, %v", kept, err)
	}
}
