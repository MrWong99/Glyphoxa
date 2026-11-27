//go:build integration

package storage_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// ADR-0065 review hardening, against a real Postgres. Each test pins one way
// a revocation used to be incomplete or reversible.

const (
	policyAdminID   = "300000000000000001"
	policyGrantedID = "300000000000000002"
	policyPlainID   = "300000000000000003"
)

// TestRevokeAccess_CutsWhatTheGrantGave: revoking switches Platform Keys off
// on the revoked operator's Tenant, and the still-live link the grant came
// through no longer admits THAT person — while it keeps working for others,
// and a new link or an explicit grant lets them back in.
func TestRevokeAccess_CutsWhatTheGrantGave(t *testing.T) {
	st, pool := migratedWithPool(t)
	ctx := context.Background()
	admin := accessAdmin(t, st)
	const gm = policyGrantedID

	mintAccess(t, st, admin, "team-link", func(n *storage.NewInvitation) { n.PlatformKeys = true })
	if _, err := st.RedeemAccessInvitation(ctx, "team-link", gm); err != nil {
		t.Fatalf("RedeemAccessInvitation: %v", err)
	}
	on := true
	tenant := foundTenant(t, st, gm, "tok-gm", &on).Tenant.ID
	if ok, _ := st.TenantPlatformKeys(ctx, tenant); !ok {
		t.Fatal("fixture: the granted Tenant should start with Platform Keys on")
	}

	if err := st.RevokeAccess(ctx, gm); err != nil {
		t.Fatalf("RevokeAccess: %v", err)
	}
	if ok, err := st.TenantPlatformKeys(ctx, tenant); err != nil || ok {
		t.Errorf("Platform Keys after revoke = %v, %v; want off — the Tenant's Discord surface and its guests still resolve keys through it", ok, err)
	}

	// The same live link must not undo the revocation.
	if _, err := st.RedeemAccessInvitation(ctx, "team-link", gm); !errors.Is(err, storage.ErrInvitationInvalid) {
		t.Fatalf("re-redeeming the link after a revoke = %v, want ErrInvitationInvalid", err)
	}
	if _, err := st.GetAccessGrant(ctx, gm); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("grant after the refused redemption = %v, want none", err)
	}
	if uses := invitationUses(t, st, "team-link"); uses != 1 {
		t.Errorf("uses = %d after a refused redemption, want still 1", uses)
	}
	if ok, _ := st.TenantPlatformKeys(ctx, tenant); ok {
		t.Error("the refused redemption switched Platform Keys back on")
	}
	// …and the link is not dead for anybody else.
	if _, err := st.RedeemAccessInvitation(ctx, "team-link", policyPlainID); err != nil {
		t.Errorf("another person redeeming the same link: %v", err)
	}

	// A NEW link is the admin deciding again.
	mintAccess(t, st, admin, "second-chance", nil)
	if _, err := st.RedeemAccessInvitation(ctx, "second-chance", gm); err != nil {
		t.Fatalf("a new link after a revoke: %v", err)
	}

	// So is an explicit grant. Neither clears a refusal: a refusal is inert
	// while the grant exists, and the next revoke must hold against every
	// link that exists by then — however the grant it takes away was made.
	if err := st.RevokeAccess(ctx, gm); err != nil {
		t.Fatalf("second RevokeAccess: %v", err)
	}
	refusals := func() int {
		return countAccessRows(t, pool, `SELECT count(*) FROM invitation_refusal WHERE discord_user_id = $1`, gm)
	}
	if n := refusals(); n != 2 {
		t.Fatalf("refusals after the second revoke = %d, want 2 (both links existed)", n)
	}
	if _, err := st.GrantAccess(ctx, gm, "back in", true, admin.ID); err != nil {
		t.Fatalf("GrantAccess: %v", err)
	}
	if n := refusals(); n != 2 {
		t.Errorf("refusals after an explicit grant = %d, want still 2", n)
	}
	if ok, _ := st.TenantPlatformKeys(ctx, tenant); !ok {
		t.Error("re-granting with Platform Keys on did not restore the Tenant's switch")
	}
	// Holding a grant, opening a refused link is the harmless no-op it is for
	// anybody already admitted.
	if g, err := st.RedeemAccessInvitation(ctx, "team-link", gm); err != nil || g.InvitationID != nil {
		t.Errorf("a grant holder opening a link refused to them = %+v, %v; want their existing grant", g, err)
	}

	// Revoking the grant given by id is as final as revoking a link-born one.
	if err := st.RevokeAccess(ctx, gm); err != nil {
		t.Fatalf("third RevokeAccess: %v", err)
	}
	for _, link := range []string{"team-link", "second-chance"} {
		if _, err := st.RedeemAccessInvitation(ctx, link, gm); !errors.Is(err, storage.ErrInvitationInvalid) {
			t.Errorf("redeeming %q after the third revoke = %v, want ErrInvitationInvalid", link, err)
		}
	}
	if ok, _ := st.TenantPlatformKeys(ctx, tenant); ok {
		t.Error("Platform Keys are on after the third revoke")
	}
}

