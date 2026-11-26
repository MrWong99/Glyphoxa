//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Campaign Guests against a real Postgres (ADR-0065). The property under test
// throughout is the narrow one a guest is confined to: ONE Campaign, the
// rights its GM granted, and nothing of the rest of the host Tenant.

// guestWorld is the fixture every test starts from: a host GM with their
// Tenant and two Campaigns, and the store.
type guestWorld struct {
	st   *storage.Store
	pool *pgxpool.Pool
	host storage.User
	// tenant is the host Tenant; alpha and beta are two of its Campaigns.
	tenant      uuid.UUID
	alpha, beta uuid.UUID
}

const (
	hostDiscordID  = "200000000000000001"
	guestDiscordID = "200000000000000002"
	otherDiscordID = "200000000000000003"
)

func newGuestWorld(t *testing.T) guestWorld {
	t.Helper()
	st, pool := migratedWithPool(t)
	host := foundTenant(t, st, hostDiscordID, "tok-host", nil)
	return guestWorld{
		st: st, pool: pool, host: host.User, tenant: host.Tenant.ID,
		alpha: insertCampaign(t, st, host.Tenant.ID, "Alpha"),
		beta:  insertCampaign(t, st, host.Tenant.ID, "Beta"),
	}
}

// signIn upserts a Discord User the way a real sign-in does (a name) and
// gives them a session.
func (w guestWorld) signIn(t *testing.T, discordID, name, token string) storage.User {
	t.Helper()
	ctx := context.Background()
	u, err := w.st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: discordID, Name: name, Avatar: "https://cdn/" + name + ".png"})
	if err != nil {
		t.Fatalf("upsert %s: %v", discordID, err)
	}
	if _, err := w.st.CreateSession(ctx, storage.NewSession{UserID: u.ID, Token: token, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("CreateSession(%s): %v", token, err)
	}
	return u
}

func (w guestWorld) addGuest(t *testing.T, campaignID uuid.UUID, discordID string, rights ...string) storage.CampaignGuest {
	t.Helper()
	g, err := w.st.AddCampaignGuest(context.Background(), campaignID, discordID, rights, w.host.ID)
	if err != nil {
		t.Fatalf("AddCampaignGuest(%s): %v", discordID, err)
	}
	return g
}

func (w guestWorld) mintCampaign(t *testing.T, campaignID uuid.UUID, token string, tweak func(*storage.NewInvitation)) storage.Invitation {
	t.Helper()
	n := storage.NewInvitation{Kind: storage.InvitationCampaign, Token: token, CampaignID: &campaignID, CreatedBy: w.host.ID}
	if tweak != nil {
		tweak(&n)
	}
	inv, err := w.st.CreateInvitation(context.Background(), n)
	if err != nil {
		t.Fatalf("CreateInvitation(%s): %v", token, err)
	}
	return inv
}

func sortedRights(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

func redemption(discordID, name string) storage.CampaignRedemption {
	return storage.CampaignRedemption{
		User:          storage.UpsertUserParams{DiscordUserID: discordID, Name: name, Avatar: "https://cdn/" + name + ".png"},
		AUPAcceptedAt: time.Now(),
	}
}

// TestCampaignGuestCRUD covers add/replace by snowflake, update, remove and
// list — each scoped to one Campaign.
func TestCampaignGuestCRUD(t *testing.T) {
	w := newGuestWorld(t)
	ctx := context.Background()

	// Someone who has never signed in gets a placeholder users row.
	g := w.addGuest(t, w.alpha, " "+guestDiscordID+" ", "wiki.read", "maps.read")
	if g.CampaignID != w.alpha || g.DiscordUserID != guestDiscordID || g.Name != "" || g.Avatar != "" || g.HasSignedIn {
		t.Fatalf("guest added by snowflake = %+v, want a nameless not-signed-in guest of alpha", g)
	}
	if !slices.Equal(g.Rights, []string{"wiki.read", "maps.read"}) || g.AddedAt.IsZero() {
		t.Errorf("rights = %v addedAt = %v", g.Rights, g.AddedAt)
	}
	// No Tenant, no session and no Access Grant came with it.
	if n := countAccessRows(t, w.pool, `SELECT count(*) FROM tenant WHERE operator_user_id = $1`, g.UserID); n != 0 {
		t.Errorf("adding a guest created %d tenants", n)
	}
	if n := countAccessRows(t, w.pool, `SELECT count(*) FROM access_grant WHERE discord_user_id = $1`, guestDiscordID); n != 0 {
		t.Errorf("adding a guest created %d access grants", n)
	}

	// Their first sign-in fills the name in.
	signed := w.signIn(t, guestDiscordID, "Mika", "tok-guest")
	if signed.ID != g.UserID {
		t.Fatalf("sign-in created a second users row: %s vs %s", signed.ID, g.UserID)
	}
	got, err := w.st.GetCampaignGuest(ctx, w.alpha, g.UserID)
	if err != nil || got.Name != "Mika" || !got.HasSignedIn {
		t.Fatalf("after sign-in = %+v, %v; want the name and has_signed_in", got, err)
	}

	// Adding an EXISTING user must not clobber their name or avatar, and
	// adding an existing guest replaces the rights.
	replaced := w.addGuest(t, w.alpha, guestDiscordID, "transcripts.read")
	if replaced.UserID != g.UserID || replaced.Name != "Mika" || replaced.Avatar != "https://cdn/Mika.png" {
		t.Errorf("re-adding clobbered the user: %+v", replaced)
	}
	if !slices.Equal(replaced.Rights, []string{"transcripts.read"}) {
		t.Errorf("re-added rights = %v, want them REPLACED", replaced.Rights)
	}
	if n := countAccessRows(t, w.pool, `SELECT count(*) FROM campaign_guest WHERE campaign_id = $1`, w.alpha); n != 1 {
		t.Errorf("campaign_guest rows for alpha = %d, want 1", n)
	}

	// The Tenant's own operator cannot be made a guest of its Campaigns…
	if _, err := w.st.AddCampaignGuest(ctx, w.alpha, hostDiscordID, []string{"wiki.read"}, w.host.ID); !errors.Is(err, storage.ErrGuestIsOperator) {
		t.Errorf("adding the operator = %v, want ErrGuestIsOperator", err)
	}
	if n := countAccessRows(t, w.pool, `SELECT count(*) FROM campaign_guest WHERE user_id = $1`, w.host.ID); n != 0 {
		t.Errorf("the refused operator has %d guest rows", n)
	}
	// …and a campaign that does not exist is ErrNotFound, leaving no user behind.
	if _, err := w.st.AddCampaignGuest(ctx, uuid.New(), "200000000000000099", nil, w.host.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("adding to an unknown campaign = %v, want ErrNotFound", err)
	}
	if n := countAccessRows(t, w.pool, `SELECT count(*) FROM users WHERE discord_user_id = '200000000000000099'`); n != 0 {
		t.Error("a refused add left a placeholder users row behind")
	}

	// Update: scoped to the campaign. The guest is a guest of alpha only.
	upd, err := w.st.UpdateCampaignGuest(ctx, w.alpha, g.UserID, []string{"wiki.read", "wiki.secrets"})
	if err != nil || !slices.Equal(upd.Rights, []string{"wiki.read", "wiki.secrets"}) {
		t.Fatalf("UpdateCampaignGuest = %+v, %v", upd, err)
	}
	if _, err := w.st.UpdateCampaignGuest(ctx, w.beta, g.UserID, []string{"planning.use"}); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("updating through ANOTHER campaign = %v, want ErrNotFound", err)
	}
	// An empty set is allowed and stored as an empty array, not NULL.
	empty, err := w.st.UpdateCampaignGuest(ctx, w.alpha, g.UserID, nil)
	if err != nil || empty.Rights == nil || len(empty.Rights) != 0 {
		t.Fatalf("clearing rights = %+v, %v; want an empty, non-nil set", empty.Rights, err)
	}

	// List: per campaign, oldest first, with display fields.
	other := w.addGuest(t, w.alpha, otherDiscordID, "maps.read")
	w.addGuest(t, w.beta, otherDiscordID, "highlights.read")
	list, err := w.st.ListCampaignGuests(ctx, w.alpha)
	if err != nil || len(list) != 2 || list[0].UserID != g.UserID || list[1].UserID != other.UserID {
		t.Fatalf("ListCampaignGuests(alpha) = %+v, %v", list, err)
	}
	if !list[0].HasSignedIn || list[1].HasSignedIn || list[0].Name != "Mika" {
		t.Errorf("list display fields = %+v", list)
	}
	if betaList, err := w.st.ListCampaignGuests(ctx, w.beta); err != nil || len(betaList) != 1 || betaList[0].UserID != other.UserID {
		t.Fatalf("ListCampaignGuests(beta) = %+v, %v; want only beta's guest", betaList, err)
	}

	// Remove: scoped, and it drops the session selection pointing at it.
	if err := w.st.SetSessionWorkspace(ctx, g.UserID, "tok-guest", &w.alpha); err != nil {
		t.Fatalf("SetSessionWorkspace: %v", err)
	}
	if err := w.st.RemoveCampaignGuest(ctx, w.beta, g.UserID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("removing through ANOTHER campaign = %v, want ErrNotFound", err)
	}
	if err := w.st.RemoveCampaignGuest(ctx, w.alpha, g.UserID); err != nil {
		t.Fatalf("RemoveCampaignGuest: %v", err)
	}
	if _, err := w.st.GetCampaignGuest(ctx, w.alpha, g.UserID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("after removal GetCampaignGuest = %v, want ErrNotFound", err)
	}
	if n := countAccessRows(t, w.pool, `SELECT count(*) FROM sessions WHERE user_id = $1 AND guest_campaign_id IS NOT NULL`, g.UserID); n != 0 {
		t.Error("removing a guest left their session pointing at the campaign")
	}
	if err := w.st.RemoveCampaignGuest(ctx, w.alpha, g.UserID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("removing twice = %v, want ErrNotFound", err)
	}
	// The users row and their session survive: removal ends the guest access,
	// not the account.
	if _, err := w.st.AuthenticateSession(ctx, "tok-guest"); err != nil {
		t.Errorf("removal signed the user out: %v", err)
	}
}

