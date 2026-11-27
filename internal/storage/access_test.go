//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// accessAdmin upserts the Platform Admin who mints invitations and grants in
// these tests (invitation.created_by / access_grant.granted_by are FKs).
func accessAdmin(t *testing.T, st *storage.Store) storage.User {
	t.Helper()
	u, err := st.UpsertUser(context.Background(), storage.UpsertUserParams{
		DiscordUserID: "100000000000000001", Name: "Admin Ada",
	})
	if err != nil {
		t.Fatalf("upsert admin: %v", err)
	}
	return u
}

// mintAccess creates an `access` invitation with the given token, applying
// tweak to the input first.
func mintAccess(t *testing.T, st *storage.Store, admin storage.User, token string, tweak func(*storage.NewInvitation)) storage.Invitation {
	t.Helper()
	n := storage.NewInvitation{Kind: storage.InvitationAccess, Token: token, CreatedBy: admin.ID}
	if tweak != nil {
		tweak(&n)
	}
	inv, err := st.CreateInvitation(context.Background(), n)
	if err != nil {
		t.Fatalf("CreateInvitation(%s): %v", token, err)
	}
	return inv
}

// invitationUses reads the stored use counter of the invitation behind token.
func invitationUses(t *testing.T, st *storage.Store, token string) int {
	t.Helper()
	inv, err := st.GetInvitationByToken(context.Background(), token)
	if err != nil {
		t.Fatalf("GetInvitationByToken(%s): %v", token, err)
	}
	return inv.Uses
}

func countAccessRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// foundTenant signs a Discord User in through the grant-shaped provisioning
// (no Plan) and returns what it founded.
func foundTenant(t *testing.T, st *storage.Store, discordID, token string, platformKeys *bool) storage.SignupResult {
	t.Helper()
	p := signupParams(discordID, token)
	p.PlanSlug = ""
	p.PlatformKeys = platformKeys
	res, err := st.ProvisionSignup(context.Background(), p)
	if err != nil {
		t.Fatalf("ProvisionSignup(%s): %v", discordID, err)
	}
	return res
}

