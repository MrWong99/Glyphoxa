package auth_test

// Campaign Guests at the auth tier (ADR-0065 lane 2): the OAuth callback's two
// guest routes, the public preview of a campaign invitation, the workspace
// switcher, and the guest fields of GetCurrentUser.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeGuests is a scripted GuestAdmissionStore recording every redemption.
type fakeGuests struct {
	// kinds maps a join token to the kind of invitation it names; a token
	// absent from it is unknown.
	kinds map[string]string
	// redeem maps a campaign token to what its redemption yields; a campaign
	// token absent from it is a dead link.
	redeem      map[string]storage.CampaignRedeemed
	standing    map[string]storage.GuestStanding
	peekErr     error
	redeemErr   error
	standingErr error

	redeemedTokens []string
	redemptions    []storage.CampaignRedemption
}

func (f *fakeGuests) GetInvitationByToken(_ context.Context, token string) (storage.Invitation, error) {
	if f.peekErr != nil {
		return storage.Invitation{}, f.peekErr
	}
	kind, ok := f.kinds[token]
	if !ok {
		return storage.Invitation{}, storage.ErrNotFound
	}
	return storage.Invitation{Kind: kind}, nil
}

func (f *fakeGuests) RedeemCampaignInvitation(_ context.Context, token string, p storage.CampaignRedemption) (storage.CampaignRedeemed, error) {
	f.redeemedTokens = append(f.redeemedTokens, token)
	f.redemptions = append(f.redemptions, p)
	if f.redeemErr != nil {
		return storage.CampaignRedeemed{}, f.redeemErr
	}
	res, ok := f.redeem[token]
	if !ok {
		return storage.CampaignRedeemed{}, storage.ErrInvitationInvalid
	}
	res.User.DiscordUserID = p.User.DiscordUserID
	return res, nil
}

func (f *fakeGuests) GuestStandingOf(_ context.Context, id string) (storage.GuestStanding, error) {
	if f.standingErr != nil {
		return storage.GuestStanding{}, f.standingErr
	}
	st, ok := f.standing[id]
	if !ok {
		return storage.GuestStanding{}, storage.ErrNotFound
	}
	return st, nil
}

const campaignToken = "campaign-token_for-test-01"

var invitee = auth.DiscordUser{ID: "888", Username: "mika", GlobalName: "Mika Tran", AvatarURL: "https://cdn/m.png"}

// guestAdmission is a fully wired policy in the given mode over the fakes.
// "42" is the Platform Admin throughout.
func guestAdmission(mode auth.AdmissionMode, sp *fakeSignup, ac *fakeAccess, gs *fakeGuests) auth.Admission {
	return auth.Admission{
		Mode:           mode,
		Allowlist:      auth.ParseOperatorAllowlist("42"),
		SignupPlanSlug: "free",
		Signup:         sp,
		Access:         ac,
		Guests:         gs,
	}
}

// assertNoTenantWork is the "never founds a Tenant" assertion: neither the
// signup/grant provisioning transaction nor the Platform Admin's
// claim-or-create tenant binding ran.
func assertNoTenantWork(t *testing.T, name string, sp *fakeSignup, store *fakeOAuthStore) {
	t.Helper()
	if sp.calls != 0 {
		t.Errorf("%s: ProvisionSignup ran %d times — a guest route founded (or tried to found) a Tenant", name, sp.calls)
	}
	if store.resolveN != 0 {
		t.Errorf("%s: ResolveOperatorTenant ran %d times — a guest route claimed/seeded a Tenant", name, store.resolveN)
	}
}