// TestCampaignInvitationsScopedToCampaign: a GM lists and revokes only the
// links of the Campaign they are managing.
func TestCampaignInvitationsScopedToCampaign(t *testing.T) {
	w := newGuestWorld(t)
	ctx := context.Background()
	admin := accessAdmin(t, w.st)

	a1 := w.mintCampaign(t, w.alpha, "alpha-1", func(n *storage.NewInvitation) { n.Rights = []string{"wiki.read"}; n.Label = "players" })
	a2 := w.mintCampaign(t, w.alpha, "alpha-2", nil)
	b1 := w.mintCampaign(t, w.beta, "beta-1", nil)
	access := mintAccess(t, w.st, admin, "access-1", nil)

	list, err := w.st.ListCampaignInvitations(ctx, w.alpha)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListCampaignInvitations(alpha) = %+v, %v; want alpha's two links", list, err)
	}
	ids := []uuid.UUID{list[0].ID, list[1].ID}
	if !slices.Contains(ids, a1.ID) || !slices.Contains(ids, a2.ID) {
		t.Errorf("alpha's list = %v, want %s and %s", ids, a1.ID, a2.ID)
	}
	for _, inv := range list {
		if inv.Kind != storage.InvitationCampaign || inv.CampaignID == nil || *inv.CampaignID != w.alpha {
			t.Errorf("alpha's list carries %+v", inv)
		}
	}
	// The access console never lists campaign links.
	if accessList, err := w.st.ListAccessInvitations(ctx); err != nil || len(accessList) != 1 || accessList[0].ID != access.ID {
		t.Errorf("ListAccessInvitations = %+v, %v; want only the access link", accessList, err)
	}

	// Revoking through the wrong campaign, or an access link, is ErrNotFound.
	for name, try := range map[string]func() error{
		"beta's link through alpha":   func() error { return w.st.RevokeCampaignInvitation(ctx, w.alpha, b1.ID) },
		"an access link":              func() error { return w.st.RevokeCampaignInvitation(ctx, w.alpha, access.ID) },
		"an unknown id":               func() error { return w.st.RevokeCampaignInvitation(ctx, w.alpha, uuid.New()) },
		"a campaign link as access":   func() error { return w.st.RevokeAccessInvitation(ctx, a1.ID) },
		"alpha's link through a void": func() error { return w.st.RevokeCampaignInvitation(ctx, uuid.New(), a1.ID) },
	} {
		if err := try(); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("revoking %s = %v, want ErrNotFound", name, err)
		}
	}
	for _, tok := range []string{"alpha-1", "beta-1", "access-1"} {
		if inv, err := w.st.GetInvitationByToken(ctx, tok); err != nil || inv.RevokedAt != nil {
			t.Errorf("%s was revoked by a refused call (%+v, %v)", tok, inv.RevokedAt, err)
		}
	}

	if err := w.st.RevokeCampaignInvitation(ctx, w.alpha, a1.ID); err != nil {
		t.Fatalf("RevokeCampaignInvitation: %v", err)
	}
	inv, _ := w.st.GetInvitationByToken(ctx, "alpha-1")
	if inv.RevokedAt == nil || inv.Live(time.Now()) {
		t.Errorf("after revoke = %+v, want revoked and not live", inv)
	}
	if _, err := w.st.RedeemCampaignInvitation(ctx, "alpha-1", redemption(guestDiscordID, "Mika")); !errors.Is(err, storage.ErrInvitationInvalid) {
		t.Errorf("redeeming a revoked link = %v, want ErrInvitationInvalid", err)
	}
}