// TestInvitationLifecycle covers minting, lookup, listing and revoking
// invitation links: only the digest is stored, the listing keeps dead links,
// and RevokeAccessInvitation is scoped to `access` invitations.
func TestInvitationLifecycle(t *testing.T) {
	st, pool := migratedWithPool(t)
	ctx := context.Background()
	admin := accessAdmin(t, st)

	if _, err := st.CreateInvitation(ctx, storage.NewInvitation{Kind: storage.InvitationAccess, CreatedBy: admin.ID}); err == nil {
		t.Fatal("CreateInvitation with an empty token = nil error, want a refusal")
	}

	max := 3
	exp := time.Now().Add(48 * time.Hour)
	first := mintAccess(t, st, admin, "link-one", func(n *storage.NewInvitation) {
		n.Label = "Friday group"
		n.PlatformKeys = true
		n.DiscordUserID = "  200000000000000001 "
		n.MaxUses = &max
		n.ExpiresAt = &exp
	})
	if first.Kind != storage.InvitationAccess || first.Label != "Friday group" || !first.PlatformKeys ||
		first.DiscordUserID != "200000000000000001" || first.MaxUses == nil || *first.MaxUses != 3 ||
		first.Uses != 0 || first.ExpiresAt == nil || first.RevokedAt != nil ||
		first.CreatedBy == nil || *first.CreatedBy != admin.ID || first.Rights == nil {
		t.Fatalf("minted invitation = %+v", first)
	}
	if !first.Live(time.Now()) {
		t.Fatal("a freshly minted invitation is not Live")
	}

	// The plaintext token is never persisted — a database read yields no link.
	if n := countAccessRows(t, pool, `SELECT count(*) FROM invitation WHERE token_hash = $1`, "link-one"); n != 0 {
		t.Fatal("the invitation token was stored in plaintext")
	}

	got, err := st.GetInvitationByToken(ctx, "link-one")
	if err != nil || got.ID != first.ID {
		t.Fatalf("GetInvitationByToken = %+v, %v, want the minted row", got, err)
	}
	for _, tok := range []string{"", "no-such-link"} {
		if _, err := st.GetInvitationByToken(ctx, tok); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("GetInvitationByToken(%q) = %v, want ErrNotFound", tok, err)
		}
	}

	// A campaign invitation shares the table but never the access listing or
	// the access revoke.
	tenantID, err := st.CreateTenant(ctx, "Host")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	campaignID := insertCampaign(t, st, tenantID, "Saltmarsh")
	campaignInv, err := st.CreateInvitation(ctx, storage.NewInvitation{
		Kind: storage.InvitationCampaign, Token: "campaign-link", CampaignID: &campaignID,
		Rights: []string{"wiki.read"}, CreatedBy: admin.ID,
	})
	if err != nil {
		t.Fatalf("CreateInvitation(campaign): %v", err)
	}
	second := mintAccess(t, st, admin, "link-two", nil)

	list, err := st.ListAccessInvitations(ctx)
	if err != nil {
		t.Fatalf("ListAccessInvitations: %v", err)
	}
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("ListAccessInvitations = %+v, want [link-two, link-one] (newest first, access only)", list)
	}

	if err := st.RevokeAccessInvitation(ctx, first.ID); err != nil {
		t.Fatalf("RevokeAccessInvitation: %v", err)
	}
	revoked, _ := st.GetInvitationByToken(ctx, "link-one")
	if revoked.RevokedAt == nil || revoked.Live(time.Now()) {
		t.Fatalf("revoked invitation = %+v, want RevokedAt set and not Live", revoked)
	}
	// Revoking again is a no-op that keeps the original timestamp.
	if err := st.RevokeAccessInvitation(ctx, first.ID); err != nil {
		t.Fatalf("second RevokeAccessInvitation: %v", err)
	}
	again, _ := st.GetInvitationByToken(ctx, "link-one")
	if !again.RevokedAt.Equal(*revoked.RevokedAt) {
		t.Errorf("re-revoking moved revoked_at: %v -> %v", revoked.RevokedAt, again.RevokedAt)
	}
	for name, id := range map[string]uuid.UUID{"unknown id": uuid.New(), "campaign invitation": campaignInv.ID} {
		if err := st.RevokeAccessInvitation(ctx, id); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("RevokeAccessInvitation(%s) = %v, want ErrNotFound", name, err)
		}
	}
	// A revoked link stays in the listing so the console shows what happened.
	if list, _ := st.ListAccessInvitations(ctx); len(list) != 2 {
		t.Errorf("listing after revoke has %d rows, want 2", len(list))
	}
}

// TestInvitationLive pins the liveness verdict the join preview and the
// redemption share.
func TestInvitationLive(t *testing.T) {
	t.Parallel()
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(time.Minute)
	one := 1
	cases := map[string]struct {
		inv  storage.Invitation
		want bool
	}{
		"plain":          {storage.Invitation{}, true},
		"revoked":        {storage.Invitation{RevokedAt: &past}, false},
		"expired":        {storage.Invitation{ExpiresAt: &past}, false},
		"expires now":    {storage.Invitation{ExpiresAt: &now}, false},
		"not yet expiry": {storage.Invitation{ExpiresAt: &future}, true},
		"uses left":      {storage.Invitation{MaxUses: &one}, true},
		"used up":        {storage.Invitation{MaxUses: &one, Uses: 1}, false},
	}
	for name, tc := range cases {
		if got := tc.inv.Live(now); got != tc.want {
			t.Errorf("%s: Live = %v, want %v", name, got, tc.want)
		}
	}
}