// TestRevokeAccess_HoldsAgainstEveryExistingLink pins the review probes P1 and
// P2 plus the by-id case: a revocation refuses the person EVERY access link
// that exists at that moment, whichever way the grant was made, and an
// explicit re-grant in between does not reopen any of them. Only a link
// minted afterwards, or another explicit grant, lets them back in.
func TestRevokeAccess_HoldsAgainstEveryExistingLink(t *testing.T) {
	refusedEverywhere := func(t *testing.T, st *storage.Store, who string, links ...string) {
		t.Helper()
		ctx := context.Background()
		for _, link := range links {
			before := invitationUses(t, st, link)
			if _, err := st.RedeemAccessInvitation(ctx, link, who); !errors.Is(err, storage.ErrInvitationInvalid) {
				t.Errorf("redeeming %q after the revoke = %v, want ErrInvitationInvalid", link, err)
			}
			if invitationUses(t, st, link) != before {
				t.Errorf("the refused redemption of %q counted a use", link)
			}
		}
		if _, err := st.GetAccessGrant(ctx, who); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("grant after the refused redemptions = %v, want none", err)
		}
	}

	t.Run("P1: revoke, re-grant by id, revoke, same link", func(t *testing.T) {
		st, _ := migratedWithPool(t)
		ctx := context.Background()
		admin := accessAdmin(t, st)
		const gm = policyGrantedID
		mintAccess(t, st, admin, "team-link", func(n *storage.NewInvitation) { n.PlatformKeys = true })
		if _, err := st.RedeemAccessInvitation(ctx, "team-link", gm); err != nil {
			t.Fatalf("RedeemAccessInvitation: %v", err)
		}
		on := true
		tenant := foundTenant(t, st, gm, "tok-gm", &on).Tenant.ID
		if err := st.RevokeAccess(ctx, gm); err != nil {
			t.Fatalf("RevokeAccess: %v", err)
		}
		if _, err := st.GrantAccess(ctx, gm, "one more chance", false, admin.ID); err != nil {
			t.Fatalf("GrantAccess: %v", err)
		}
		if err := st.RevokeAccess(ctx, gm); err != nil {
			t.Fatalf("second RevokeAccess: %v", err)
		}
		refusedEverywhere(t, st, gm, "team-link")
		if ok, err := st.TenantPlatformKeys(ctx, tenant); err != nil || ok {
			t.Errorf("Platform Keys = %v, %v; want off — the link must not restore them against the admin's last choice", ok, err)
		}
	})

	t.Run("P2: in through one link, revoked, a second link that already existed", func(t *testing.T) {
		st, _ := migratedWithPool(t)
		ctx := context.Background()
		admin := accessAdmin(t, st)
		const gm = policyGrantedID
		mintAccess(t, st, admin, "link-1", nil)
		mintAccess(t, st, admin, "link-2", func(n *storage.NewInvitation) { n.PlatformKeys = true })
		if _, err := st.RedeemAccessInvitation(ctx, "link-1", gm); err != nil {
			t.Fatalf("RedeemAccessInvitation: %v", err)
		}
		if err := st.RevokeAccess(ctx, gm); err != nil {
			t.Fatalf("RevokeAccess: %v", err)
		}
		refusedEverywhere(t, st, gm, "link-1", "link-2")
		// Nobody else is affected, and a link minted afterwards admits.
		if _, err := st.RedeemAccessInvitation(ctx, "link-2", policyPlainID); err != nil {
			t.Errorf("another person redeeming link-2: %v", err)
		}
		mintAccess(t, st, admin, "link-3", nil)
		if _, err := st.RedeemAccessInvitation(ctx, "link-3", gm); err != nil {
			t.Errorf("a link minted after the revoke: %v", err)
		}
	})

	t.Run("granted by id, revoked, a link that already existed", func(t *testing.T) {
		st, _ := migratedWithPool(t)
		ctx := context.Background()
		admin := accessAdmin(t, st)
		const gm = policyGrantedID
		mintAccess(t, st, admin, "open-link", nil)
		if _, err := st.GrantAccess(ctx, gm, "", false, admin.ID); err != nil {
			t.Fatalf("GrantAccess: %v", err)
		}
		if err := st.RevokeAccess(ctx, gm); err != nil {
			t.Fatalf("RevokeAccess: %v", err)
		}
		refusedEverywhere(t, st, gm, "open-link")
		// Revoking twice (grant, revoke again) records nothing twice.
		if _, err := st.GrantAccess(ctx, gm, "", false, admin.ID); err != nil {
			t.Fatalf("re-grant: %v", err)
		}
		if err := st.RevokeAccess(ctx, gm); err != nil {
			t.Fatalf("second RevokeAccess: %v", err)
		}
		refusedEverywhere(t, st, gm, "open-link")
	})
}