// TestRedeemCampaignInvitation covers the redemption: it creates the user and
// the guest row and nothing else, unions rights for an existing guest, counts
// a use only when something changed, and refuses everything it should.
func TestRedeemCampaignInvitation(t *testing.T) {
	w := newGuestWorld(t)
	ctx := context.Background()
	tenantsBefore := countAccessRows(t, w.pool, `SELECT count(*) FROM tenant`)

	inv := w.mintCampaign(t, w.alpha, "open-link", func(n *storage.NewInvitation) {
		n.Rights = []string{"wiki.read", "maps.read"}
	})
	res, err := w.st.RedeemCampaignInvitation(ctx, "open-link", redemption(guestDiscordID, "Mika"))
	if err != nil {
		t.Fatalf("RedeemCampaignInvitation: %v", err)
	}
	if res.CampaignID != w.alpha || !res.Changed || res.User.DiscordUserID != guestDiscordID || res.User.Name != "Mika" {
		t.Fatalf("redeemed = %+v", res)
	}
	if res.User.AUPAcceptedAt == nil {
		t.Error("the acknowledgment time was not recorded on the user")
	}
	if got := sortedRights(res.Rights); !slices.Equal(got, []string{"maps.read", "wiki.read"}) {
		t.Errorf("rights = %v", got)
	}
	g, err := w.st.GetCampaignGuest(ctx, w.alpha, res.User.ID)
	if err != nil || !g.HasSignedIn || !slices.Equal(sortedRights(g.Rights), []string{"maps.read", "wiki.read"}) {
		t.Fatalf("guest row = %+v, %v", g, err)
	}
	if n := countAccessRows(t, w.pool, `SELECT count(*) FROM campaign_guest WHERE invitation_id = $1`, inv.ID); n != 1 {
		t.Errorf("guest rows attributed to the link = %d, want 1", n)
	}
	if invitationUses(t, w.st, "open-link") != 1 {
		t.Errorf("uses = %d, want 1", invitationUses(t, w.st, "open-link"))
	}
	// NEVER a Tenant, a session, an Access Grant or a Plan.
	if n := countAccessRows(t, w.pool, `SELECT count(*) FROM tenant`); n != tenantsBefore {
		t.Errorf("tenants went from %d to %d: a campaign redemption founded a Tenant", tenantsBefore, n)
	}
	if n := countAccessRows(t, w.pool, `SELECT count(*) FROM sessions WHERE user_id = $1`, res.User.ID); n != 0 {
		t.Errorf("the redemption created %d sessions; minting the session is the callback's job", n)
	}
	if n := countAccessRows(t, w.pool, `SELECT count(*) FROM access_grant`); n != 0 {
		t.Errorf("a campaign redemption created %d access grants", n)
	}

	t.Run("reopening the same link changes nothing and counts no use", func(t *testing.T) {
		again, err := w.st.RedeemCampaignInvitation(ctx, "open-link", redemption(guestDiscordID, "Mika"))
		if err != nil || again.Changed || again.CampaignID != w.alpha {
			t.Fatalf("second redemption = %+v, %v; want an unchanged success", again, err)
		}
		if invitationUses(t, w.st, "open-link") != 1 {
			t.Errorf("uses = %d after reopening, want 1", invitationUses(t, w.st, "open-link"))
		}
	})

	t.Run("an existing guest keeps the union of old and new rights", func(t *testing.T) {
		w.mintCampaign(t, w.alpha, "wider-link", func(n *storage.NewInvitation) {
			n.Rights = []string{"maps.read", "transcripts.read"}
		})
		wider, err := w.st.RedeemCampaignInvitation(ctx, "wider-link", redemption(guestDiscordID, "Mika"))
		if err != nil || !wider.Changed {
			t.Fatalf("widening redemption = %+v, %v", wider, err)
		}
		want := []string{"maps.read", "transcripts.read", "wiki.read"}
		if got := sortedRights(wider.Rights); !slices.Equal(got, want) {
			t.Errorf("returned rights = %v, want the union %v", got, want)
		}
		g, _ := w.st.GetCampaignGuest(ctx, w.alpha, res.User.ID)
		if got := sortedRights(g.Rights); !slices.Equal(got, want) {
			t.Errorf("stored rights = %v, want the union %v", got, want)
		}
		if invitationUses(t, w.st, "wider-link") != 1 {
			t.Errorf("uses of the widening link = %d, want 1", invitationUses(t, w.st, "wider-link"))
		}
		// A NARROWER link never takes rights away, and counts no use.
		w.mintCampaign(t, w.alpha, "narrow-link", func(n *storage.NewInvitation) { n.Rights = []string{"wiki.read"} })
		narrow, err := w.st.RedeemCampaignInvitation(ctx, "narrow-link", redemption(guestDiscordID, "Mika"))
		if err != nil || narrow.Changed || !slices.Equal(sortedRights(narrow.Rights), want) {
			t.Errorf("narrower redemption = %+v, %v; want unchanged rights %v", narrow, err, want)
		}
		if invitationUses(t, w.st, "narrow-link") != 0 {
			t.Error("a redemption that changed nothing counted a use")
		}
	})

	t.Run("a spent link still lets its own guest back in", func(t *testing.T) {
		one := 1
		w.mintCampaign(t, w.beta, "single", func(n *storage.NewInvitation) { n.MaxUses = &one; n.Rights = []string{"wiki.read"} })
		first, err := w.st.RedeemCampaignInvitation(ctx, "single", redemption(guestDiscordID, "Mika"))
		if err != nil || !first.Changed {
			t.Fatalf("first use = %+v, %v", first, err)
		}
		if _, err := w.st.RedeemCampaignInvitation(ctx, "single", redemption(otherDiscordID, "Noor")); !errors.Is(err, storage.ErrInvitationInvalid) {
			t.Errorf("a second person on a spent link = %v, want ErrInvitationInvalid", err)
		}
		back, err := w.st.RedeemCampaignInvitation(ctx, "single", redemption(guestDiscordID, "Mika"))
		if err != nil || back.Changed || back.CampaignID != w.beta {
			t.Errorf("the link's own guest reopening it = %+v, %v; want an unchanged success", back, err)
		}
		if invitationUses(t, w.st, "single") != 1 {
			t.Errorf("uses = %d, want 1", invitationUses(t, w.st, "single"))
		}
		if n := countAccessRows(t, w.pool, `SELECT count(*) FROM users WHERE discord_user_id = $1`, otherDiscordID); n != 0 {
			t.Error("a refused redemption left a users row behind")
		}
	})

	t.Run("refusals write nothing", func(t *testing.T) {
		past := time.Now().Add(-time.Minute)
		admin := accessAdmin(t, w.st)
		w.mintCampaign(t, w.alpha, "expired", func(n *storage.NewInvitation) { n.ExpiresAt = &past })
		w.mintCampaign(t, w.alpha, "pinned", func(n *storage.NewInvitation) { n.DiscordUserID = "200000000000000077" })
		mintAccess(t, w.st, admin, "an-access-link", nil)
		archived := insertCampaign(t, w.st, w.tenant, "Archived")
		w.mintCampaign(t, archived, "archived-link", nil)
		if _, err := w.st.ArchiveCampaign(ctx, w.tenant, archived); err != nil {
			t.Fatalf("ArchiveCampaign: %v", err)
		}

		stranger := "200000000000000055"
		for _, tok := range []string{"never-minted", "", "expired", "pinned", "an-access-link", "archived-link"} {
			if _, err := w.st.RedeemCampaignInvitation(ctx, tok, redemption(stranger, "Sam")); !errors.Is(err, storage.ErrInvitationInvalid) {
				t.Errorf("redeeming %q = %v, want ErrInvitationInvalid", tok, err)
			}
		}
		if n := countAccessRows(t, w.pool, `SELECT count(*) FROM users WHERE discord_user_id = $1`, stranger); n != 0 {
			t.Error("a refused redemption created the user")
		}
		for _, tok := range []string{"expired", "pinned", "an-access-link", "archived-link"} {
			if invitationUses(t, w.st, tok) != 0 {
				t.Errorf("%s: a refused redemption counted a use", tok)
			}
		}
		// The access link is untouched by the campaign path, in both directions.
		if _, err := w.st.RedeemAccessInvitation(ctx, "open-link", stranger); !errors.Is(err, storage.ErrInvitationInvalid) {
			t.Errorf("redeeming a campaign link as an ACCESS invitation = %v, want ErrInvitationInvalid", err)
		}

		// The pinned link works for its own Discord User.
		if _, err := w.st.RedeemCampaignInvitation(ctx, "pinned", redemption("200000000000000077", "Pin")); err != nil {
			t.Errorf("the pinned user redeeming their link: %v", err)
		}
	})

	t.Run("the host tenant's own operator cannot redeem into a guest row", func(t *testing.T) {
		w.mintCampaign(t, w.alpha, "for-the-gm", func(n *storage.NewInvitation) { n.Rights = []string{"wiki.read"} })
		if _, err := w.st.RedeemCampaignInvitation(ctx, "for-the-gm", redemption(hostDiscordID, "Host")); !errors.Is(err, storage.ErrInvitationInvalid) {
			t.Errorf("the operator redeeming their own campaign's link = %v, want ErrInvitationInvalid", err)
		}
		if n := countAccessRows(t, w.pool, `SELECT count(*) FROM campaign_guest WHERE user_id = $1`, w.host.ID); n != 0 {
			t.Errorf("the operator has %d guest rows", n)
		}
		if invitationUses(t, w.st, "for-the-gm") != 0 {
			t.Error("the refused operator consumed a use")
		}
	})

	t.Run("a suspended user is refused before anything is written", func(t *testing.T) {
		suspended := "200000000000000066"
		if _, err := w.st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: suspended, Name: "Before"}); err != nil {
			t.Fatal(err)
		}
		if err := w.st.SetUserSuspended(ctx, suspended, true); err != nil {
			t.Fatal(err)
		}
		w.mintCampaign(t, w.alpha, "for-suspended", nil)
		if _, err := w.st.RedeemCampaignInvitation(ctx, "for-suspended", redemption(suspended, "After")); !errors.Is(err, storage.ErrUserSuspended) {
			t.Fatalf("suspended redeemer = %v, want ErrUserSuspended", err)
		}
		u, _ := w.st.GetUserByDiscordID(ctx, suspended)
		if u.Name != "Before" {
			t.Errorf("the refused redemption rewrote the user's name to %q", u.Name)
		}
		if n := countAccessRows(t, w.pool, `SELECT count(*) FROM campaign_guest WHERE user_id = $1`, u.ID); n != 0 || invitationUses(t, w.st, "for-suspended") != 0 {
			t.Error("a suspended user's attempt left a guest row or burned a use")
		}
	})
}