// TestRedeemAccessInvitation pins redemption: a live link becomes exactly one
// Access Grant and one counted use; anyone already admitted consumes nothing;
// and every way a link can be unusable answers the single ErrInvitationInvalid.
func TestRedeemAccessInvitation(t *testing.T) {
	st, pool := migratedWithPool(t)
	ctx := context.Background()
	admin := accessAdmin(t, st)

	inv := mintAccess(t, st, admin, "open-link", func(n *storage.NewInvitation) {
		n.Label = "Con crew"
		n.PlatformKeys = true
	})

	g, err := st.RedeemAccessInvitation(ctx, "open-link", "300000000000000001")
	if err != nil {
		t.Fatalf("RedeemAccessInvitation: %v", err)
	}
	if g.DiscordUserID != "300000000000000001" || !g.PlatformKeys || g.Note != "Con crew" ||
		g.InvitationID == nil || *g.InvitationID != inv.ID {
		t.Fatalf("grant from invitation = %+v", g)
	}
	if got, err := st.GetAccessGrant(ctx, "300000000000000001"); err != nil || got.ID != g.ID {
		t.Fatalf("GetAccessGrant = %+v, %v, want the redeemed grant", got, err)
	}
	if uses := invitationUses(t, st, "open-link"); uses != 1 {
		t.Fatalf("uses after first redeem = %d, want 1", uses)
	}

	t.Run("opening the link twice consumes no second use", func(t *testing.T) {
		again, err := st.RedeemAccessInvitation(ctx, "open-link", "300000000000000001")
		if err != nil || again.ID != g.ID {
			t.Fatalf("second redeem = %+v, %v, want the same grant", again, err)
		}
		if uses := invitationUses(t, st, "open-link"); uses != 1 {
			t.Errorf("uses after re-opening = %d, want 1", uses)
		}
	})

	t.Run("an already granted user consumes no use and keeps their grant", func(t *testing.T) {
		direct, err := st.GrantAccess(ctx, "300000000000000002", "by hand", false, admin.ID)
		if err != nil {
			t.Fatalf("GrantAccess: %v", err)
		}
		got, err := st.RedeemAccessInvitation(ctx, "open-link", "300000000000000002")
		if err != nil {
			t.Fatalf("redeem by a grant holder: %v", err)
		}
		if got.ID != direct.ID || got.PlatformKeys || got.Note != "by hand" || got.InvitationID != nil {
			t.Errorf("grant holder's grant changed on redeem: %+v", got)
		}
		if uses := invitationUses(t, st, "open-link"); uses != 1 {
			t.Errorf("uses after a grant holder opened the link = %d, want 1", uses)
		}
	})

	t.Run("every unusable link is ErrInvitationInvalid", func(t *testing.T) {
		past := time.Now().Add(-time.Minute)
		one := 1
		dead := mintAccess(t, st, admin, "revoked-link", nil)
		if err := st.RevokeAccessInvitation(ctx, dead.ID); err != nil {
			t.Fatalf("revoke: %v", err)
		}
		mintAccess(t, st, admin, "expired-link", func(n *storage.NewInvitation) { n.ExpiresAt = &past })
		mintAccess(t, st, admin, "single-link", func(n *storage.NewInvitation) { n.MaxUses = &one })
		if _, err := st.RedeemAccessInvitation(ctx, "single-link", "300000000000000010"); err != nil {
			t.Fatalf("first redeem of the single-use link: %v", err)
		}
		mintAccess(t, st, admin, "pinned-link", func(n *storage.NewInvitation) { n.DiscordUserID = "300000000000000020" })

		tenantID, err := st.CreateTenant(ctx, "Host")
		if err != nil {
			t.Fatalf("CreateTenant: %v", err)
		}
		campaignID := insertCampaign(t, st, tenantID, "Saltmarsh")
		if _, err := st.CreateInvitation(ctx, storage.NewInvitation{
			Kind: storage.InvitationCampaign, Token: "campaign-link", CampaignID: &campaignID, CreatedBy: admin.ID,
		}); err != nil {
			t.Fatalf("CreateInvitation(campaign): %v", err)
		}

		for name, token := range map[string]string{
			"unknown token":       "never-minted",
			"empty token":         "",
			"revoked":             "revoked-link",
			"expired":             "expired-link",
			"exhausted":           "single-link",
			"pinned to another":   "pinned-link",
			"campaign invitation": "campaign-link",
		} {
			const redeemer = "300000000000000011"
			if _, err := st.RedeemAccessInvitation(ctx, token, redeemer); !errors.Is(err, storage.ErrInvitationInvalid) {
				t.Errorf("%s: RedeemAccessInvitation = %v, want ErrInvitationInvalid", name, err)
			}
			if _, err := st.GetAccessGrant(ctx, redeemer); !errors.Is(err, storage.ErrNotFound) {
				t.Errorf("%s: a refused redemption left a grant behind (%v)", name, err)
			}
		}
		if uses := invitationUses(t, st, "single-link"); uses != 1 {
			t.Errorf("refused redemptions moved the exhausted link's counter to %d", uses)
		}
		if uses := invitationUses(t, st, "pinned-link"); uses != 0 {
			t.Errorf("a refused redeemer counted a use on the pinned link (%d)", uses)
		}

		// The pinned Discord User themself is admitted.
		if _, err := st.RedeemAccessInvitation(ctx, "pinned-link", "300000000000000020"); err != nil {
			t.Errorf("the pinned user was refused their own link: %v", err)
		}
	})

	t.Run("the last use is never shared", func(t *testing.T) {
		one := 1
		mintAccess(t, st, admin, "race-link", func(n *storage.NewInvitation) { n.MaxUses = &one })
		const racers = 8
		errs := make([]error, racers)
		var wg sync.WaitGroup
		for i := range racers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = st.RedeemAccessInvitation(ctx, "race-link", fmt.Sprintf("40000000000000000%d", i))
			}()
		}
		wg.Wait()
		won := 0
		for i, err := range errs {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, storage.ErrInvitationInvalid):
				t.Errorf("racer %d: %v, want nil or ErrInvitationInvalid", i, err)
			}
		}
		if won != 1 {
			t.Errorf("%d racers redeemed a single-use link, want exactly 1", won)
		}
		if uses := invitationUses(t, st, "race-link"); uses != 1 {
			t.Errorf("uses = %d, want exactly 1", uses)
		}
		if n := countAccessRows(t, pool,
			`SELECT count(*) FROM access_grant WHERE discord_user_id LIKE '40000000000000000%'`); n != 1 {
			t.Errorf("%d grants created from a single-use link, want 1", n)
		}
	})

	t.Run("one person racing two links gets one grant and spends one use", func(t *testing.T) {
		mintAccess(t, st, admin, "twin-a", nil)
		mintAccess(t, st, admin, "twin-b", nil)
		const who = "500000000000000001"
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, tok := range []string{"twin-a", "twin-b"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = st.RedeemAccessInvitation(ctx, tok, who)
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("racer %d: %v, want both redemptions to succeed", i, err)
			}
		}
		if total := invitationUses(t, st, "twin-a") + invitationUses(t, st, "twin-b"); total != 1 {
			t.Errorf("uses across both links = %d, want 1", total)
		}
	})

	t.Run("a returning operator's Tenant follows the new grant", func(t *testing.T) {
		const who = "600000000000000001"
		on := true
		founded := foundTenant(t, st, who, "tok-returning", &on)
		mintAccess(t, st, admin, "keys-off-link", nil) // platform_keys false
		if _, err := st.RedeemAccessInvitation(ctx, "keys-off-link", who); err != nil {
			t.Fatalf("redeem: %v", err)
		}
		if ok, err := st.TenantPlatformKeys(ctx, founded.Tenant.ID); err != nil || ok {
			t.Errorf("TenantPlatformKeys after redeeming a keys-off link = %v, %v, want false", ok, err)
		}
	})
}