// Redeeming a campaign invitation makes the visitor a guest and signs them in
// acting in that Campaign — in BOTH Admission Modes and whoever they are — and
// never founds a Tenant.
func TestOAuthCallback_CampaignJoin_NeverFoundsATenant(t *testing.T) {
	t.Parallel()
	campaignID := uuid.New()
	userID := uuid.New()
	admin := auth.DiscordUser{ID: "42", Username: "ada", GlobalName: "Admin Ada"}

	cases := []struct {
		name string
		mode auth.AdmissionMode
		du   auth.DiscordUser
		ac   *fakeAccess
	}{
		{"allowlist, a stranger", auth.AdmissionAllowlist, invitee, &fakeAccess{}},
		{"open, a stranger", auth.AdmissionOpen, invitee, &fakeAccess{}},
		{"allowlist, a Platform Admin", auth.AdmissionAllowlist, admin, &fakeAccess{}},
		{"open, a Platform Admin", auth.AdmissionOpen, admin, &fakeAccess{}},
		{"allowlist, an Access Grant holder", auth.AdmissionAllowlist, invitee,
			&fakeAccess{grants: map[string]storage.AccessGrant{"888": {DiscordUserID: "888", PlatformKeys: true}}}},
	}
	for _, tc := range cases {
		sp := &fakeSignup{created: true}
		gs := &fakeGuests{
			kinds:  map[string]string{campaignToken: storage.InvitationCampaign},
			redeem: map[string]storage.CampaignRedeemed{campaignToken: {User: storage.User{ID: userID}, CampaignID: campaignID, Changed: true}},
		}
		store := &fakeOAuthStore{}
		before := time.Now()
		rec := accessRound{du: tc.du, aup: true, join: campaignToken}.run(t, guestAdmission(tc.mode, sp, tc.ac, gs), store)

		if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/" {
			t.Fatalf("%s: status = %d, Location = %q; want a redirect into the app", tc.name, rec.Code, loc)
		}
		assertNoTenantWork(t, tc.name, sp, store)
		if tc.ac.redeemed() {
			t.Errorf("%s: an ACCESS redemption ran for a campaign link: %v", tc.name, tc.ac.calls)
		}
		if !slices.Equal(gs.redeemedTokens, []string{campaignToken}) {
			t.Fatalf("%s: redeemed %v, want exactly the campaign token once", tc.name, gs.redeemedTokens)
		}
		got := gs.redemptions[0]
		if got.User.DiscordUserID != tc.du.ID || got.User.Name != tc.du.DisplayName() || got.User.Avatar != tc.du.AvatarURL {
			t.Errorf("%s: redeemed as %+v, want the Discord identity", tc.name, got.User)
		}
		if got.AUPAcceptedAt.Before(before) {
			t.Errorf("%s: AUPAcceptedAt = %v, want the acknowledgment recorded with the redemption", tc.name, got.AUPAcceptedAt)
		}
		// The redemption upserts the user itself; nothing else may write one.
		if store.upsertCalls != 0 {
			t.Errorf("%s: UpsertUser ran outside the redemption transaction", tc.name)
		}
		if store.createCalls != 1 || store.created.UserID != userID ||
			store.created.GuestCampaignID == nil || *store.created.GuestCampaignID != campaignID {
			t.Errorf("%s: session = %+v (calls %d), want one session for the redeemer acting in campaign %s",
				tc.name, store.created, store.createCalls, campaignID)
		}
		c := respCookies(rec)
		if c["glyphoxa_session"] == nil || c["glyphoxa_session"].Value != store.created.Token || c["glyphoxa_csrf"] == nil {
			t.Errorf("%s: session/csrf cookies = %v", tc.name, c)
		}
		assertJoinCookieCleared(t, rec)
	}
}

// A campaign join that does not admit is terminal: it never falls through to a
// route that would found a Tenant — not even on an `open` deployment, where
// the same visitor could self-sign-up from the login screen.
func TestOAuthCallback_CampaignJoin_Refusals(t *testing.T) {
	t.Parallel()
	kinds := map[string]string{campaignToken: storage.InvitationCampaign}
	cases := []struct {
		name     string
		aup      bool
		gs       *fakeGuests
		wantCode int
		wantLoc  string
		wantTry  bool // a redemption was attempted
	}{
		{"no acknowledgment", false, &fakeGuests{kinds: kinds}, http.StatusFound,
			"/join/" + campaignToken + "?error=aup_required", false},
		{"a dead link", true, &fakeGuests{kinds: kinds}, http.StatusFound,
			"/join/" + campaignToken + "?error=invalid", true},
		{"a suspended redeemer", true, &fakeGuests{kinds: kinds, redeemErr: storage.ErrUserSuspended}, http.StatusFound,
			"/login?error=not_authorized", true},
		{"the store failing on the redemption", true, &fakeGuests{kinds: kinds, redeemErr: errors.New("db down")},
			http.StatusInternalServerError, "", true},
		{"the store failing on the kind lookup", true, &fakeGuests{peekErr: errors.New("db down")},
			http.StatusInternalServerError, "", false},
	}
	for _, mode := range []auth.AdmissionMode{auth.AdmissionAllowlist, auth.AdmissionOpen} {
		for _, tc := range cases {
			name := string(mode) + ": " + tc.name
			sp := &fakeSignup{created: true}
			store := &fakeOAuthStore{}
			// The visitor is ALSO an existing guest elsewhere: that must not
			// rescue a join that failed.
			tc.gs.standing = map[string]storage.GuestStanding{"888": {CampaignID: uuid.New(), Joined: true}}
			tc.gs.redeemedTokens, tc.gs.redemptions = nil, nil
			rec := accessRound{du: invitee, aup: tc.aup, join: campaignToken}.run(t, guestAdmission(mode, sp, &fakeAccess{}, tc.gs), store)

			if rec.Code != tc.wantCode || rec.Header().Get("Location") != tc.wantLoc {
				t.Errorf("%s: status = %d, Location = %q; want %d %q", name, rec.Code, rec.Header().Get("Location"), tc.wantCode, tc.wantLoc)
			}
			if tried := len(tc.gs.redeemedTokens) > 0; tried != tc.wantTry {
				t.Errorf("%s: redemption attempted = %v, want %v", name, tried, tc.wantTry)
			}
			assertNoSession(t, rec)
			assertNoTenantWork(t, name, sp, store)
			if store.createCalls != 0 || store.upsertCalls != 0 {
				t.Errorf("%s: wrote user/session for a refused join: %+v", name, store)
			}
			assertJoinCookieCleared(t, rec)
		}
	}
}