// TestRedeemCampaignInvitation_Races: the last use of a link goes to exactly
// one redeemer, and one person racing two links of the same Campaign ends with
// one guest row holding both links' rights.
func TestRedeemCampaignInvitation_Races(t *testing.T) {
	w := newGuestWorld(t)
	ctx := context.Background()

	one := 1
	w.mintCampaign(t, w.alpha, "last-seat", func(n *storage.NewInvitation) { n.MaxUses = &one; n.Rights = []string{"wiki.read"} })
	const racers = 8
	var wg sync.WaitGroup
	results := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = w.st.RedeemCampaignInvitation(ctx, "last-seat",
				redemption(fmt.Sprintf("21000000000000000%d", i), fmt.Sprintf("Racer %d", i)))
		}()
	}
	wg.Wait()
	won := 0
	for i, err := range results {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, storage.ErrInvitationInvalid):
			t.Errorf("racer %d: %v, want success or ErrInvitationInvalid", i, err)
		}
	}
	if won != 1 {
		t.Errorf("%d racers got the single use, want exactly 1", won)
	}
	if n := countAccessRows(t, w.pool, `SELECT count(*) FROM campaign_guest WHERE campaign_id = $1`, w.alpha); n != 1 {
		t.Errorf("guest rows = %d, want 1", n)
	}
	if invitationUses(t, w.st, "last-seat") != 1 {
		t.Errorf("uses = %d, want 1", invitationUses(t, w.st, "last-seat"))
	}

	w.mintCampaign(t, w.beta, "link-a", func(n *storage.NewInvitation) { n.Rights = []string{"wiki.read"} })
	w.mintCampaign(t, w.beta, "link-b", func(n *storage.NewInvitation) { n.Rights = []string{"maps.read"} })
	errs := make([]error, 2)
	for i, tok := range []string{"link-a", "link-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = w.st.RedeemCampaignInvitation(ctx, tok, redemption(guestDiscordID, "Mika"))
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("racing link %d: %v", i, err)
		}
	}
	u, _ := w.st.GetUserByDiscordID(ctx, guestDiscordID)
	g, err := w.st.GetCampaignGuest(ctx, w.beta, u.ID)
	if err != nil || !slices.Equal(sortedRights(g.Rights), []string{"maps.read", "wiki.read"}) {
		t.Errorf("after racing two links, guest = %+v, %v; want one row with both rights", g, err)
	}
}