// TestGrantAndRevokeAccess pins the direct Platform Admin actions: granting is
// an upsert that an existing Tenant follows, and revoking signs the person out
// everywhere while keeping everything they built.
func TestGrantAndRevokeAccess(t *testing.T) {
	st, pool := migratedWithPool(t)
	ctx := context.Background()
	admin := accessAdmin(t, st)
	const who = "300000000000000001"

	if _, err := st.GetAccessGrant(ctx, who); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetAccessGrant before granting = %v, want ErrNotFound", err)
	}
	if err := st.RevokeAccess(ctx, who); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("RevokeAccess without a grant = %v, want ErrNotFound", err)
	}

	first, err := st.GrantAccess(ctx, who, "plays Fridays", false, admin.ID)
	if err != nil {
		t.Fatalf("GrantAccess: %v", err)
	}
	if first.DiscordUserID != who || first.Note != "plays Fridays" || first.PlatformKeys || first.InvitationID != nil {
		t.Fatalf("grant = %+v", first)
	}

	// The granted person signs in and founds their Tenant with the grant's choice.
	founded := foundTenant(t, st, who, "tok-granted", &first.PlatformKeys)
	if ok, err := st.TenantPlatformKeys(ctx, founded.Tenant.ID); err != nil || ok {
		t.Fatalf("founded tenant platform_keys = %v, %v, want false (the grant's value)", ok, err)
	}

	// Granting again updates in place and the existing Tenant follows.
	second, err := st.GrantAccess(ctx, who, "now trusted", true, admin.ID)
	if err != nil {
		t.Fatalf("GrantAccess again: %v", err)
	}
	if second.ID != first.ID || second.Note != "now trusted" || !second.PlatformKeys {
		t.Fatalf("re-grant = %+v, want the same row updated", second)
	}
	if ok, err := st.TenantPlatformKeys(ctx, founded.Tenant.ID); err != nil || !ok {
		t.Fatalf("tenant platform_keys after re-grant = %v, %v, want true", ok, err)
	}

	// A bystander with a session of their own must be untouched by the revoke.
	bystander := foundTenant(t, st, "300000000000000002", "tok-bystander", nil)
	if _, err := st.CreateSession(ctx, storage.NewSession{
		UserID: founded.User.ID, Token: "tok-granted-2", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("second session: %v", err)
	}

	if err := st.RevokeAccess(ctx, who); err != nil {
		t.Fatalf("RevokeAccess: %v", err)
	}
	if _, err := st.GetAccessGrant(ctx, who); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("grant survived the revoke: %v", err)
	}
	for _, tok := range []string{"tok-granted", "tok-granted-2"} {
		if _, err := st.AuthenticateSession(ctx, tok); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("AuthenticateSession(%s) after revoke = %v, want ErrNotFound", tok, err)
		}
	}
	if n := countAccessRows(t, pool, `SELECT count(*) FROM sessions WHERE user_id = $1`, founded.User.ID); n != 0 {
		t.Errorf("%d session rows left for the revoked user, want 0", n)
	}
	if _, err := st.AuthenticateSession(ctx, "tok-bystander"); err != nil {
		t.Errorf("the revoke signed out a bystander: %v", err)
	}
	// Tenant and user are kept: re-granting restores everything.
	if tid, err := st.TenantForUser(ctx, founded.User.ID); err != nil || tid != founded.Tenant.ID {
		t.Errorf("TenantForUser after revoke = %s, %v, want the kept tenant", tid, err)
	}
	if _, err := st.GetTenant(ctx, bystander.Tenant.ID); err != nil {
		t.Errorf("bystander tenant: %v", err)
	}

	// Re-granting with Platform Keys off turns the kept Tenant's switch off —
	// it must not silently keep what it had before the revoke.
	if _, err := st.GrantAccess(ctx, who, "back", false, admin.ID); err != nil {
		t.Fatalf("re-grant after revoke: %v", err)
	}
	if ok, err := st.TenantPlatformKeys(ctx, founded.Tenant.ID); err != nil || ok {
		t.Errorf("tenant platform_keys after a keys-off re-grant = %v, %v, want false", ok, err)
	}
}