// TestOperatorBindings_FollowAccessPolicy: on an allowlist deployment the
// Discord-side GM identity source lists only ADMITTED operators — Platform
// Admins and Access Grant holders — so revoking a grant ends GM standing on
// Discord too. Open mode (the zero policy) is unchanged.
func TestOperatorBindings_FollowAccessPolicy(t *testing.T) {
	st, _ := migratedWithPool(t)
	ctx := context.Background()
	admin := accessAdmin(t, st)
	for _, id := range []string{policyAdminID, policyGrantedID, policyPlainID} {
		foundTenant(t, st, id, "tok-"+id, nil)
	}
	if _, err := st.GrantAccess(ctx, policyGrantedID, "", false, admin.ID); err != nil {
		t.Fatalf("GrantAccess: %v", err)
	}

	bound := func(t *testing.T) []string {
		t.Helper()
		bindings, err := st.ListTenantOperatorBindings(ctx)
		if err != nil {
			t.Fatalf("ListTenantOperatorBindings: %v", err)
		}
		ids := []string{}
		for _, b := range bindings {
			ids = append(ids, b.DiscordUserID)
		}
		slices.Sort(ids)
		// The deployment-wide list must agree with the per-Tenant one.
		flat, err := st.ListTenantOperatorDiscordIDs(ctx)
		if err != nil {
			t.Fatalf("ListTenantOperatorDiscordIDs: %v", err)
		}
		if !slices.Equal(flat, ids) && (len(flat) != 0 || len(ids) != 0) {
			t.Errorf("ListTenantOperatorDiscordIDs = %v, bindings name %v", flat, ids)
		}
		return ids
	}
	want := func(t *testing.T, when string, ids ...string) {
		t.Helper()
		if got := bound(t); !slices.Equal(got, ids) {
			t.Errorf("%s: bound operators = %v, want %v", when, got, ids)
		}
	}
	all := []string{policyAdminID, policyGrantedID, policyPlainID}

	want(t, "no policy (open mode)", all...)

	st.SetAccessPolicy(storage.AccessPolicy{OperatorsNeedGrant: true, PlatformAdmins: []string{policyAdminID}})
	want(t, "allowlist", policyAdminID, policyGrantedID)

	if err := st.RevokeAccess(ctx, policyGrantedID); err != nil {
		t.Fatalf("RevokeAccess: %v", err)
	}
	want(t, "allowlist, grant revoked", policyAdminID)
	if _, err := st.GrantAccess(ctx, policyGrantedID, "", false, admin.ID); err != nil {
		t.Fatalf("re-grant: %v", err)
	}
	want(t, "allowlist, re-granted", policyAdminID, policyGrantedID)

	// Suspension is independent of the policy: it drops a Platform Admin too.
	if err := st.SetUserSuspended(ctx, policyAdminID, true); err != nil {
		t.Fatal(err)
	}
	want(t, "allowlist, admin suspended", policyGrantedID)
	if err := st.SetUserSuspended(ctx, policyAdminID, false); err != nil {
		t.Fatal(err)
	}

	// A process without the env list (a voice worker) trusts the list the web
	// tier recorded — and requires nothing while none was ever recorded,
	// rather than dropping the deployment's own admin.
	st.SetAccessPolicy(storage.AccessPolicy{OperatorsNeedGrant: true, RecordedAdmins: true})
	want(t, "worker, nothing recorded yet", all...)
	if err := st.RecordAdmissionPosture(ctx, "allowlist"); err != nil {
		t.Fatal(err)
	}
	want(t, "worker, posture recorded but no admin list", all...)
	if err := st.RecordPlatformAdmins(ctx, "open", []string{policyAdminID}); err != nil {
		t.Fatalf("RecordPlatformAdmins: %v", err)
	}
	want(t, "worker, admin list recorded", policyAdminID, policyGrantedID)
	if mode, _ := st.GetAdmissionPosture(ctx); mode != "allowlist" {
		t.Errorf("recording the admin list changed the posture to %q", mode)
	}
	// Such a process reads the Admission Mode LIVE, like the admin list: it is
	// not restarted when the web tier flips the posture, and must neither keep
	// dropping every self-signup GM of a deployment that went open nor keep
	// binding ungranted operators of one that was locked down.
	if err := st.RecordAdmissionPosture(ctx, "open"); err != nil {
		t.Fatal(err)
	}
	want(t, "worker, posture flipped to open", all...)
	if err := st.RecordAdmissionPosture(ctx, "allowlist"); err != nil {
		t.Fatal(err)
	}
	want(t, "worker, posture flipped back to allowlist", policyAdminID, policyGrantedID)
	// An EMPTY recorded list is a recorded list: nobody is an admin.
	if err := st.RecordPlatformAdmins(ctx, "allowlist", nil); err != nil {
		t.Fatal(err)
	}
	want(t, "worker, empty admin list recorded", policyGrantedID)

	// Without RecordedAdmins neither the recorded list nor the recorded
	// posture is consulted: the web tier knows both first-hand.
	if err := st.RecordPlatformAdmins(ctx, "allowlist", []string{policyPlainID}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordAdmissionPosture(ctx, "open"); err != nil {
		t.Fatal(err)
	}
	st.SetAccessPolicy(storage.AccessPolicy{OperatorsNeedGrant: true, PlatformAdmins: []string{policyAdminID}})
	want(t, "web, ignores the recorded list and posture", policyAdminID, policyGrantedID)
}

// TestRecordPlatformAdmins_CreatesTheSettingsRow: on a database no boot has
// recorded a posture in yet, the admin list brings the row with it.
func TestRecordPlatformAdmins_CreatesTheSettingsRow(t *testing.T) {
	st, _ := migratedWithPool(t)
	ctx := context.Background()
	if err := st.RecordPlatformAdmins(ctx, "allowlist", []string{policyAdminID}); err != nil {
		t.Fatalf("RecordPlatformAdmins: %v", err)
	}
	if mode, err := st.GetAdmissionPosture(ctx); err != nil || mode != "allowlist" {
		t.Errorf("posture = %q, %v; want the mode the row was created with", mode, err)
	}
}

// TestHostStanding: a Campaign is reachable for its guests only while the
// operator of its Tenant is in good standing. Suspending the host — or, on an
// allowlist deployment, revoking their grant — closes every guest-facing read
// and the campaign links; the guest rows are kept, so restoring the host
// restores the guests.
func TestHostStanding(t *testing.T) {
	w := newGuestWorld(t)
	ctx := context.Background()
	admin := accessAdmin(t, w.st)

	guest := w.signIn(t, guestDiscordID, "Mika", "tok-guest")
	w.mintCampaign(t, w.alpha, "party", func(n *storage.NewInvitation) { n.Rights = []string{"wiki.read"} })
	if _, err := w.st.RedeemCampaignInvitation(ctx, "party", redemption(guestDiscordID, "Mika")); err != nil {
		t.Fatalf("RedeemCampaignInvitation: %v", err)
	}
	if err := w.st.SetSessionWorkspace(ctx, guest.ID, "tok-guest", &w.alpha); err != nil {
		t.Fatalf("SetSessionWorkspace: %v", err)
	}

	// What the Admin screen says about the guest: it must agree with what
	// actually admits them.
	guestCampaigns := func(t *testing.T) int {
		t.Helper()
		people, err := w.st.ListAccessPeople(ctx, false)
		if err != nil {
			t.Fatalf("ListAccessPeople: %v", err)
		}
		for _, p := range people {
			if p.DiscordUserID == guestDiscordID {
				return p.GuestCampaignCount
			}
		}
		t.Fatalf("the guest is missing from the people list")
		return 0
	}
	reachable := func(t *testing.T, when string) {
		t.Helper()
		if ws, err := w.st.ResolveWorkspace(ctx, guest.ID, "tok-guest", false); err != nil || !ws.Guest || ws.CampaignID != w.alpha {
			t.Errorf("%s: workspace = %+v, %v; want the guest campaign", when, ws, err)
		}
		if list, err := w.st.ListGuestWorkspaces(ctx, guest.ID); err != nil || len(list) != 1 {
			t.Errorf("%s: guest workspaces = %v, %v; want the one campaign", when, list, err)
		}
		if st, err := w.st.GuestStandingOf(ctx, guestDiscordID); err != nil || st.CampaignID != w.alpha {
			t.Errorf("%s: guest standing = %+v, %v", when, st, err)
		}
		if err := w.st.SetSessionWorkspace(ctx, guest.ID, "tok-guest", &w.alpha); err != nil {
			t.Errorf("%s: selecting the campaign: %v", when, err)
		}
		if _, err := w.st.GetInvitationCampaign(ctx, w.alpha); err != nil {
			t.Errorf("%s: the link preview: %v", when, err)
		}
		if n := guestCampaigns(t); n != 1 {
			t.Errorf("%s: the Admin people list counts %d guest campaigns, want 1", when, n)
		}
	}
	newcomers := 0
	unreachable := func(t *testing.T, when string) {
		t.Helper()
		// The session still SELECTS alpha, so this is step 1 failing, and step
		// 3 not rescuing it.
		if ws, err := w.st.ResolveWorkspace(ctx, guest.ID, "tok-guest", false); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("%s: workspace = %+v, %v; want ErrNotFound", when, ws, err)
		}
		if list, err := w.st.ListGuestWorkspaces(ctx, guest.ID); err != nil || len(list) != 0 {
			t.Errorf("%s: guest workspaces = %v, %v; want none", when, list, err)
		}
		if st, err := w.st.GuestStandingOf(ctx, guestDiscordID); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("%s: guest standing = %+v, %v; want ErrNotFound", when, st, err)
		}
		if err := w.st.SetSessionWorkspace(ctx, guest.ID, "tok-guest", &w.alpha); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("%s: selecting the campaign = %v, want ErrNotFound", when, err)
		}
		if _, err := w.st.GetInvitationCampaign(ctx, w.alpha); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("%s: the link preview = %v, want ErrNotFound", when, err)
		}
		if n := guestCampaigns(t); n != 0 {
			t.Errorf("%s: the Admin people list counts %d guest campaigns, want 0 — none is reachable", when, n)
		}
		// The link does not redeem — neither for a newcomer (the second
		// account of a cut-off GM) nor as a sign-in for the existing guest.
		newcomers++
		newcomer := "40000000000000000" + strconv.Itoa(newcomers)
		usesBefore := invitationUses(t, w.st, "party")
		for _, who := range []string{newcomer, guestDiscordID} {
			if _, err := w.st.RedeemCampaignInvitation(ctx, "party", redemption(who, "Sam")); !errors.Is(err, storage.ErrInvitationInvalid) {
				t.Errorf("%s: %s redeeming the campaign link = %v, want ErrInvitationInvalid", when, who, err)
			}
		}
		if invitationUses(t, w.st, "party") != usesBefore {
			t.Errorf("%s: a refused redemption counted a use", when)
		}
		if n := countAccessRows(t, w.pool, `SELECT count(*) FROM users WHERE discord_user_id = $1`, newcomer); n != 0 {
			t.Errorf("%s: a refused redemption created the user", when)
		}
		// Nothing is deleted: restoring the host restores the guest.
		if _, err := w.st.GetCampaignGuest(ctx, w.alpha, guest.ID); err != nil {
			t.Errorf("%s: the guest row is gone: %v", when, err)
		}
	}

	reachable(t, "baseline")

	// Suspension: in every Admission Mode.
	if err := w.st.SetUserSuspended(ctx, hostDiscordID, true); err != nil {
		t.Fatal(err)
	}
	unreachable(t, "host suspended")
	if err := w.st.SetUserSuspended(ctx, hostDiscordID, false); err != nil {
		t.Fatal(err)
	}
	reachable(t, "host unsuspended")

	// Allowlist: the host must be a Platform Admin or hold an Access Grant.
	w.st.SetAccessPolicy(storage.AccessPolicy{OperatorsNeedGrant: true, PlatformAdmins: []string{policyAdminID}})
	unreachable(t, "allowlist, host holds no grant")
	if _, err := w.st.GrantAccess(ctx, hostDiscordID, "", false, admin.ID); err != nil {
		t.Fatalf("GrantAccess: %v", err)
	}
	reachable(t, "allowlist, host granted")
	if err := w.st.RevokeAccess(ctx, hostDiscordID); err != nil {
		t.Fatalf("RevokeAccess: %v", err)
	}
	unreachable(t, "allowlist, host revoked")
	w.st.SetAccessPolicy(storage.AccessPolicy{OperatorsNeedGrant: true, PlatformAdmins: []string{hostDiscordID}})
	reachable(t, "allowlist, host is a Platform Admin")

	// A Tenant nobody operates is treated as before the rule existed.
	w.st.SetAccessPolicy(storage.AccessPolicy{OperatorsNeedGrant: true})
	if _, err := w.pool.Exec(ctx, `UPDATE tenant SET operator_user_id = NULL WHERE id = $1`, w.tenant); err != nil {
		t.Fatal(err)
	}
	reachable(t, "allowlist, tenant without an operator")
}