// TestResolveWorkspace walks the one-query resolution: the selection when the
// guest row still backs it, else the own Tenant, else the most recent guest
// Campaign, else ErrNotFound.
func TestResolveWorkspace(t *testing.T) {
	w := newGuestWorld(t)
	ctx := context.Background()
	resolve := func(t *testing.T, u storage.User, token string, needsGrant bool) (storage.Workspace, error) {
		t.Helper()
		return w.st.ResolveWorkspace(ctx, u.ID, token, needsGrant)
	}

	// The host: their own Tenant, never a guest.
	if ws, err := resolve(t, w.host, "tok-host", false); err != nil || ws.Guest || ws.TenantID != w.tenant {
		t.Fatalf("host workspace = %+v, %v; want their own tenant", ws, err)
	}

	guest := w.signIn(t, guestDiscordID, "Mika", "tok-guest")
	if _, err := resolve(t, guest, "tok-guest", false); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("someone with no tenant and no guest row = %v, want ErrNotFound", err)
	}

	// A guest with no Tenant of their own resolves to their MOST RECENTLY
	// added guest Campaign.
	w.addGuest(t, w.alpha, guestDiscordID, "wiki.read")
	ws, err := resolve(t, guest, "tok-guest", false)
	if err != nil || !ws.Guest || ws.CampaignID != w.alpha || ws.TenantID != w.tenant || !slices.Equal(ws.Rights, []string{"wiki.read"}) {
		t.Fatalf("guest workspace = %+v, %v; want alpha in the HOST tenant", ws, err)
	}
	time.Sleep(5 * time.Millisecond)
	w.addGuest(t, w.beta, guestDiscordID, "maps.read")
	if ws, _ := resolve(t, guest, "tok-guest", false); ws.CampaignID != w.beta {
		t.Fatalf("after joining beta the fallback = %s, want the most recently added (beta %s)", ws.CampaignID, w.beta)
	}

	// The session's selection wins while the guest row backs it…
	if err := w.st.SetSessionWorkspace(ctx, guest.ID, "tok-guest", &w.alpha); err != nil {
		t.Fatalf("SetSessionWorkspace: %v", err)
	}
	if ws, _ := resolve(t, guest, "tok-guest", false); ws.CampaignID != w.alpha || !slices.Equal(ws.Rights, []string{"wiki.read"}) {
		t.Fatalf("selected workspace = %+v, want alpha", ws)
	}
	// …is per SESSION, not per user…
	if _, err := w.st.CreateSession(ctx, storage.NewSession{UserID: guest.ID, Token: "tok-guest-2", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if ws, _ := resolve(t, guest, "tok-guest-2", false); ws.CampaignID != w.beta {
		t.Errorf("a second session inherited the first one's selection: %+v", ws)
	}
	// …and cannot be borrowed with someone else's token.
	if ws, err := resolve(t, w.host, "tok-guest", false); err != nil || ws.Guest {
		t.Errorf("the host resolving with the guest's token = %+v, %v; want their own tenant", ws, err)
	}

	// Rights are read per request: a change lands on the next resolution.
	if _, err := w.st.UpdateCampaignGuest(ctx, w.alpha, guest.ID, []string{"transcripts.read"}); err != nil {
		t.Fatal(err)
	}
	if ws, _ := resolve(t, guest, "tok-guest", false); !slices.Equal(ws.Rights, []string{"transcripts.read"}) {
		t.Errorf("rights after an update = %v, want the new set immediately", ws.Rights)
	}

	// A stale selection stops resolving the moment the guest row is gone. The
	// selection is forced back in here to prove the JOIN is what authorizes,
	// not the column.
	if err := w.st.RemoveCampaignGuest(ctx, w.alpha, guest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pool.Exec(ctx, `UPDATE sessions SET guest_campaign_id = $1 WHERE user_id = $2`, w.alpha, guest.ID); err != nil {
		t.Fatal(err)
	}
	if ws, _ := resolve(t, guest, "tok-guest", false); ws.CampaignID != w.beta {
		t.Fatalf("with a stale selection pointing at alpha, resolved %+v; want the fallback (beta), never alpha", ws)
	}

	// An archived Campaign is not reachable for guests; unarchiving restores it.
	if _, err := w.st.ArchiveCampaign(ctx, w.tenant, w.beta); err != nil {
		t.Fatalf("ArchiveCampaign: %v", err)
	}
	if _, err := resolve(t, guest, "tok-guest", false); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("guest of only an archived campaign = %v, want ErrNotFound", err)
	}
	if _, err := w.st.UnarchiveCampaign(ctx, w.tenant, w.beta); err != nil {
		t.Fatalf("UnarchiveCampaign: %v", err)
	}
	if ws, err := resolve(t, guest, "tok-guest", false); err != nil || ws.CampaignID != w.beta {
		t.Fatalf("after unarchiving = %+v, %v; want beta back with the guest row intact", ws, err)
	}

	t.Run("someone with their own tenant", func(t *testing.T) {
		// A second GM who is also a guest of the host's beta.
		second := foundTenant(t, w.st, otherDiscordID, "tok-second", nil)
		w.addGuest(t, w.beta, otherDiscordID, "planning.use")

		// Unselected: their own Tenant comes before any guest Campaign.
		if ws, err := resolve(t, second.User, "tok-second", false); err != nil || ws.Guest || ws.TenantID != second.Tenant.ID {
			t.Fatalf("unselected = %+v, %v; want their own tenant", ws, err)
		}
		// Selected: the guest Campaign, in the HOST tenant.
		if err := w.st.SetSessionWorkspace(ctx, second.User.ID, "tok-second", &w.beta); err != nil {
			t.Fatalf("SetSessionWorkspace: %v", err)
		}
		if ws, _ := resolve(t, second.User, "tok-second", false); !ws.Guest || ws.CampaignID != w.beta || ws.TenantID != w.tenant {
			t.Fatalf("selected = %+v, want beta in the host tenant", ws)
		}
		// Cleared: back in their own.
		if err := w.st.SetSessionWorkspace(ctx, second.User.ID, "tok-second", nil); err != nil {
			t.Fatalf("clearing: %v", err)
		}
		if ws, _ := resolve(t, second.User, "tok-second", false); ws.Guest || ws.TenantID != second.Tenant.ID {
			t.Fatalf("after clearing = %+v, want their own tenant", ws)
		}

		// An `allowlist` deployment: without an Access Grant the own Tenant is
		// NOT a workspace. A GM whose access was revoked but who is someone's
		// guest lands in the guest Campaign, never back in their own Tenant.
		ws, err := resolve(t, second.User, "tok-second", true)
		if err != nil || !ws.Guest || ws.CampaignID != w.beta || ws.TenantID != w.tenant {
			t.Fatalf("no grant, needsGrant: %+v, %v; want the guest campaign", ws, err)
		}
		if _, err := w.st.OwnTenantForUser(ctx, second.User.ID, true); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("OwnTenantForUser without a grant = %v, want ErrNotFound", err)
		}
		if id, err := w.st.OwnTenantForUser(ctx, second.User.ID, false); err != nil || id != second.Tenant.ID {
			t.Errorf("OwnTenantForUser(needsGrant=false) = %s, %v", id, err)
		}
		// With the grant, the own Tenant is back.
		if _, err := w.st.GrantAccess(ctx, otherDiscordID, "", false, w.host.ID); err != nil {
			t.Fatal(err)
		}
		if ws, _ := resolve(t, second.User, "tok-second", true); ws.Guest || ws.TenantID != second.Tenant.ID {
			t.Fatalf("with a grant, needsGrant: %+v; want their own tenant", ws)
		}
		if id, err := w.st.OwnTenantForUser(ctx, second.User.ID, true); err != nil || id != second.Tenant.ID {
			t.Errorf("OwnTenantForUser with a grant = %s, %v", id, err)
		}
		// Revoked again with NO guest row left: nothing resolves at all.
		if err := w.st.RevokeAccess(ctx, otherDiscordID); err != nil {
			t.Fatal(err)
		}
		if err := w.st.RemoveCampaignGuest(ctx, w.beta, second.User.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := w.st.ResolveWorkspace(ctx, second.User.ID, "tok-second", true); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("revoked, no guest rows, needsGrant = %v, want ErrNotFound", err)
		}
	})

	t.Run("a guest row on the user's OWN tenant never resolves", func(t *testing.T) {
		// AddCampaignGuest refuses this; a row written some other way must
		// still not turn the operator into a guest of their own Campaign.
		if _, err := w.pool.Exec(ctx,
			`INSERT INTO campaign_guest (campaign_id, user_id, rights) VALUES ($1, $2, '{wiki.read}')`, w.alpha, w.host.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := w.pool.Exec(ctx, `UPDATE sessions SET guest_campaign_id = $1 WHERE user_id = $2`, w.alpha, w.host.ID); err != nil {
			t.Fatal(err)
		}
		if ws, err := resolve(t, w.host, "tok-host", false); err != nil || ws.Guest || ws.TenantID != w.tenant {
			t.Errorf("host with a self guest row = %+v, %v; want their own tenant, not a guest scope", ws, err)
		}
		if list, _ := w.st.ListGuestWorkspaces(ctx, w.host.ID); len(list) != 0 {
			t.Errorf("the host's own campaign is listed as shared with them: %+v", list)
		}
		if err := w.st.SetSessionWorkspace(ctx, w.host.ID, "tok-host", &w.alpha); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("selecting an own campaign as a guest workspace = %v, want ErrNotFound", err)
		}
	})
}