// Someone who already is a Campaign Guest signs in as that guest with no
// invitation at all — on an allowlist deployment whatever else they are, on an
// open one only when they JOINED (redeemed a link) and operate no Tenant.
func TestOAuthCallback_ExistingGuest(t *testing.T) {
	t.Parallel()
	campaignID := uuid.New()
	userID := uuid.New()
	// guestOnly was ADDED by Discord User ID; joined redeemed a campaign link.
	guestOnly := map[string]storage.GuestStanding{"888": {CampaignID: campaignID}}
	joined := map[string]storage.GuestStanding{"888": {CampaignID: campaignID, Joined: true}}
	alsoOperator := map[string]storage.GuestStanding{"888": {CampaignID: campaignID, OperatesTenant: true}}

	t.Run("admitted as a guest", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name     string
			mode     auth.AdmissionMode
			standing map[string]storage.GuestStanding
			aup      bool
		}{
			{"allowlist, added by id", auth.AdmissionAllowlist, guestOnly, false},
			{"allowlist, joined through a link", auth.AdmissionAllowlist, joined, false},
			// A GM whose Access Grant was revoked: still someone's guest, so
			// they get in — as a guest. (auth.Workspaces keeps them out of
			// their own Tenant on every request.)
			{"allowlist, operates a Tenant but holds no grant", auth.AdmissionAllowlist, alsoOperator, false},
			// Open mode: a guest who CHOSE to be one is NOT turned into a Tenant
			// founder by signing in.
			{"open, joined through a link", auth.AdmissionOpen, joined, true},
		} {
			sp := &fakeSignup{created: true}
			store := &fakeOAuthStore{userID: userID}
			gs := &fakeGuests{standing: tc.standing}
			rec := accessRound{du: invitee, aup: tc.aup}.run(t, guestAdmission(tc.mode, sp, &fakeAccess{}, gs), store)

			if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/" {
				t.Fatalf("%s: status = %d, Location = %q; want a redirect into the app", tc.name, rec.Code, loc)
			}
			assertNoTenantWork(t, tc.name, sp, store)
			// The sign-in refreshes the user row: for a guest a GM added by
			// snowflake this is what first fills in their name.
			if store.upsertCalls != 1 || store.upserted.Name != "Mika Tran" || store.upserted.DiscordUserID != "888" {
				t.Errorf("%s: upserted %+v (calls %d), want the Discord identity once", tc.name, store.upserted, store.upsertCalls)
			}
			if store.createCalls != 1 || store.created.UserID != userID ||
				store.created.GuestCampaignID == nil || *store.created.GuestCampaignID != campaignID {
				t.Errorf("%s: session = %+v, want one acting in the guest campaign", tc.name, store.created)
			}
			if len(gs.redeemedTokens) != 0 {
				t.Errorf("%s: a redemption ran with no invitation: %v", tc.name, gs.redeemedTokens)
			}
		}
	})

	// The lockout this guards against: a GM can add ANY Discord User ID as a
	// guest, with no consent and before that person ever signed in. If that
	// alone made them "a guest" at sign-in, any GM could keep anybody from
	// founding a Tenant on an open deployment, for good. Being added is not
	// choosing: they self-sign-up, and the Campaign waits in the switcher.
	t.Run("open mode: someone merely ADDED as a guest still self-signs-up", func(t *testing.T) {
		t.Parallel()
		sp := &fakeSignup{created: true}
		store := &fakeOAuthStore{userID: userID}
		rec := accessRound{du: invitee, aup: true}.run(t,
			guestAdmission(auth.AdmissionOpen, sp, &fakeAccess{}, &fakeGuests{standing: guestOnly}), store)
		if rec.Code != http.StatusFound {
			t.Fatalf("status = %d, want a redirect into the app", rec.Code)
		}
		if sp.calls != 1 {
			t.Errorf("ProvisionSignup ran %d times, want 1: being added as a guest must not block self-signup", sp.calls)
		}
		if store.createCalls != 0 {
			t.Errorf("a guest-only session was issued (%d) to someone who never chose to be a guest", store.createCalls)
		}
	})

	t.Run("open mode: a guest who operates a Tenant signs in to it", func(t *testing.T) {
		t.Parallel()
		sp := &fakeSignup{created: false}
		store := &fakeOAuthStore{}
		rec := accessRound{du: invitee, aup: true}.run(t,
			guestAdmission(auth.AdmissionOpen, sp, &fakeAccess{}, &fakeGuests{standing: alsoOperator}), store)
		if rec.Code != http.StatusFound || sp.calls != 1 || store.createCalls != 0 {
			t.Fatalf("status = %d, ProvisionSignup calls = %d, guest sessions = %d; want the returning-signup path",
				rec.Code, sp.calls, store.createCalls)
		}
	})

	t.Run("a grant holder who is also a guest signs in to their own Tenant", func(t *testing.T) {
		t.Parallel()
		sp := &fakeSignup{created: false}
		store := &fakeOAuthStore{}
		ac := &fakeAccess{grants: map[string]storage.AccessGrant{"888": {DiscordUserID: "888"}}}
		rec := accessRound{du: invitee}.run(t,
			guestAdmission(auth.AdmissionAllowlist, sp, ac, &fakeGuests{standing: guestOnly}), store)
		if rec.Code != http.StatusFound || sp.calls != 1 || store.createCalls != 0 {
			t.Fatalf("status = %d, provisioning calls = %d, guest sessions = %d; want the grant route",
				rec.Code, sp.calls, store.createCalls)
		}
	})

	t.Run("a dead ACCESS link does not stop an existing guest signing in", func(t *testing.T) {
		t.Parallel()
		sp := &fakeSignup{}
		store := &fakeOAuthStore{userID: userID}
		gs := &fakeGuests{standing: guestOnly, kinds: map[string]string{"old-access-link": storage.InvitationAccess}}
		rec := accessRound{du: invitee, aup: true, join: "old-access-link"}.run(t,
			guestAdmission(auth.AdmissionAllowlist, sp, &fakeAccess{}, gs), store)
		if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/" || store.createCalls != 1 {
			t.Fatalf("status = %d, Location = %q, sessions = %d; want the guest admitted", rec.Code, loc, store.createCalls)
		}
		assertNoTenantWork(t, "dead access link", sp, store)
	})

	t.Run("suspension wins", func(t *testing.T) {
		t.Parallel()
		for _, mode := range []auth.AdmissionMode{auth.AdmissionAllowlist, auth.AdmissionOpen} {
			now := time.Now()
			sp := &fakeSignup{}
			store := &fakeOAuthStore{userID: userID, suspendedAt: &now}
			rec := accessRound{du: invitee, aup: true}.run(t,
				guestAdmission(mode, sp, &fakeAccess{}, &fakeGuests{standing: joined}), store)
			if loc := rec.Header().Get("Location"); loc != "/login?error=not_authorized" {
				t.Errorf("%s: Location = %q, want the not_authorized bounce", mode, loc)
			}
			assertNoSession(t, rec)
			if store.createCalls != 0 {
				t.Errorf("%s: a session was created for a suspended guest", mode)
			}
			assertNoTenantWork(t, string(mode), sp, store)
		}
	})

	t.Run("a store failure admits nobody", func(t *testing.T) {
		t.Parallel()
		for _, mode := range []auth.AdmissionMode{auth.AdmissionAllowlist, auth.AdmissionOpen} {
			sp := &fakeSignup{}
			store := &fakeOAuthStore{}
			rec := accessRound{du: invitee, aup: true}.run(t,
				guestAdmission(mode, sp, &fakeAccess{}, &fakeGuests{standingErr: errors.New("db down")}), store)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("%s: status = %d, want 500", mode, rec.Code)
			}
			assertNoSession(t, rec)
			assertNoTenantWork(t, string(mode), sp, store)
		}
	})

	t.Run("nobody is a guest without a Guests store", func(t *testing.T) {
		t.Parallel()
		sp := &fakeSignup{}
		store := &fakeOAuthStore{}
		adm := guestAdmission(auth.AdmissionAllowlist, sp, &fakeAccess{}, nil)
		adm.Guests = nil
		// A campaign-shaped token is then just a join that failed.
		rec := accessRound{du: invitee, aup: true, join: campaignToken}.run(t, adm, store)
		if loc := rec.Header().Get("Location"); loc != "/join/"+campaignToken+"?error=invalid" {
			t.Errorf("Location = %q, want the join page's invalid error", loc)
		}
		assertNoSession(t, rec)
	})

	t.Run("a stranger is still refused", func(t *testing.T) {
		t.Parallel()
		sp := &fakeSignup{}
		store := &fakeOAuthStore{}
		rec := accessRound{du: invitee}.run(t,
			guestAdmission(auth.AdmissionAllowlist, sp, &fakeAccess{}, &fakeGuests{}), store)
		if loc := rec.Header().Get("Location"); loc != "/login?error=not_authorized" {
			t.Errorf("Location = %q, want the not_authorized bounce", loc)
		}
		assertNoSession(t, rec)
		if store.upsertCalls != 0 {
			t.Error("a users row was written for a stranger who was refused")
		}
	})
}