// TestGuestStanding_Joined tells a guest who chose to be one (redeemed a
// link) from someone a GM merely added by Discord User ID.
func TestGuestStanding_Joined(t *testing.T) {
	w := newGuestWorld(t)
	ctx := context.Background()

	w.addGuest(t, w.alpha, otherDiscordID, "wiki.read")
	st, err := w.st.GuestStandingOf(ctx, otherDiscordID)
	if err != nil || st.Joined || st.CampaignID != w.alpha {
		t.Fatalf("added by id: standing = %+v, %v; want a guest who did NOT join", st, err)
	}

	w.mintCampaign(t, w.beta, "beta-link", nil)
	if _, err := w.st.RedeemCampaignInvitation(ctx, "beta-link", redemption(guestDiscordID, "Mika")); err != nil {
		t.Fatalf("RedeemCampaignInvitation: %v", err)
	}
	if st, err := w.st.GuestStandingOf(ctx, guestDiscordID); err != nil || !st.Joined {
		t.Fatalf("redeemed a link: standing = %+v, %v; want Joined", st, err)
	}

	// Joined is about ANY reachable row, not just the newest: this person
	// joined beta and was later added to alpha by id.
	w.addGuest(t, w.alpha, guestDiscordID, "wiki.read")
	if st, err := w.st.GuestStandingOf(ctx, guestDiscordID); err != nil || !st.Joined || st.CampaignID != w.alpha {
		t.Errorf("joined one campaign, added to a newer one: standing = %+v, %v; want Joined, newest campaign first", st, err)
	}
	// …and only reachable rows count: with beta archived the joined row is
	// out of reach, and what is left is an add by id.
	if _, err := w.st.ArchiveCampaign(ctx, w.tenant, w.beta); err != nil {
		t.Fatalf("ArchiveCampaign: %v", err)
	}
	if st, err := w.st.GuestStandingOf(ctx, guestDiscordID); err != nil || st.Joined {
		t.Errorf("the joined campaign archived: standing = %+v, %v; want not Joined", st, err)
	}

	// Someone ADDED by id who then opens the Campaign's link has consented as
	// much as anyone who came in through it: the row is linked to the
	// invitation (Joined), with no use counted and their rights untouched.
	w.mintCampaign(t, w.alpha, "alpha-link", func(n *storage.NewInvitation) { n.Rights = []string{"wiki.read", "maps.read"} })
	res, err := w.st.RedeemCampaignInvitation(ctx, "alpha-link", redemption(otherDiscordID, "Noor"))
	if err != nil || res.Changed || !slices.Equal(res.Rights, []string{"wiki.read"}) {
		t.Fatalf("an added guest opening the link = %+v, %v; want a sign-in keeping [wiki.read]", res, err)
	}
	if uses := invitationUses(t, w.st, "alpha-link"); uses != 0 {
		t.Errorf("uses = %d after an existing guest opened the link, want 0", uses)
	}
	if st, err := w.st.GuestStandingOf(ctx, otherDiscordID); err != nil || !st.Joined {
		t.Errorf("added by id, then opened the link: standing = %+v, %v; want Joined", st, err)
	}

	// Only a link that would have admitted them counts as joining: a dead
	// one, one pinned to somebody else, and one refused to them are sign-ins
	// for an existing guest and nothing more.
	const third = "210000000000000077"
	one := 1
	w.mintCampaign(t, w.alpha, "spent", func(n *storage.NewInvitation) { n.MaxUses = &one })
	if _, err := w.st.RedeemCampaignInvitation(ctx, "spent", redemption("210000000000000078", "Seat taker")); err != nil {
		t.Fatalf("spend the single-use link: %v", err)
	}
	w.mintCampaign(t, w.alpha, "pinned", func(n *storage.NewInvitation) { n.DiscordUserID = otherDiscordID })
	w.mintCampaign(t, w.alpha, "refused", nil)
	// Join through "refused", get removed (which refuses every alpha link
	// existing now), then get added back by id.
	joined, err := w.st.RedeemCampaignInvitation(ctx, "refused", redemption(third, "Sam"))
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if err := w.st.RemoveCampaignGuest(ctx, w.alpha, joined.User.ID); err != nil {
		t.Fatalf("RemoveCampaignGuest: %v", err)
	}
	w.addGuest(t, w.alpha, third, "wiki.read")
	for _, link := range []string{"spent", "pinned", "refused"} {
		if res, err := w.st.RedeemCampaignInvitation(ctx, link, redemption(third, "Sam")); err != nil || res.Changed {
			t.Errorf("an existing guest opening %q = %+v, %v; want a plain sign-in", link, res, err)
		}
		if st, err := w.st.GuestStandingOf(ctx, third); err != nil || st.Joined {
			t.Errorf("after opening %q: standing = %+v, %v; want not Joined", link, st, err)
		}
	}
}