// TestSessionWorkspaceAndList: the selection is only ever set to a Campaign
// the user is a guest of, and the "shared with me" list names host and rights.
func TestSessionWorkspaceAndList(t *testing.T) {
	w := newGuestWorld(t)
	ctx := context.Background()
	guest := w.signIn(t, guestDiscordID, "Mika", "tok-guest")
	stranger := w.signIn(t, otherDiscordID, "Noor", "tok-stranger")
	w.addGuest(t, w.alpha, guestDiscordID, "wiki.read", "maps.read")

	selected := func(token string) *uuid.UUID {
		t.Helper()
		var id *uuid.UUID
		if err := w.pool.QueryRow(ctx, `SELECT guest_campaign_id FROM sessions WHERE token = $1`, tokenDigest(token)).Scan(&id); err != nil {
			t.Fatalf("read selection of %s: %v", token, err)
		}
		return id
	}

	// Not a guest of beta; not a guest at all; someone else's token; unknown
	// campaign; unknown token — every one ErrNotFound, none writes.
	for name, try := range map[string]func() error{
		"a campaign of the same tenant they are not a guest of": func() error { return w.st.SetSessionWorkspace(ctx, guest.ID, "tok-guest", &w.beta) },
		"someone who is no guest":                               func() error { return w.st.SetSessionWorkspace(ctx, stranger.ID, "tok-stranger", &w.alpha) },
		"another user's session token":                          func() error { return w.st.SetSessionWorkspace(ctx, guest.ID, "tok-stranger", &w.alpha) },
		"a campaign that does not exist":                        func() error { id := uuid.New(); return w.st.SetSessionWorkspace(ctx, guest.ID, "tok-guest", &id) },
		"a session that does not exist":                         func() error { return w.st.SetSessionWorkspace(ctx, guest.ID, "no-such-token", &w.alpha) },
	} {
		if err := try(); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("selecting %s = %v, want ErrNotFound", name, err)
		}
	}
	if selected("tok-guest") != nil || selected("tok-stranger") != nil {
		t.Fatal("a refused selection was written")
	}

	if err := w.st.SetSessionWorkspace(ctx, guest.ID, "tok-guest", &w.alpha); err != nil {
		t.Fatalf("SetSessionWorkspace: %v", err)
	}
	if got := selected("tok-guest"); got == nil || *got != w.alpha {
		t.Fatalf("selection = %v, want alpha", got)
	}
	// A new session can start already acting as a guest (the OAuth callback).
	if _, err := w.st.CreateSession(ctx, storage.NewSession{
		UserID: guest.ID, Token: "tok-born-guest", ExpiresAt: time.Now().Add(time.Hour), GuestCampaignID: &w.alpha,
	}); err != nil {
		t.Fatalf("CreateSession with a guest campaign: %v", err)
	}
	if got := selected("tok-born-guest"); got == nil || *got != w.alpha {
		t.Errorf("born-guest selection = %v, want alpha", got)
	}
	// Archived: cannot be selected.
	w.addGuest(t, w.beta, guestDiscordID, "highlights.read")
	if _, err := w.st.ArchiveCampaign(ctx, w.tenant, w.beta); err != nil {
		t.Fatal(err)
	}
	if err := w.st.SetSessionWorkspace(ctx, guest.ID, "tok-guest", &w.beta); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("selecting an archived campaign = %v, want ErrNotFound", err)
	}

	list, err := w.st.ListGuestWorkspaces(ctx, guest.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListGuestWorkspaces = %+v, %v; want only the unarchived alpha", list, err)
	}
	if list[0].CampaignID != w.alpha || list[0].CampaignName != "Alpha" || list[0].TenantName == "" ||
		!slices.Equal(list[0].Rights, []string{"wiki.read", "maps.read"}) || list[0].AddedAt.IsZero() {
		t.Errorf("workspace entry = %+v", list[0])
	}
	if _, err := w.st.UnarchiveCampaign(ctx, w.tenant, w.beta); err != nil {
		t.Fatal(err)
	}
	if list, _ := w.st.ListGuestWorkspaces(ctx, guest.ID); len(list) != 2 || list[0].CampaignID != w.beta {
		t.Errorf("after unarchiving = %+v, want both, most recently added (beta) first", list)
	}
	if list, err := w.st.ListGuestWorkspaces(ctx, stranger.ID); err != nil || len(list) != 0 {
		t.Errorf("a non-guest's list = %+v, %v; want empty", list, err)
	}

	// Clearing is allowed for any real session, and deleting the Campaign
	// drops the selection with it (ON DELETE SET NULL).
	if err := w.st.SetSessionWorkspace(ctx, guest.ID, "tok-guest", nil); err != nil || selected("tok-guest") != nil {
		t.Errorf("clearing = %v, selection %v", err, selected("tok-guest"))
	}
	if _, err := w.st.ArchiveCampaign(ctx, w.tenant, w.alpha); err != nil { // a hard delete requires an archived campaign
		t.Fatalf("ArchiveCampaign: %v", err)
	}
	if err := w.st.DeleteCampaign(ctx, w.tenant, w.alpha); err != nil {
		t.Fatalf("DeleteCampaign: %v", err)
	}
	if selected("tok-born-guest") != nil {
		t.Error("deleting the campaign left a session selecting it")
	}
	if n := countAccessRows(t, w.pool, `SELECT count(*) FROM campaign_guest WHERE campaign_id = $1`, w.alpha); n != 0 {
		t.Error("deleting the campaign left its guest rows")
	}

	// GuestStandingOf: the callback's "is this person a guest anywhere".
	st, err := w.st.GuestStandingOf(ctx, guestDiscordID)
	if err != nil || st.CampaignID != w.beta || st.OperatesTenant {
		t.Errorf("GuestStandingOf(guest) = %+v, %v; want beta, no tenant", st, err)
	}
	for _, id := range []string{otherDiscordID, hostDiscordID, "200000000000000404"} {
		if _, err := w.st.GuestStandingOf(ctx, id); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("GuestStandingOf(%s) = %v, want ErrNotFound", id, err)
		}
	}
	second := foundTenant(t, w.st, "200000000000000009", "tok-second", nil)
	w.addGuest(t, w.beta, "200000000000000009", "wiki.read")
	if st, err := w.st.GuestStandingOf(ctx, "200000000000000009"); err != nil || !st.OperatesTenant || st.CampaignID != w.beta {
		t.Errorf("GuestStandingOf(a GM who is also a guest) = %+v, %v (tenant %s)", st, err, second.Tenant.ID)
	}
	if _, err := w.st.ArchiveCampaign(ctx, w.tenant, w.beta); err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.GuestStandingOf(ctx, guestDiscordID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("guest of only an archived campaign: GuestStandingOf = %v, want ErrNotFound", err)
	}
}