// TestTenantPlatformKeys pins the per-Tenant switch: it defaults on (every
// pre-existing Tenant is unchanged), an unknown Tenant reads off (the gate
// fails closed), and setting it keeps the operator's grant in step.
func TestTenantPlatformKeys(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()
	admin := accessAdmin(t, st)

	seed, err := st.CreateTenant(ctx, "Seed")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if ok, err := st.TenantPlatformKeys(ctx, seed); err != nil || !ok {
		t.Fatalf("TenantPlatformKeys(default) = %v, %v, want true", ok, err)
	}
	if ok, err := st.TenantPlatformKeys(ctx, uuid.New()); err != nil || ok {
		t.Fatalf("TenantPlatformKeys(unknown) = %v, %v, want false, nil", ok, err)
	}
	if err := st.SetTenantPlatformKeys(ctx, uuid.New(), true); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("SetTenantPlatformKeys(unknown) = %v, want ErrNotFound", err)
	}

	// A Tenant with no grant behind it (the seed) flips on its own.
	if err := st.SetTenantPlatformKeys(ctx, seed, false); err != nil {
		t.Fatalf("SetTenantPlatformKeys(seed, false): %v", err)
	}
	if ok, _ := st.TenantPlatformKeys(ctx, seed); ok {
		t.Error("seed tenant still has platform keys after switching them off")
	}

	const who = "300000000000000001"
	grant, err := st.GrantAccess(ctx, who, "", false, admin.ID)
	if err != nil {
		t.Fatalf("GrantAccess: %v", err)
	}
	founded := foundTenant(t, st, who, "tok-keys", &grant.PlatformKeys)
	if _, err := st.GrantAccess(ctx, "300000000000000002", "", false, admin.ID); err != nil {
		t.Fatalf("GrantAccess(other): %v", err)
	}

	if err := st.SetTenantPlatformKeys(ctx, founded.Tenant.ID, true); err != nil {
		t.Fatalf("SetTenantPlatformKeys(true): %v", err)
	}
	if ok, _ := st.TenantPlatformKeys(ctx, founded.Tenant.ID); !ok {
		t.Error("tenant platform keys not switched on")
	}
	if g, _ := st.GetAccessGrant(ctx, who); !g.PlatformKeys {
		t.Error("the operator's grant did not follow the tenant switch")
	}
	if g, _ := st.GetAccessGrant(ctx, "300000000000000002"); g.PlatformKeys {
		t.Error("another person's grant was switched on by an unrelated tenant")
	}
	if ok, _ := st.TenantPlatformKeys(ctx, seed); ok {
		t.Error("an unrelated tenant was switched on")
	}
}