// TestRemoveCampaignGuest_RefusesTheLinkTheyCameThrough: a removed guest
// cannot walk back in through the same still-live link; everybody else can
// still use it, and a new link or an explicit add lets them back.
func TestRemoveCampaignGuest_RefusesTheLinkTheyCameThrough(t *testing.T) {
	w := newGuestWorld(t)
	ctx := context.Background()

	w.mintCampaign(t, w.alpha, "party", func(n *storage.NewInvitation) { n.Rights = []string{"wiki.read", "maps.read"} })
	res, err := w.st.RedeemCampaignInvitation(ctx, "party", redemption(guestDiscordID, "Mika"))
	if err != nil {
		t.Fatalf("RedeemCampaignInvitation: %v", err)
	}
	if err := w.st.RemoveCampaignGuest(ctx, w.alpha, res.User.ID); err != nil {
		t.Fatalf("RemoveCampaignGuest: %v", err)
	}

	if _, err := w.st.RedeemCampaignInvitation(ctx, "party", redemption(guestDiscordID, "Mika")); !errors.Is(err, storage.ErrInvitationInvalid) {
		t.Fatalf("re-redeeming the link after being removed = %v, want ErrInvitationInvalid", err)
	}
	if _, err := w.st.GetCampaignGuest(ctx, w.alpha, res.User.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("guest row after the refused redemption = %v, want none", err)
	}
	if uses := invitationUses(t, w.st, "party"); uses != 1 {
		t.Errorf("uses = %d after a refused redemption, want still 1", uses)
	}
	if _, err := w.st.RedeemCampaignInvitation(ctx, "party", redemption(otherDiscordID, "Noor")); err != nil {
		t.Errorf("another person redeeming the same link: %v", err)
	}

	// A new link is the GM deciding again.
	w.mintCampaign(t, w.alpha, "party-2", func(n *storage.NewInvitation) { n.Rights = []string{"wiki.read"} })
	if again, err := w.st.RedeemCampaignInvitation(ctx, "party-2", redemption(guestDiscordID, "Mika")); err != nil || !again.Changed {
		t.Fatalf("a new link after a removal = %+v, %v; want a fresh guest row", again, err)
	}

	// So is adding them by id. A removal refuses every link of THAT campaign
	// existing at the moment — and of no other campaign — and adding the
	// person back clears nothing: the refusals are inert while they are a
	// guest and hold again the next time they are removed.
	w.mintCampaign(t, w.beta, "beta-link", nil)
	if _, err := w.st.RedeemCampaignInvitation(ctx, "beta-link", redemption(guestDiscordID, "Mika")); err != nil {
		t.Fatalf("redeem beta: %v", err)
	}
	refusals := func(campaign any) int {
		return countAccessRows(t, w.pool,
			`SELECT count(*) FROM invitation_refusal r JOIN invitation i ON i.id = r.invitation_id
			  WHERE r.discord_user_id = $1 AND i.campaign_id = $2`, guestDiscordID, campaign)
	}
	if err := w.st.RemoveCampaignGuest(ctx, w.alpha, res.User.ID); err != nil {
		t.Fatalf("second RemoveCampaignGuest (alpha): %v", err)
	}
	if a, b := refusals(w.alpha), refusals(w.beta); a != 2 || b != 0 {
		t.Fatalf("after removing from alpha: refusals = %d on alpha, %d on beta; want 2 and 0", a, b)
	}
	if err := w.st.RemoveCampaignGuest(ctx, w.beta, res.User.ID); err != nil {
		t.Fatalf("RemoveCampaignGuest (beta): %v", err)
	}
	if a, b := refusals(w.alpha), refusals(w.beta); a != 2 || b != 1 {
		t.Fatalf("refusals = %d on alpha, %d on beta; want 2 and 1", a, b)
	}
	w.addGuest(t, w.alpha, guestDiscordID, "wiki.read")
	if a, b := refusals(w.alpha), refusals(w.beta); a != 2 || b != 1 {
		t.Errorf("after adding by id to alpha: refusals = %d on alpha, %d on beta; want still 2 and 1", a, b)
	}
	// P4: joined via the link, removed, added back by id, removed again —
	// the same link (and the other old one) still does not admit.
	if err := w.st.RemoveCampaignGuest(ctx, w.alpha, res.User.ID); err != nil {
		t.Fatalf("third RemoveCampaignGuest: %v", err)
	}
	for _, link := range []string{"party", "party-2"} {
		if _, err := w.st.RedeemCampaignInvitation(ctx, link, redemption(guestDiscordID, "Mika")); !errors.Is(err, storage.ErrInvitationInvalid) {
			t.Errorf("P4: redeeming %q after the re-add was removed = %v, want ErrInvitationInvalid", link, err)
		}
	}
	if _, err := w.st.GetCampaignGuest(ctx, w.alpha, res.User.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("P4: guest row = %v, want none", err)
	}
}