// TestPlanningThreadCreatorScope: created_by is recorded, and the creator-
// scoped variants see only the creator's threads — never the GM's, never
// another guest's, never one from before the column.
func TestPlanningThreadCreatorScope(t *testing.T) {
	w := newGuestWorld(t)
	ctx := context.Background()
	mika := w.signIn(t, guestDiscordID, "Mika", "tok-mika")
	noor := w.signIn(t, otherDiscordID, "Noor", "tok-noor")

	legacy, err := w.st.CreatePlanningThread(ctx, w.alpha, "from before the column")
	if err != nil || legacy.CreatedBy.Valid {
		t.Fatalf("CreatePlanningThread = %+v, %v; want created_by unset", legacy, err)
	}
	gms, err := w.st.CreatePlanningThreadBy(ctx, w.alpha, "the GM's", w.host.ID)
	if err != nil || !gms.CreatedBy.Valid || gms.CreatedBy.UUID != w.host.ID {
		t.Fatalf("CreatePlanningThreadBy(host) = %+v, %v", gms, err)
	}
	mikas, err := w.st.CreatePlanningThreadBy(ctx, w.alpha, "Mika's", mika.ID)
	if err != nil {
		t.Fatal(err)
	}
	noors, err := w.st.CreatePlanningThreadBy(ctx, w.alpha, "Noor's", noor.ID)
	if err != nil {
		t.Fatal(err)
	}
	mikasInBeta, err := w.st.CreatePlanningThreadBy(ctx, w.beta, "Mika's in beta", mika.ID)
	if err != nil {
		t.Fatal(err)
	}

	list, err := w.st.ListPlanningThreadsBy(ctx, w.alpha, mika.ID)
	if err != nil || len(list) != 1 || list[0].ID != mikas.ID {
		t.Fatalf("ListPlanningThreadsBy(alpha, mika) = %+v, %v; want only Mika's alpha thread", list, err)
	}
	// The GM's unscoped list still sees everything in the campaign.
	if all, err := w.st.ListPlanningThreads(ctx, w.alpha); err != nil || len(all) != 4 {
		t.Fatalf("ListPlanningThreads(alpha) = %d threads, %v; want all 4", len(all), err)
	}

	for name, id := range map[string]uuid.UUID{
		"the GM's thread": gms.ID, "another guest's thread": noors.ID,
		"a thread with no creator": legacy.ID, "their own thread in ANOTHER campaign": mikasInBeta.ID,
		"an unknown id": uuid.New(),
	} {
		if _, err := w.st.GetPlanningThreadBy(ctx, w.alpha, id, mika.ID); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("GetPlanningThreadBy(%s) = %v, want ErrNotFound", name, err)
		}
		if _, err := w.st.RenamePlanningThreadBy(ctx, w.alpha, id, mika.ID, "hijacked"); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("RenamePlanningThreadBy(%s) = %v, want ErrNotFound", name, err)
		}
		if err := w.st.DeletePlanningThreadBy(ctx, w.alpha, id, mika.ID); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("DeletePlanningThreadBy(%s) = %v, want ErrNotFound", name, err)
		}
	}
	// Nothing was renamed or deleted by the refused calls.
	for _, th := range []storage.PlanningThread{gms, noors, legacy} {
		got, err := w.st.GetPlanningThread(ctx, w.alpha, th.ID)
		if err != nil || got.Title != th.Title {
			t.Errorf("thread %q after refused writes = %+v, %v", th.Title, got, err)
		}
	}

	if got, err := w.st.GetPlanningThreadBy(ctx, w.alpha, mikas.ID, mika.ID); err != nil || got.ID != mikas.ID {
		t.Errorf("GetPlanningThreadBy(own) = %+v, %v", got, err)
	}
	if got, err := w.st.RenamePlanningThreadBy(ctx, w.alpha, mikas.ID, mika.ID, "Mika's plan"); err != nil || got.Title != "Mika's plan" {
		t.Errorf("RenamePlanningThreadBy(own) = %+v, %v", got, err)
	}
	if err := w.st.DeletePlanningThreadBy(ctx, w.alpha, mikas.ID, mika.ID); err != nil {
		t.Errorf("DeletePlanningThreadBy(own): %v", err)
	}
	if _, err := w.st.GetPlanningThread(ctx, w.alpha, mikas.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("the deleted thread is still there: %v", err)
	}
}

