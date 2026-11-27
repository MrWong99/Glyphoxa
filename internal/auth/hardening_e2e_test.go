//go:build integration

package auth_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// ADR-0065 review hardening, through the real handlers and a real Postgres.

// TestOpenMode_AddedGuestStillSelfSignsUp: on an open deployment a GM adding
// somebody's Discord User ID as a Campaign Guest — which needs no consent —
// must not take away that person's self-signup. They found their Tenant like
// anybody else, and the Campaign they were added to is in their switcher.
func TestOpenMode_AddedGuestStillSelfSignsUp(t *testing.T) {
	store, pool := migratedStore(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO plan (slug, display_name) VALUES ('free', 'Free')`); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	host, err := store.ProvisionSignup(ctx, storage.SignupParams{
		User: storage.UpsertUserParams{DiscordUserID: "100000000000000001", Name: "GM Gil"}, TenantName: "Gil's Table",
		PlanSlug: "free", Session: storage.NewSession{Token: "tok-host", ExpiresAt: time.Now().Add(time.Hour)},
	})
	if err != nil {
		t.Fatalf("provision host: %v", err)
	}
	campaignID, err := store.CreateCampaign(ctx, storage.NewCampaign{TenantID: host.Tenant.ID, Name: "Saltmarsh", System: "dnd5e", Language: "en"})
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}

	// The victim has never signed in; the GM only knows their snowflake.
	const mikaID = "300000000000000001"
	if _, err := store.AddCampaignGuest(ctx, campaignID, mikaID, []string{"wiki.read"}, host.User.ID); err != nil {
		t.Fatalf("AddCampaignGuest: %v", err)
	}

	disc := &fakeDiscord{authBase: "https://discord/authorize",
		user: auth.DiscordUser{ID: mikaID, Username: "mika", GlobalName: "Mika Tran"}}
	o := auth.NewOAuth(store, disc, "/", auth.Admission{
		Mode: auth.AdmissionOpen, SignupPlanSlug: "free", Signup: store, Access: store, Guests: store,
	}, auth.EdgeConfig{}, nil)

	for _, attempt := range []string{"first sign-in", "returning sign-in"} {
		rec := plainRoundTrip(t, o, true)
		// A fresh signup lands on the name-your-Tenant step; either way it
		// is a redirect INTO the app, not the not-authorized bounce.
		if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || strings.HasPrefix(loc, "/login") {
			t.Fatalf("%s: status = %d, Location = %q, body = %s", attempt, rec.Code, loc, rec.Body)
		}
		sess := cookieByName(rec.Result().Cookies(), auth.SessionCookieName)
		if sess == nil {
			t.Fatalf("%s: no session cookie", attempt)
		}
		mika, err := store.AuthenticateSession(ctx, sess.Value)
		if err != nil {
			t.Fatalf("%s: AuthenticateSession: %v", attempt, err)
		}
		own, err := store.TenantForUser(ctx, mika.ID)
		if err != nil {
			t.Fatalf("%s: the added guest operates no Tenant (%v): being added by id blocked their self-signup", attempt, err)
		}
		// The session acts in their OWN Tenant…
		ws, err := auth.Workspaces{Store: store, Mode: auth.AdmissionOpen}.ResolveWorkspace(ctx, mika, sess.Value)
		if err != nil || ws.Guest || ws.TenantID != own {
			t.Fatalf("%s: workspace = %+v, %v; want their own tenant", attempt, ws, err)
		}
		// …and the Campaign they were added to is one switch away.
		shared, err := store.ListGuestWorkspaces(ctx, mika.ID)
		if err != nil || len(shared) != 1 || shared[0].CampaignID != campaignID {
			t.Fatalf("%s: shared campaigns = %v, %v; want Saltmarsh", attempt, shared, err)
		}
	}
	if n := countRows(t, pool, `SELECT count(*) FROM tenant t JOIN users u ON u.id = t.operator_user_id WHERE u.discord_user_id = $1`, mikaID); n != 1 {
		t.Errorf("the added guest operates %d Tenants after two sign-ins, want exactly 1", n)
	}
}

// TestRevokeAccess_EndsGMOnDiscord: on an allowlist deployment the Discord
// side of GM identity follows the grant. A revoked operator stops being GM of
// their own Tenant on the next snapshot — the presence gate and the Butler
// voice gate both read this — and re-granting restores it. A Platform Admin
// needs no grant.
func TestRevokeAccess_EndsGMOnDiscord(t *testing.T) {
	store, _ := migratedStore(t)
	ctx := context.Background()
	const adminID, gmID = "100000000000000001", "300000000000000002"
	allow := auth.ParseOperatorAllowlist(adminID)
	store.SetAccessPolicy(storage.AccessPolicy{OperatorsNeedGrant: true, PlatformAdmins: allow.IDs()})

	found := func(discordID, token string) storage.SignupResult {
		t.Helper()
		res, err := store.ProvisionSignup(ctx, storage.SignupParams{
			User: storage.UpsertUserParams{DiscordUserID: discordID, Name: "GM " + discordID}, TenantName: "Table " + discordID,
			Session: storage.NewSession{Token: token, ExpiresAt: time.Now().Add(time.Hour)},
		})
		if err != nil {
			t.Fatalf("provision %s: %v", discordID, err)
		}
		return res
	}
	admin := found(adminID, "tok-admin")
	if _, err := store.GrantAccess(ctx, gmID, "", true, admin.User.ID); err != nil {
		t.Fatalf("GrantAccess: %v", err)
	}
	gm := found(gmID, "tok-gm")

	gmIdentity := auth.NewGMIdentity(store, allow, nil)
	isGM := func() bool {
		t.Helper()
		if err := gmIdentity.Refresh(ctx); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		return gmIdentity.IsGMInTenant(gm.Tenant.ID, gmID)
	}
	if !isGM() {
		t.Fatal("a granted operator is not GM of their own Tenant")
	}

	if err := store.RevokeAccess(ctx, gmID); err != nil {
		t.Fatalf("RevokeAccess: %v", err)
	}
	if isGM() {
		t.Error("a revoked operator is still GM of their Tenant on Discord")
	}
	if gmIdentity.IsGM(gmID) {
		t.Error("a revoked operator still passes the deployment-wide GM check")
	}
	if !gmIdentity.IsGMInTenant(admin.Tenant.ID, adminID) || !gmIdentity.IsGMInTenant(gm.Tenant.ID, adminID) {
		t.Error("the Platform Admin lost GM standing")
	}

	if _, err := store.GrantAccess(ctx, gmID, "", false, admin.User.ID); err != nil {
		t.Fatalf("re-grant: %v", err)
	}
	if !isGM() {
		t.Error("re-granting did not restore GM standing")
	}
}