// TestRemoveCampaignGuest_AddedByIDThenLink pins review probe P3: a guest the
// GM added by Discord User ID — never through a link — is refused the
// Campaign's still-live link once removed. Without that the first removal is
// undone by the link the GM posted in the group chat, with the LINK's rights,
// which may be wider than anything the GM gave this person.
func TestRemoveCampaignGuest_AddedByIDThenLink(t *testing.T) {
	w := newGuestWorld(t)
	ctx := context.Background()

	w.mintCampaign(t, w.alpha, "party", func(n *storage.NewInvitation) { n.Rights = []string{"wiki.read", "maps.read"} })
	added := w.addGuest(t, w.alpha, guestDiscordID, "wiki.read")
	if err := w.st.RemoveCampaignGuest(ctx, w.alpha, added.UserID); err != nil {
		t.Fatalf("RemoveCampaignGuest: %v", err)
	}

	if res, err := w.st.RedeemCampaignInvitation(ctx, "party", redemption(guestDiscordID, "Mika")); !errors.Is(err, storage.ErrInvitationInvalid) {
		t.Fatalf("a removed added-by-id guest opening the live link = %+v, %v; want ErrInvitationInvalid", res, err)
	}
	if _, err := w.st.GetCampaignGuest(ctx, w.alpha, added.UserID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("guest row after the refused redemption = %v, want none", err)
	}
	if uses := invitationUses(t, w.st, "party"); uses != 0 {
		t.Errorf("uses = %d after a refused redemption, want 0", uses)
	}
	// The link still admits everybody else, and a link minted afterwards
	// admits the removed person too.
	if _, err := w.st.RedeemCampaignInvitation(ctx, "party", redemption(otherDiscordID, "Noor")); err != nil {
		t.Errorf("another person redeeming the same link: %v", err)
	}
	w.mintCampaign(t, w.alpha, "party-2", nil)
	if again, err := w.st.RedeemCampaignInvitation(ctx, "party-2", redemption(guestDiscordID, "Mika")); err != nil || !again.Changed {
		t.Errorf("a link minted after the removal = %+v, %v; want a fresh guest row", again, err)
	}
}