// A live campaign invitation previews what it opens and what it grants; a link
// to an archived (or deleted) Campaign previews like any other dead link.
func TestGetInvitation_Campaign(t *testing.T) {
	t.Parallel()
	inviter, live, archived := uuid.New(), uuid.New(), uuid.New()
	past := time.Now().Add(-time.Hour)
	inv := fakeInvitations{
		users:     map[uuid.UUID]storage.User{inviter: {ID: inviter, Name: "GM Gil"}},
		campaigns: map[uuid.UUID]storage.InvitationTarget{live: {CampaignName: "Tides over Saltmarsh", TenantName: "Gil's Table"}},
		byToken: map[string]storage.Invitation{
			// maps.edit is stored without its implied maps.read here on
			// purpose: the preview normalizes, so a hand-written row still
			// shows what the redeemer will actually hold.
			"live":     {Kind: storage.InvitationCampaign, CreatedBy: &inviter, CampaignID: &live, Rights: []string{"maps.edit", "transcripts.read", "bogus"}},
			"no-right": {Kind: storage.InvitationCampaign, CreatedBy: &inviter, CampaignID: &live},
			"archived": {Kind: storage.InvitationCampaign, CreatedBy: &inviter, CampaignID: &archived, Rights: []string{"wiki.read"}},
			"revoked":  {Kind: storage.InvitationCampaign, CreatedBy: &inviter, CampaignID: &live, RevokedAt: &past},
			"broken":   {Kind: storage.InvitationCampaign, CreatedBy: &inviter},
		},
	}
	client := accessAuthClient(t, fakeAuthN{}, fakeTenant{}, func(s *auth.AuthServer) { s.SetInvitations(inv) })
	preview := func(token string) *managementv1.GetInvitationResponse {
		t.Helper()
		resp, err := client.GetInvitation(context.Background(), connect.NewRequest(&managementv1.GetInvitationRequest{Token: token}))
		if err != nil {
			t.Fatalf("GetInvitation(%q): %v", token, err)
		}
		return resp.Msg
	}

	got := preview("live")
	wantRights := []managementv1.GuestRight{
		managementv1.GuestRight_GUEST_RIGHT_MAPS_READ,
		managementv1.GuestRight_GUEST_RIGHT_MAPS_EDIT,
		managementv1.GuestRight_GUEST_RIGHT_TRANSCRIPTS_READ,
	}
	if !got.GetValid() || got.GetKind() != managementv1.InvitationKind_INVITATION_KIND_CAMPAIGN ||
		got.GetInviterName() != "GM Gil" || got.GetCampaignName() != "Tides over Saltmarsh" ||
		got.GetTenantName() != "Gil's Table" || !slices.Equal(got.GetRights(), wantRights) {
		t.Errorf("live campaign preview = %+v", got)
	}
	if got := preview("no-right"); !got.GetValid() || len(got.GetRights()) != 0 {
		t.Errorf("rights-less campaign preview = %+v, want valid with no rights", got)
	}
	for _, token := range []string{"archived", "revoked", "broken"} {
		got := preview(token)
		if got.GetValid() || got.GetKind() != managementv1.InvitationKind_INVITATION_KIND_UNSPECIFIED ||
			got.GetCampaignName() != "" || got.GetTenantName() != "" || got.GetInviterName() != "" || len(got.GetRights()) != 0 {
			t.Errorf("%s: preview = %+v, want valid=false with every other field empty", token, got)
		}
	}

	broken := accessAuthClient(t, fakeAuthN{}, fakeTenant{}, func(s *auth.AuthServer) {
		failing := inv
		failing.campaignErr = errors.New("db down")
		s.SetInvitations(failing)
	})
	_, err := broken.GetInvitation(context.Background(), connect.NewRequest(&managementv1.GetInvitationRequest{Token: "live"}))
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Errorf("campaign lookup failure = %v, want CodeInternal", err)
	}
}