// TestGuestScopedReads covers the small by-id reads a guest's handlers lean
// on: the invitation preview target, the campaign-scoped session check, the
// private-node set and the public portrait read — plus the Admin overview
// reporting a guest-only person.
func TestGuestScopedReads(t *testing.T) {
	w := newGuestWorld(t)
	ctx := context.Background()

	target, err := w.st.GetInvitationCampaign(ctx, w.alpha)
	if err != nil || target.CampaignName != "Alpha" || target.TenantName == "" {
		t.Fatalf("GetInvitationCampaign(alpha) = %+v, %v", target, err)
	}
	if _, err := w.st.GetInvitationCampaign(ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("unknown campaign = %v, want ErrNotFound", err)
	}

	sessA, err := w.st.CreateVoiceSession(ctx, w.alpha)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		campaign, session uuid.UUID
		want              bool
	}{
		{w.alpha, sessA.ID, true},
		{w.beta, sessA.ID, false}, // the same tenant's OTHER campaign
		{w.alpha, uuid.New(), false},
	} {
		got, err := w.st.VoiceSessionInCampaign(ctx, tc.campaign, tc.session)
		if err != nil || got != tc.want {
			t.Errorf("VoiceSessionInCampaign(%s, %s) = %v, %v; want %v", tc.campaign, tc.session, got, err, tc.want)
		}
	}
	// The tenant-level check passes for BOTH campaigns — which is exactly why
	// it is not enough for a guest.
	if in, _ := w.st.VoiceSessionInTenant(ctx, w.tenant, sessA.ID); !in {
		t.Fatal("fixture: the session should be in the host tenant")
	}

	mkNode := func(campaign uuid.UUID, name string, private bool) storage.KGNode {
		t.Helper()
		n, err := w.st.CreateNode(ctx, storage.NewKGNode{CampaignID: campaign, Type: storage.KGNodeNPC, Name: name, GMPrivate: private})
		if err != nil {
			t.Fatalf("CreateNode(%s): %v", name, err)
		}
		return n
	}
	public := mkNode(w.alpha, "Bart the innkeeper", false)
	secret := mkNode(w.alpha, "The cult leader", true)
	betaSecret := mkNode(w.beta, "Beta's secret", true)

	private, err := w.st.PrivateNodeIDs(ctx, w.alpha)
	if err != nil || len(private) != 1 {
		t.Fatalf("PrivateNodeIDs(alpha) = %v, %v; want exactly alpha's one private node", private, err)
	}
	if _, ok := private[secret.ID]; !ok {
		t.Errorf("PrivateNodeIDs lacks the private node")
	}
	if _, ok := private[betaSecret.ID]; ok {
		t.Errorf("PrivateNodeIDs(alpha) contains a node of beta")
	}

	for _, n := range []storage.KGNode{public, secret} {
		if _, _, err := w.st.SetNodePortrait(ctx, w.alpha, n.ID, "tenant/x/node/"+n.ID.String()+"/portrait"); err != nil {
			t.Fatalf("SetNodePortrait: %v", err)
		}
	}
	if key, _, err := w.st.PublicNodePortrait(ctx, w.alpha, public.ID); err != nil || key == "" {
		t.Errorf("PublicNodePortrait(public) = %q, %v", key, err)
	}
	if _, _, err := w.st.PublicNodePortrait(ctx, w.alpha, secret.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("PublicNodePortrait(private) = %v, want ErrNotFound", err)
	}
	if _, _, err := w.st.PublicNodePortrait(ctx, w.beta, public.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("PublicNodePortrait through another campaign = %v, want ErrNotFound", err)
	}
	// The GM read still serves the private one.
	if key, _, err := w.st.NodePortrait(ctx, w.alpha, secret.ID); err != nil || key == "" {
		t.Errorf("NodePortrait(private) = %q, %v", key, err)
	}

	// The Admin overview: a guest added by snowflake is listed as not signed
	// in, with their guest campaign count and no Tenant.
	w.addGuest(t, w.alpha, guestDiscordID, "wiki.read")
	w.addGuest(t, w.beta, guestDiscordID, "wiki.read")
	find := func() storage.AccessPerson {
		t.Helper()
		people, err := w.st.ListAccessPeople(ctx, false)
		if err != nil {
			t.Fatalf("ListAccessPeople: %v", err)
		}
		for _, p := range people {
			if p.DiscordUserID == guestDiscordID {
				return p
			}
		}
		t.Fatalf("the guest is missing from the people list: %+v", people)
		return storage.AccessPerson{}
	}
	p := find()
	if p.Known || p.TenantID != nil || p.HasGrant || p.GuestCampaignCount != 2 || p.Name != "" {
		t.Errorf("guest added by snowflake = %+v; want not signed in, no tenant, no grant, 2 guest campaigns", p)
	}
	w.signIn(t, guestDiscordID, "Mika", "tok-guest")
	if _, err := w.st.ArchiveCampaign(ctx, w.tenant, w.beta); err != nil {
		t.Fatal(err)
	}
	p = find()
	if !p.Known || p.Name != "Mika" || p.GuestCampaignCount != 1 {
		t.Errorf("after sign-in and archiving beta = %+v; want signed in, 1 reachable guest campaign", p)
	}
}