// TestArchiveCampaign_ClearsGuestSessionSelections: archiving drops the
// session selections pointing at the Campaign, so unarchiving it later does
// not silently move a session that has been working in the user's own Tenant
// back into the host's Campaign. The guest row is kept.
func TestArchiveCampaign_ClearsGuestSessionSelections(t *testing.T) {
	w := newGuestWorld(t)
	ctx := context.Background()

	// A GM with a Tenant of their own who is also a guest of alpha.
	own := foundTenant(t, w.st, guestDiscordID, "tok-guest", nil)
	guest := own.User
	w.addGuest(t, w.alpha, guestDiscordID, "wiki.edit")
	if err := w.st.SetSessionWorkspace(ctx, guest.ID, "tok-guest", &w.alpha); err != nil {
		t.Fatalf("SetSessionWorkspace: %v", err)
	}
	if ws, err := w.st.ResolveWorkspace(ctx, guest.ID, "tok-guest", false); err != nil || ws.CampaignID != w.alpha {
		t.Fatalf("fixture: workspace = %+v, %v; want acting as alpha's guest", ws, err)
	}

	if _, err := w.st.ArchiveCampaign(ctx, w.tenant, w.alpha); err != nil {
		t.Fatalf("ArchiveCampaign: %v", err)
	}
	if n := countAccessRows(t, w.pool, `SELECT count(*) FROM sessions WHERE guest_campaign_id = $1`, w.alpha); n != 0 {
		t.Errorf("%d sessions still select the archived campaign", n)
	}
	if ws, err := w.st.ResolveWorkspace(ctx, guest.ID, "tok-guest", false); err != nil || ws.Guest || ws.TenantID != own.Tenant.ID {
		t.Fatalf("while archived: workspace = %+v, %v; want the user's own tenant", ws, err)
	}

	if _, err := w.st.UnarchiveCampaign(ctx, w.tenant, w.alpha); err != nil {
		t.Fatalf("UnarchiveCampaign: %v", err)
	}
	if ws, err := w.st.ResolveWorkspace(ctx, guest.ID, "tok-guest", false); err != nil || ws.Guest || ws.TenantID != own.Tenant.ID {
		t.Errorf("after the unarchive: workspace = %+v, %v; want STILL the user's own tenant — the host must not be able to move a live session", ws, err)
	}
	// Nothing was lost: the campaign is back in the switcher.
	if list, err := w.st.ListGuestWorkspaces(ctx, guest.ID); err != nil || len(list) != 1 || list[0].CampaignID != w.alpha {
		t.Errorf("guest workspaces after the unarchive = %v, %v; want alpha", list, err)
	}
}
