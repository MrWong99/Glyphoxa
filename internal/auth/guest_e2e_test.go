//go:build integration

package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// plainRoundTrip is an ordinary (no-invitation) sign-in through the real
// handlers. On an `open` deployment the login start requires the
// acknowledgment, which the login screen appends as aup=1.
func plainRoundTrip(t *testing.T, o *auth.OAuth, open bool) *httptest.ResponseRecorder {
	t.Helper()
	start := "/auth/discord/login"
	if open {
		start += "?aup=1"
	}
	loginRec := httptest.NewRecorder()
	o.Login(loginRec, httptest.NewRequest(http.MethodGet, start, nil))
	loc := loginRec.Header().Get("Location")
	if loginRec.Code != http.StatusFound {
		t.Fatalf("login start: status = %d, Location = %q", loginRec.Code, loc)
	}
	cb := httptest.NewRequest(http.MethodGet, "/auth/discord/callback?code=c&state="+stateOf(loc), nil)
	for _, c := range loginRec.Result().Cookies() {
		cb.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	o.Callback(rec, cb)
	return rec
}

// stateOf extracts the state parameter from the fake authorize URL.
func stateOf(loc string) string {
	const key = "state="
	for i := 0; i+len(key) <= len(loc); i++ {
		if loc[i:i+len(key)] == key {
			return loc[i+len(key):]
		}
	}
	return ""
}

func countRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// TestCampaignGuestEndToEnd drives ADR-0065 lane 2 against a real Postgres, in
// BOTH Admission Modes: a GM's campaign invitation link turns a stranger into
// a Campaign Guest — a session acting in that Campaign of the HOST Tenant, and
// no Tenant of their own, ever; signing in again later keeps them a guest; the
// policy resolves them per request; and removing them takes effect at once.
func TestCampaignGuestEndToEnd(t *testing.T) {
	for _, mode := range []auth.AdmissionMode{auth.AdmissionAllowlist, auth.AdmissionOpen} {
		t.Run(string(mode), func(t *testing.T) {
			store, pool := migratedStore(t)
			ctx := context.Background()
			open := mode == auth.AdmissionOpen
			planSlug := ""
			if open {
				// An open deployment binds its signups to a Plan; the guest
				// routes must never reach that binding.
				if _, err := pool.Exec(ctx,
					`INSERT INTO plan (slug, display_name) VALUES ('free', 'Free')`); err != nil {
					t.Fatalf("seed plan: %v", err)
				}
				planSlug = "free"
			}

			// The host GM is a Platform Admin here, so their own Tenant is a
			// workspace in either mode.
			const hostID = "100000000000000001"
			host, err := store.ProvisionSignup(ctx, storage.SignupParams{
				User: storage.UpsertUserParams{DiscordUserID: hostID, Name: "GM Gil"}, TenantName: "Gil's Table",
				PlanSlug: planSlug, Session: storage.NewSession{Token: "tok-host", ExpiresAt: time.Now().Add(time.Hour)},
			})
			if err != nil {
				t.Fatalf("provision host: %v", err)
			}
			campaignID, err := store.CreateCampaign(ctx, storage.NewCampaign{TenantID: host.Tenant.ID, Name: "Saltmarsh", System: "dnd5e", Language: "en"})
			if err != nil {
				t.Fatalf("create campaign: %v", err)
			}
			token, err := auth.NewToken()
			if err != nil {
				t.Fatalf("NewToken: %v", err)
			}
			if _, err := store.CreateInvitation(ctx, storage.NewInvitation{
				Kind: storage.InvitationCampaign, Token: token, CampaignID: &campaignID, CreatedBy: host.User.ID,
				Rights: auth.GuestRightStrings(auth.NormalizeGuestRights([]auth.GuestRight{auth.RightMapsEdit})),
			}); err != nil {
				t.Fatalf("CreateInvitation: %v", err)
			}

			admins := auth.PlatformAdmins{Allowlist: auth.ParseOperatorAllowlist(hostID)}
			disc := &fakeDiscord{authBase: "https://discord/authorize",
				user: auth.DiscordUser{ID: "300000000000000001", Username: "mika", GlobalName: "Mika Tran"}}
			o := auth.NewOAuth(store, disc, "/", auth.Admission{
				Mode: mode, Allowlist: admins.Allowlist, SignupPlanSlug: planSlug,
				Signup: store, Access: store, Guests: store,
			}, auth.EdgeConfig{}, nil)

			workspaces := auth.Workspaces{Store: store, Admins: admins, Mode: mode}
			policy := auth.NewPolicy(store, store).WithWorkspaces(workspaces)
			stack := policy.Stack(
				managementv1connect.AuthServiceGetCurrentUserProcedure,
				managementv1connect.AuthServiceGetInvitationProcedure)
			server := auth.NewAuthServer(store, store, store, mode, auth.EdgeConfig{}, nil)
			server.SetPlatformAdmins(admins)
			server.SetInvitations(store)
			server.SetWorkspaces(workspaces, store)
			mux := http.NewServeMux()
			mux.Handle(server.Handler(stack.HandlerOptions()...))
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			client := managementv1connect.NewAuthServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())

			// The public preview names the campaign, the table and the rights.
			p, err := client.GetInvitation(ctx, connect.NewRequest(&managementv1.GetInvitationRequest{Token: token}))
			if err != nil || !p.Msg.GetValid() || p.Msg.GetKind() != managementv1.InvitationKind_INVITATION_KIND_CAMPAIGN ||
				p.Msg.GetCampaignName() != "Saltmarsh" || p.Msg.GetTenantName() != "Gil's Table" ||
				p.Msg.GetInviterName() != "GM Gil" || len(p.Msg.GetRights()) != 2 {
				t.Fatalf("preview = %+v, %v", p, err)
			}

			tenantsBefore := countRows(t, pool, `SELECT count(*) FROM tenant`)

			// 1. The join.
			rec := joinRoundTrip(t, o, token)
			if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/" {
				t.Fatalf("join: status = %d, Location = %q, body = %s", rec.Code, loc, rec.Body)
			}
			sess := cookieByName(rec.Result().Cookies(), auth.SessionCookieName)
			if sess == nil {
				t.Fatal("the join issued no session cookie")
			}
			mika, err := store.AuthenticateSession(ctx, sess.Value)
			if err != nil || mika.Name != "Mika Tran" || mika.AUPAcceptedAt == nil {
				t.Fatalf("the join's session user = %+v, %v; want Mika with the acknowledgment recorded", mika, err)
			}
			// NEVER a Tenant, a Plan subscription or an Access Grant.
			if n := countRows(t, pool, `SELECT count(*) FROM tenant`); n != tenantsBefore {
				t.Fatalf("tenants went from %d to %d: the campaign join founded a Tenant", tenantsBefore, n)
			}
			if _, err := store.TenantForUser(ctx, mika.ID); err == nil {
				t.Fatal("the guest operates a Tenant")
			}
			if n := countRows(t, pool, `SELECT count(*) FROM access_grant`); n != 0 {
				t.Errorf("the campaign join created %d access grants", n)
			}
			if n := countRows(t, pool, `SELECT count(*) FROM campaign_guest WHERE user_id = $1 AND campaign_id = $2`, mika.ID, campaignID); n != 1 {
				t.Fatalf("guest rows = %d, want 1", n)
			}
			if uses := countRows(t, pool, `SELECT uses FROM invitation`); uses != 1 {
				t.Errorf("invitation uses = %d, want 1", uses)
			}

			// The policy resolves the session to the guest workspace.
			me := func(cookie string) *managementv1.GetCurrentUserResponse {
				t.Helper()
				req := connect.NewRequest(&managementv1.GetCurrentUserRequest{})
				req.Header().Set("Cookie", auth.SessionCookieName+"="+cookie)
				resp, err := client.GetCurrentUser(ctx, req)
				if err != nil {
					t.Fatalf("GetCurrentUser: %v", err)
				}
				return resp.Msg
			}
			got := me(sess.Value)
			if g := got.GetGuest(); g == nil || g.GetCampaignId() != campaignID.String() || g.GetCampaignName() != "Saltmarsh" || len(g.GetRights()) != 2 {
				t.Fatalf("GetCurrentUser.guest = %+v, want Saltmarsh with maps.read + maps.edit", got.GetGuest())
			}
			if got.GetTenantId() != host.Tenant.ID.String() || got.GetHasOwnTenant() || got.GetIsPlatformAdmin() {
				t.Errorf("GetCurrentUser = %+v; want the HOST tenant, no own tenant, not an admin", got)
			}
			v := policy.Evaluate(ctx, auth.Input{SessionToken: sess.Value},
				auth.Check{Tenant: auth.TenantRequired, Guest: auth.GuestWith(auth.RightMapsRead)})
			if v.Deny != auth.DenyNone || !v.IsGuest || v.Guest.CampaignID != campaignID || v.Tenant != host.Tenant.ID {
				t.Fatalf("policy verdict for a maps mount = %+v, want the guest admitted in the host tenant", v)
			}
			if v := policy.Evaluate(ctx, auth.Input{SessionToken: sess.Value}, auth.Check{Tenant: auth.TenantRequired}); v.Deny != auth.DenyGuest {
				t.Fatalf("policy verdict for a GM-only mount = %v, want DenyGuest", v.Deny)
			}

			// 2. An ordinary sign-in later: still a guest, still no Tenant —
			// in open mode this is exactly where a stranger would found one.
			again := plainRoundTrip(t, o, open)
			if loc := again.Header().Get("Location"); again.Code != http.StatusFound || loc != "/" {
				t.Fatalf("returning guest: status = %d, Location = %q", again.Code, loc)
			}
			if n := countRows(t, pool, `SELECT count(*) FROM tenant`); n != tenantsBefore {
				t.Fatalf("tenants went from %d to %d on a returning guest's sign-in", tenantsBefore, n)
			}
			sess2 := cookieByName(again.Result().Cookies(), auth.SessionCookieName)
			if sess2 == nil || me(sess2.Value).GetGuest().GetCampaignId() != campaignID.String() {
				t.Fatal("the returning guest's session does not act in the guest campaign")
			}
			// Reopening the link is harmless: no second use.
			if rec := joinRoundTrip(t, o, token); rec.Header().Get("Location") != "/" {
				t.Fatalf("reopening the link: Location = %q", rec.Header().Get("Location"))
			}
			if uses := countRows(t, pool, `SELECT uses FROM invitation`); uses != 1 {
				t.Errorf("invitation uses after reopening = %d, want 1", uses)
			}

			// 3. The host GM opening their own campaign's link is not turned
			// into a guest of it.
			disc.user = auth.DiscordUser{ID: hostID, Username: "gil", GlobalName: "GM Gil"}
			if rec := joinRoundTrip(t, o, token); rec.Header().Get("Location") != "/join/"+token+"?error=invalid" {
				t.Errorf("the host on their own link: Location = %q, want the join page's invalid error", rec.Header().Get("Location"))
			}
			if n := countRows(t, pool, `SELECT count(*) FROM campaign_guest WHERE user_id = $1`, host.User.ID); n != 0 {
				t.Errorf("the host has %d guest rows", n)
			}
			if got := me("tok-host"); got.GetGuest() != nil || got.GetTenantId() != host.Tenant.ID.String() || !got.GetHasOwnTenant() {
				t.Errorf("the host's GetCurrentUser = %+v; want their own tenant", got)
			}

			// 4. Suspension wins: a suspended guest cannot sign in, and their
			// live session stops authenticating.
			if err := store.SetUserSuspended(ctx, mika.DiscordUserID, true); err != nil {
				t.Fatalf("SetUserSuspended: %v", err)
			}
			disc.user = auth.DiscordUser{ID: mika.DiscordUserID, Username: "mika", GlobalName: "Mika Tran"}
			if rec := plainRoundTrip(t, o, open); rec.Header().Get("Location") != "/login?error=not_authorized" {
				t.Errorf("suspended guest signing in: Location = %q", rec.Header().Get("Location"))
			}
			if rec := joinRoundTrip(t, o, token); rec.Header().Get("Location") != "/login?error=not_authorized" {
				t.Errorf("suspended guest reopening the link: Location = %q", rec.Header().Get("Location"))
			}
			if _, err := store.AuthenticateSession(ctx, sess.Value); err == nil {
				t.Error("a suspended guest's session still authenticates")
			}
			if err := store.SetUserSuspended(ctx, mika.DiscordUserID, false); err != nil {
				t.Fatalf("unsuspend: %v", err)
			}

			// 5. Removal takes effect on the next request, and on the next sign-in.
			if err := store.RemoveCampaignGuest(ctx, campaignID, mika.ID); err != nil {
				t.Fatalf("RemoveCampaignGuest: %v", err)
			}
			if v := policy.Evaluate(ctx, auth.Input{SessionToken: sess.Value},
				auth.Check{Tenant: auth.TenantRequired, Guest: auth.GuestWith(auth.RightMapsRead)}); v.Deny == auth.DenyNone {
				t.Fatalf("a removed guest still passes the maps mount: %+v", v)
			}
			if got := me(sess.Value); got.GetGuest() != nil || got.GetTenantId() != "" {
				t.Errorf("a removed guest's GetCurrentUser = %+v; want no guest scope and no tenant", got)
			}
			rec = plainRoundTrip(t, o, open)
			if open {
				// On an open deployment a former guest is an ordinary stranger
				// again: self-signup admits them (and only now founds a Tenant).
				if loc := rec.Header().Get("Location"); loc != "/onboarding/create-tenant" {
					t.Errorf("former guest on an open deployment: Location = %q, want the signup onboarding", loc)
				}
			} else if loc := rec.Header().Get("Location"); loc != "/login?error=not_authorized" {
				t.Errorf("former guest on an allowlist deployment: Location = %q, want the not_authorized bounce", loc)
			}
		})
	}
}