// fakeGuestWorkspaces is a scripted GuestWorkspaceStore.
type fakeGuestWorkspaces struct {
	list    []storage.GuestWorkspace
	listErr error
	// guestOf is the set of campaigns SetSessionWorkspace accepts.
	guestOf map[uuid.UUID]bool
	setErr  error

	setCalls  int
	gotUser   uuid.UUID
	gotToken  string
	gotTarget *uuid.UUID
}

func (f *fakeGuestWorkspaces) ListGuestWorkspaces(context.Context, uuid.UUID) ([]storage.GuestWorkspace, error) {
	return f.list, f.listErr
}

func (f *fakeGuestWorkspaces) SetSessionWorkspace(_ context.Context, userID uuid.UUID, token string, campaignID *uuid.UUID) error {
	f.setCalls++
	f.gotUser, f.gotToken, f.gotTarget = userID, token, campaignID
	if f.setErr != nil {
		return f.setErr
	}
	if campaignID != nil && !f.guestOf[*campaignID] {
		return storage.ErrNotFound
	}
	return nil
}

// ownTenantStore scripts auth.Workspaces' store for the AuthServer tests.
type ownTenantStore struct {
	own uuid.UUID // uuid.Nil = operates no usable Tenant
	err error
}

func (o ownTenantStore) ResolveWorkspace(context.Context, uuid.UUID, string, bool) (storage.Workspace, error) {
	return storage.Workspace{}, storage.ErrNotFound
}