// TestListAccessPeople pins the row shapes of the Platform Admin's people
// list: a grant nobody has used, a signed-in user without a grant, an operator
// with a Tenant, a Campaign Guest, and the synthetic dev operator.
func TestListAccessPeople(t *testing.T) {
	st, pool := migratedWithPool(t)
	ctx := context.Background()
	admin := accessAdmin(t, st)

	// (1) a grant created through a link, never used to sign in.
	mintAccess(t, st, admin, "people-link", func(n *storage.NewInvitation) { n.Label = "Con crew" })
	if _, err := st.RedeemAccessInvitation(ctx, "people-link", "300000000000000001"); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	// (2) a signed-in user with neither grant nor Tenant, who is a Campaign Guest.
	guest, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: "300000000000000002", Name: "Guest Gil", Avatar: "https://cdn/g.png"})
	if err != nil {
		t.Fatalf("upsert guest: %v", err)
	}
	// (3) a granted operator with a Tenant, two Campaigns and a session.
	if _, err := st.GrantAccess(ctx, "300000000000000003", "the GM", true, admin.ID); err != nil {
		t.Fatalf("GrantAccess: %v", err)
	}
	on := true
	gm := foundTenant(t, st, "300000000000000003", "tok-gm", &on)
	c1 := insertCampaign(t, st, gm.Tenant.ID, "One")
	c2 := insertCampaign(t, st, gm.Tenant.ID, "Two")
	for _, c := range []uuid.UUID{c1, c2} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO campaign_guest (campaign_id, user_id, rights) VALUES ($1, $2, '{wiki.read}')`, c, guest.ID); err != nil {
			t.Fatalf("insert campaign_guest: %v", err)
		}
	}
	// (4) the dev operator.
	if _, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: storage.DevOperatorDiscordID, Name: "Dev"}); err != nil {
		t.Fatalf("upsert dev operator: %v", err)
	}
	if err := st.SetUserSuspended(ctx, "300000000000000002", true); err != nil {
		t.Fatalf("SetUserSuspended: %v", err)
	}

	people, err := st.ListAccessPeople(ctx, false)
	if err != nil {
		t.Fatalf("ListAccessPeople: %v", err)
	}
	byID := map[string]storage.AccessPerson{}
	for _, p := range people {
		if _, dup := byID[p.DiscordUserID]; dup {
			t.Errorf("%s listed twice", p.DiscordUserID)
		}
		byID[p.DiscordUserID] = p
	}
	if _, ok := byID[storage.DevOperatorDiscordID]; ok {
		t.Error("the dev operator is listed without includeDev")
	}
	if len(people) != 4 {
		t.Errorf("listed %d people, want 4 (admin, unused grant, guest, GM)", len(people))
	}

	unused := byID["300000000000000001"]
	if unused.Known || unused.Name != "" || !unused.HasGrant || unused.GrantedAt == nil ||
		unused.GrantNote != "Con crew" || unused.InvitationLabel != "Con crew" ||
		unused.JoinedAt != nil || unused.TenantID != nil || unused.GuestCampaignCount != 0 {
		t.Errorf("unused grant row = %+v", unused)
	}

	g := byID["300000000000000002"]
	if !g.Known || g.Name != "Guest Gil" || g.Avatar != "https://cdn/g.png" || g.HasGrant ||
		g.GrantedAt != nil || g.TenantID != nil || g.JoinedAt == nil || g.SuspendedAt == nil ||
		g.GuestCampaignCount != 2 || g.LastSeenAt != nil {
		t.Errorf("guest row = %+v", g)
	}

	op := byID["300000000000000003"]
	if !op.Known || !op.HasGrant || op.GrantNote != "the GM" || !op.GrantPlatformKeys ||
		op.InvitationLabel != "" || op.TenantID == nil || *op.TenantID != gm.Tenant.ID ||
		op.TenantName != gm.Tenant.Name || !op.TenantPlatformKeys || op.CampaignCount != 2 ||
		op.GuestCampaignCount != 0 || op.SuspendedAt != nil || op.LastSeenAt == nil {
		t.Errorf("operator row = %+v", op)
	}

	if a := byID[admin.DiscordUserID]; !a.Known || a.HasGrant || a.TenantID != nil {
		t.Errorf("admin row = %+v", a)
	}

	withDev, err := st.ListAccessPeople(ctx, true)
	if err != nil {
		t.Fatalf("ListAccessPeople(includeDev): %v", err)
	}
	found := false
	for _, p := range withDev {
		found = found || p.DiscordUserID == storage.DevOperatorDiscordID
	}
	if !found || len(withDev) != 5 {
		t.Errorf("includeDev listed %d people (dev present: %v), want 5 with the dev operator", len(withDev), found)
	}
}

// TestProvisionSignupWithoutPlan pins the ADR-0065 shape of the provisioning
// transaction: an Access Grant holder founds a Tenant with NO Plan bind (an
// allowlist deployment has zero Plan rows) and the grant's Platform Keys
// choice; a nil choice leaves the column default; and neither ever rewrites a
// Tenant that already exists.
func TestProvisionSignupWithoutPlan(t *testing.T) {
	st, pool := migratedWithPool(t)
	ctx := context.Background()

	off := false
	res := foundTenant(t, st, "777101", "tok-off", &off)
	if !res.Created {
		t.Fatal("grant-shaped signup Created = false, want true")
	}
	if ok, err := st.TenantPlatformKeys(ctx, res.Tenant.ID); err != nil || ok {
		t.Fatalf("platform_keys = %v, %v, want false", ok, err)
	}
	if n := countAccessRows(t, pool, `SELECT count(*) FROM tenant_subscription WHERE tenant_id = $1`, res.Tenant.ID); n != 0 {
		t.Fatalf("a plan-less signup bound %d subscriptions, want 0", n)
	}
	if _, err := st.AuthenticateSession(ctx, "tok-off"); err != nil {
		t.Fatalf("minted session does not authenticate: %v", err)
	}

	on := true
	if res := foundTenant(t, st, "777102", "tok-on", &on); !res.Created {
		t.Fatal("second founder Created = false")
	} else if ok, _ := st.TenantPlatformKeys(ctx, res.Tenant.ID); !ok {
		t.Error("platform_keys = false, want true (the caller's choice)")
	}

	// nil = the column default, which is what open-mode signup relies on.
	if res := foundTenant(t, st, "777103", "tok-default", nil); !res.Created {
		t.Fatal("third founder Created = false")
	} else if ok, _ := st.TenantPlatformKeys(ctx, res.Tenant.ID); !ok {
		t.Error("platform_keys with a nil choice = false, want the column default true")
	}

	// A returning login founds nothing and must not rewrite the existing flag.
	again := foundTenant(t, st, "777101", "tok-off-2", &on)
	if again.Created || again.Tenant.ID != res.Tenant.ID {
		t.Fatalf("returning login = %+v, want the existing tenant, Created=false", again)
	}
	if ok, _ := st.TenantPlatformKeys(ctx, res.Tenant.ID); ok {
		t.Error("a returning login switched an existing tenant's platform keys on")
	}
}

// TestProvisionSignupOpenModeKeepsColumnDefault pins that the open-mode call
// shape (Plan set, no Platform Keys choice) is unchanged by ADR-0065: the Plan
// binds and tenant.platform_keys keeps its default.
func TestProvisionSignupOpenModeKeepsColumnDefault(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()
	if _, err := st.SyncPlans(ctx, billingSpecs(), false); err != nil {
		t.Fatalf("SyncPlans: %v", err)
	}
	res, err := st.ProvisionSignup(ctx, signupParams("777201", "tok-open"))
	if err != nil {
		t.Fatalf("ProvisionSignup: %v", err)
	}
	if sub, err := st.ActiveSubscription(ctx, res.Tenant.ID); err != nil || sub.PlanSlug != "byok-free" {
		t.Fatalf("ActiveSubscription = %+v, %v, want byok-free", sub, err)
	}
	if ok, err := st.TenantPlatformKeys(ctx, res.Tenant.ID); err != nil || !ok {
		t.Fatalf("open-mode tenant platform_keys = %v, %v, want the column default true", ok, err)
	}
}

// TestRevokeSessionsOutsideAllowlistSparesGrantsAndGuests pins the ADR-0065
// boot sweep: Access Grant holders and Campaign Guests are admitted from the
// web, so a restart must not sign them out — while everyone else off the
// Platform Admin list still goes, including someone whose grant was removed.
func TestRevokeSessionsOutsideAllowlistSparesGrantsAndGuests(t *testing.T) {
	st, pool := migratedWithPool(t)
	ctx := context.Background()
	admin := accessAdmin(t, st)

	mk := func(discordID, token string) storage.User {
		t.Helper()
		u, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: discordID})
		if err != nil {
			t.Fatalf("upsert %s: %v", discordID, err)
		}
		if _, err := st.CreateSession(ctx, storage.NewSession{
			UserID: u.ID, Token: token, ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("CreateSession(%s): %v", token, err)
		}
		return u
	}
	mk(admin.DiscordUserID, "tok-admin")
	mk("300000000000000001", "tok-granted")
	guest := mk("300000000000000002", "tok-guest")
	mk("300000000000000003", "tok-stranger")
	mk("300000000000000004", "tok-ex-granted")

	for _, id := range []string{"300000000000000001", "300000000000000004"} {
		if _, err := st.GrantAccess(ctx, id, "", false, admin.ID); err != nil {
			t.Fatalf("GrantAccess(%s): %v", id, err)
		}
	}
	// Removing the grant row directly (not RevokeAccess, which would delete
	// the session itself) leaves the sweep as the only thing that can evict.
	if _, err := pool.Exec(ctx, `DELETE FROM access_grant WHERE discord_user_id = $1`, "300000000000000004"); err != nil {
		t.Fatalf("delete grant: %v", err)
	}
	tenantID, err := st.CreateTenant(ctx, "Host")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	campaignID := insertCampaign(t, st, tenantID, "Saltmarsh")
	if _, err := pool.Exec(ctx,
		`INSERT INTO campaign_guest (campaign_id, user_id) VALUES ($1, $2)`, campaignID, guest.ID); err != nil {
		t.Fatalf("insert campaign_guest: %v", err)
	}

	revoked, err := st.RevokeSessionsOutsideAllowlist(ctx, []string{admin.DiscordUserID})
	if err != nil {
		t.Fatalf("RevokeSessionsOutsideAllowlist: %v", err)
	}
	if revoked != 2 {
		t.Errorf("revoked = %d sessions, want 2 (the stranger and the ex-granted user)", revoked)
	}
	for _, tok := range []string{"tok-admin", "tok-granted", "tok-guest"} {
		if _, err := st.AuthenticateSession(ctx, tok); err != nil {
			t.Errorf("%s was signed out by the sweep: %v", tok, err)
		}
	}
	for _, tok := range []string{"tok-stranger", "tok-ex-granted"} {
		if _, err := st.AuthenticateSession(ctx, tok); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("AuthenticateSession(%s) = %v, want ErrNotFound", tok, err)
		}
	}
}