func (o ownTenantStore) OwnTenantForUser(context.Context, uuid.UUID, bool) (uuid.UUID, error) {
	switch {
	case o.err != nil:
		return uuid.Nil, o.err
	case o.own == uuid.Nil:
		return uuid.Nil, storage.ErrNotFound
	}
	return o.own, nil
}

// workspaceAuthClient mounts the real AuthServer behind the real stack over a
// policy that resolves the session to ws, with the Campaign Guest seams wired.
func workspaceAuthClient(t *testing.T, u storage.User, ws auth.WorkspaceResolver, own ownTenantStore, guests auth.GuestWorkspaceStore, tenants map[uuid.UUID]storage.Tenant) managementv1connect.AuthServiceClient {
	t.Helper()
	authn := fakeAuthN{users: map[string]storage.User{validToken: u}}
	stack := auth.NewPolicy(authn, nil).WithWorkspaces(ws).Stack(
		managementv1connect.AuthServiceGetCurrentUserProcedure)
	server := auth.NewAuthServer(&fakeDeleter{}, fakeNamer{tenants: tenants}, &fakeRenamer{}, auth.AdmissionAllowlist, auth.EdgeConfig{}, slog.New(slog.DiscardHandler))
	server.SetWorkspaces(auth.Workspaces{Store: own, Mode: auth.AdmissionOpen}, guests)
	mux := http.NewServeMux()
	mux.Handle(server.Handler(stack.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return managementv1connect.NewAuthServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())
}

// GetCurrentUser as a Campaign Guest names the HOST Tenant, describes the
// guest workspace with the rights the request was authorized with, and lists
// everything shared with the caller.
func TestGetCurrentUser_GuestWorkspace(t *testing.T) {
	t.Parallel()
	u := storage.User{ID: uuid.New(), DiscordUserID: "888", Name: "Mika Tran", Role: "operator"}
	host, campaignA, campaignB := uuid.New(), uuid.New(), uuid.New()
	tenants := map[uuid.UUID]storage.Tenant{host: {ID: host, Name: "Gil's Table"}}
	guests := &fakeGuestWorkspaces{list: []storage.GuestWorkspace{
		// The list's rights for A deliberately disagree with what the policy
		// authorized (a change racing the request): `guest` must report the
		// authorized ones.
		{CampaignID: campaignA, CampaignName: "Saltmarsh", TenantName: "Gil's Table", Rights: []string{"wiki.read", "transcripts.read"}},
		{CampaignID: campaignB, CampaignName: "Strahd", TenantName: "Ana's Table", Rights: []string{"planning.use"}},
	}}

	t.Run("acting as a guest", func(t *testing.T) {
		t.Parallel()
		client := workspaceAuthClient(t, u, guestOf(host, campaignA, auth.RightMapsEdit), ownTenantStore{}, guests, tenants)
		resp, err := client.GetCurrentUser(context.Background(), signedIn(&managementv1.GetCurrentUserRequest{}))
		if err != nil {
			t.Fatalf("GetCurrentUser: %v", err)
		}
		got := resp.Msg
		if got.GetTenantId() != host.String() || got.GetTenantName() != "Gil's Table" {
			t.Errorf("tenant = %q/%q, want the HOST tenant", got.GetTenantId(), got.GetTenantName())
		}
		if got.GetHasOwnTenant() {
			t.Error("has_own_tenant = true for someone who operates none")
		}
		g := got.GetGuest()
		wantRights := []managementv1.GuestRight{managementv1.GuestRight_GUEST_RIGHT_MAPS_READ, managementv1.GuestRight_GUEST_RIGHT_MAPS_EDIT}
		if g == nil || g.GetCampaignId() != campaignA.String() || g.GetCampaignName() != "Saltmarsh" ||
			g.GetTenantName() != "Gil's Table" || !slices.Equal(g.GetRights(), wantRights) {
			t.Errorf("guest = %+v, want campaign A with the authorized rights %v", g, wantRights)
		}
		if len(got.GetGuestWorkspaces()) != 2 || got.GetGuestWorkspaces()[1].GetCampaignId() != campaignB.String() {
			t.Fatalf("guest_workspaces = %+v, want both shared campaigns", got.GetGuestWorkspaces())
		}
		// planning.use is listed with what it implies.
		wantB := []managementv1.GuestRight{
			managementv1.GuestRight_GUEST_RIGHT_WIKI_READ, managementv1.GuestRight_GUEST_RIGHT_WIKI_SECRETS,
			managementv1.GuestRight_GUEST_RIGHT_PLANNING_USE,
		}
		if !slices.Equal(got.GetGuestWorkspaces()[1].GetRights(), wantB) {
			t.Errorf("campaign B rights = %v, want %v", got.GetGuestWorkspaces()[1].GetRights(), wantB)
		}
	})

	t.Run("acting in their own tenant", func(t *testing.T) {
		t.Parallel()
		own := uuid.New()
		ownTenants := map[uuid.UUID]storage.Tenant{own: {ID: own, Name: "Mika's Table"}}
		client := workspaceAuthClient(t, u, &fakeWorkspaces{ws: storage.Workspace{TenantID: own}}, ownTenantStore{own: own}, guests, ownTenants)
		resp, err := client.GetCurrentUser(context.Background(), signedIn(&managementv1.GetCurrentUserRequest{}))
		if err != nil {
			t.Fatalf("GetCurrentUser: %v", err)
		}
		got := resp.Msg
		if got.GetGuest() != nil {
			t.Errorf("guest = %+v, want unset for a Tenant's own operator", got.GetGuest())
		}
		if !got.GetHasOwnTenant() || got.GetTenantName() != "Mika's Table" || len(got.GetGuestWorkspaces()) != 2 {
			t.Errorf("own-tenant response = %+v", got)
		}
	})

	t.Run("the guest row vanished mid-request", func(t *testing.T) {
		t.Parallel()
		client := workspaceAuthClient(t, u, guestOf(host, campaignA, auth.RightWikiRead), ownTenantStore{}, &fakeGuestWorkspaces{}, tenants)
		resp, err := client.GetCurrentUser(context.Background(), signedIn(&managementv1.GetCurrentUserRequest{}))
		if err != nil {
			t.Fatalf("GetCurrentUser: %v", err)
		}
		// Authorized as a guest, so never rendered as a GM shell.
		if g := resp.Msg.GetGuest(); g == nil || g.GetCampaignId() != campaignA.String() {
			t.Errorf("guest = %+v, want the authorized guest scope still reported", g)
		}
	})

	t.Run("a list failure is an internal error", func(t *testing.T) {
		t.Parallel()
		client := workspaceAuthClient(t, u, guestOf(host, campaignA), ownTenantStore{}, &fakeGuestWorkspaces{listErr: errors.New("db down")}, tenants)
		_, err := client.GetCurrentUser(context.Background(), signedIn(&managementv1.GetCurrentUserRequest{}))
		if connect.CodeOf(err) != connect.CodeInternal {
			t.Errorf("err = %v, want CodeInternal", err)
		}
	})
}

// SwitchWorkspace writes the session's selection and nothing else: a campaign
// the caller is not a guest of is NotFound, and "my own tenant" needs one.
func TestSwitchWorkspace(t *testing.T) {
	t.Parallel()
	u := storage.User{ID: uuid.New(), DiscordUserID: "888", Name: "Mika Tran"}
	host, shared, foreign, own := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	asGuest := func() auth.WorkspaceResolver { return guestOf(host, shared) }
	call := func(t *testing.T, client managementv1connect.AuthServiceClient, campaignID string) error {
		t.Helper()
		_, err := client.SwitchWorkspace(context.Background(), signedIn(&managementv1.SwitchWorkspaceRequest{CampaignId: campaignID}))
		return err
	}

	t.Run("to a campaign shared with the caller", func(t *testing.T) {
		t.Parallel()
		guests := &fakeGuestWorkspaces{guestOf: map[uuid.UUID]bool{shared: true}}
		client := workspaceAuthClient(t, u, &fakeWorkspaces{ws: storage.Workspace{TenantID: own}}, ownTenantStore{own: own}, guests, nil)
		if err := call(t, client, shared.String()); err != nil {
			t.Fatalf("SwitchWorkspace: %v", err)
		}
		if guests.setCalls != 1 || guests.gotUser != u.ID || guests.gotToken != validToken ||
			guests.gotTarget == nil || *guests.gotTarget != shared {
			t.Errorf("selection written = user %s token %q target %v, want this session → the shared campaign",
				guests.gotUser, guests.gotToken, guests.gotTarget)
		}
	})

	t.Run("to a campaign the caller is not a guest of", func(t *testing.T) {
		t.Parallel()
		guests := &fakeGuestWorkspaces{guestOf: map[uuid.UUID]bool{shared: true}}
		client := workspaceAuthClient(t, u, asGuest(), ownTenantStore{}, guests, nil)
		if err := call(t, client, foreign.String()); connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("err = %v, want CodeNotFound", err)
		}
	})

	t.Run("back to the caller's own tenant, from a guest workspace", func(t *testing.T) {
		t.Parallel()
		guests := &fakeGuestWorkspaces{}
		client := workspaceAuthClient(t, u, asGuest(), ownTenantStore{own: own}, guests, nil)
		if err := call(t, client, ""); err != nil {
			t.Fatalf("SwitchWorkspace(\"\") as a guest: %v (the guest gate must let every guest switch)", err)
		}
		if guests.setCalls != 1 || guests.gotTarget != nil {
			t.Errorf("selection written = %v (calls %d), want it cleared once", guests.gotTarget, guests.setCalls)
		}
	})

	t.Run("to an own tenant the caller does not have", func(t *testing.T) {
		t.Parallel()
		guests := &fakeGuestWorkspaces{}
		client := workspaceAuthClient(t, u, asGuest(), ownTenantStore{}, guests, nil)
		if err := call(t, client, ""); connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Errorf("err = %v, want CodeFailedPrecondition", err)
		}
		if guests.setCalls != 0 {
			t.Error("the selection was cleared for someone with no tenant of their own")
		}
	})

	t.Run("a malformed campaign id", func(t *testing.T) {
		t.Parallel()
		guests := &fakeGuestWorkspaces{}
		client := workspaceAuthClient(t, u, asGuest(), ownTenantStore{}, guests, nil)
		if err := call(t, client, "not-a-uuid"); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("err = %v, want CodeInvalidArgument", err)
		}
		if guests.setCalls != 0 {
			t.Error("the store was reached with a malformed id")
		}
	})

	t.Run("store failures are internal errors", func(t *testing.T) {
		t.Parallel()
		client := workspaceAuthClient(t, u, asGuest(), ownTenantStore{}, &fakeGuestWorkspaces{setErr: errors.New("db down")}, nil)
		if err := call(t, client, shared.String()); connect.CodeOf(err) != connect.CodeInternal {
			t.Errorf("set failure: err = %v, want CodeInternal", err)
		}
		client = workspaceAuthClient(t, u, asGuest(), ownTenantStore{err: errors.New("db down")}, &fakeGuestWorkspaces{}, nil)
		if err := call(t, client, ""); connect.CodeOf(err) != connect.CodeInternal {
			t.Errorf("own-tenant failure: err = %v, want CodeInternal", err)
		}
	})

	t.Run("it is state-changing: no CSRF pair, no switch", func(t *testing.T) {
		t.Parallel()
		guests := &fakeGuestWorkspaces{guestOf: map[uuid.UUID]bool{shared: true}}
		client := workspaceAuthClient(t, u, asGuest(), ownTenantStore{}, guests, nil)
		req := connect.NewRequest(&managementv1.SwitchWorkspaceRequest{CampaignId: shared.String()})
		req.Header().Set("Cookie", auth.SessionCookieName+"="+validToken)
		if _, err := client.SwitchWorkspace(context.Background(), req); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("err = %v, want CodePermissionDenied", err)
		}
		if guests.setCalls != 0 {
			t.Error("the selection was written without the CSRF pair")
		}
	})

	t.Run("unwired, it is unavailable rather than silently accepting", func(t *testing.T) {
		t.Parallel()
		client := accessAuthClient(t, fakeAuthN{users: map[string]storage.User{validToken: u}}, fakeTenant{id: own}, nil)
		if err := call(t, client, shared.String()); connect.CodeOf(err) != connect.CodeUnimplemented {
			t.Errorf("err = %v, want CodeUnimplemented", err)
		}
	})
}

// RenameTenant refuses a Campaign Guest in the handler too: the context's
// Tenant is the host's, so a guest who got past the gate would rename someone
// else's Tenant.
func TestRenameTenant_RefusesAGuestInTheHandler(t *testing.T) {
	t.Parallel()
	renamer := &fakeRenamer{}
	server := auth.NewAuthServer(&fakeDeleter{}, fakeNamer{}, renamer, auth.AdmissionAllowlist, auth.EdgeConfig{}, slog.New(slog.DiscardHandler))
	host := uuid.New()
	ctx := auth.WithTenant(auth.WithUser(context.Background(), operator()), host)
	ctx = auth.WithGuest(ctx, auth.GuestScope{CampaignID: uuid.New(), Rights: auth.NewRightSet(allGuestRights...)})

	_, err := server.RenameTenant(ctx, connect.NewRequest(&managementv1.RenameTenantRequest{Name: "Mine now"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("err = %v, want CodePermissionDenied", err)
	}
	if renamer.gotName != "" {
		t.Fatalf("the host tenant was renamed to %q by a guest", renamer.gotName)
	}
}
