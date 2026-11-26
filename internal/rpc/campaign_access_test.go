package rpc_test

// Campaign Guest management (ADR-0065): the six GM-only procedures that decide
// who else may open the Active Campaign.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeAccessStore is an in-memory campaignAccessStore recording its calls.
type fakeAccessStore struct {
	calls []string

	guests      map[uuid.UUID][]storage.CampaignGuest // by campaign
	invitations map[uuid.UUID][]storage.Invitation    // by campaign
	operator    string                                // the tenant operator's snowflake

	added struct {
		campaign uuid.UUID
		id       string
		rights   []string
		by       uuid.UUID
	}
	updated struct {
		campaign, user uuid.UUID
		rights         []string
	}
	removed struct{ campaign, user uuid.UUID }
	minted  storage.NewInvitation
	revoked struct{ campaign, id uuid.UUID }

	err error
}

func newFakeAccessStore() *fakeAccessStore {
	return &fakeAccessStore{guests: map[uuid.UUID][]storage.CampaignGuest{}, invitations: map[uuid.UUID][]storage.Invitation{}}
}

func (f *fakeAccessStore) ListCampaignGuests(_ context.Context, campaignID uuid.UUID) ([]storage.CampaignGuest, error) {
	f.calls = append(f.calls, "ListCampaignGuests")
	return f.guests[campaignID], f.err
}

func (f *fakeAccessStore) ListCampaignInvitations(_ context.Context, campaignID uuid.UUID) ([]storage.Invitation, error) {
	f.calls = append(f.calls, "ListCampaignInvitations")
	return f.invitations[campaignID], f.err
}

func (f *fakeAccessStore) AddCampaignGuest(_ context.Context, campaignID uuid.UUID, id string, rights []string, by uuid.UUID) (storage.CampaignGuest, error) {
	f.calls = append(f.calls, "AddCampaignGuest")
	f.added.campaign, f.added.id, f.added.rights, f.added.by = campaignID, id, rights, by
	if f.err != nil {
		return storage.CampaignGuest{}, f.err
	}
	if id == f.operator {
		return storage.CampaignGuest{}, storage.ErrGuestIsOperator
	}
	return storage.CampaignGuest{CampaignID: campaignID, UserID: uuid.New(), DiscordUserID: id, Rights: rights, AddedAt: adminNow}, nil
}

func (f *fakeAccessStore) find(campaignID, userID uuid.UUID) (storage.CampaignGuest, bool) {
	for _, g := range f.guests[campaignID] {
		if g.UserID == userID {
			return g, true
		}
	}
	return storage.CampaignGuest{}, false
}

func (f *fakeAccessStore) UpdateCampaignGuest(_ context.Context, campaignID, userID uuid.UUID, rights []string) (storage.CampaignGuest, error) {
	f.calls = append(f.calls, "UpdateCampaignGuest")
	f.updated.campaign, f.updated.user, f.updated.rights = campaignID, userID, rights
	if f.err != nil {
		return storage.CampaignGuest{}, f.err
	}
	g, ok := f.find(campaignID, userID)
	if !ok {
		return storage.CampaignGuest{}, storage.ErrNotFound
	}
	g.Rights = rights
	return g, nil
}

func (f *fakeAccessStore) RemoveCampaignGuest(_ context.Context, campaignID, userID uuid.UUID) error {
	f.calls = append(f.calls, "RemoveCampaignGuest")
	f.removed.campaign, f.removed.user = campaignID, userID
	if f.err != nil {
		return f.err
	}
	if _, ok := f.find(campaignID, userID); !ok {
		return storage.ErrNotFound
	}
	return nil
}

func (f *fakeAccessStore) CreateInvitation(_ context.Context, n storage.NewInvitation) (storage.Invitation, error) {
	f.calls = append(f.calls, "CreateInvitation")
	f.minted = n
	if f.err != nil {
		return storage.Invitation{}, f.err
	}
	return storage.Invitation{
		ID: uuid.New(), Kind: n.Kind, Label: n.Label, CampaignID: n.CampaignID, Rights: n.Rights,
		DiscordUserID: n.DiscordUserID, MaxUses: n.MaxUses, ExpiresAt: n.ExpiresAt, CreatedBy: &n.CreatedBy, CreatedAt: adminNow,
	}, nil
}

func (f *fakeAccessStore) RevokeCampaignInvitation(_ context.Context, campaignID, id uuid.UUID) error {
	f.calls = append(f.calls, "RevokeCampaignInvitation")
	f.revoked.campaign, f.revoked.id = campaignID, id
	if f.err != nil {
		return f.err
	}
	for _, inv := range f.invitations[campaignID] {
		if inv.ID == id {
			return nil
		}
	}
	return storage.ErrNotFound
}

// accessWorld is a CampaignServer whose Active Campaign for the GM is
// `campaign`; a second campaign of the tenant exists to prove scoping.
type accessWorld struct {
	hostWorld
	store *fakeAccessStore
	srv   *rpc.CampaignServer
	gm    context.Context
	gmID  uuid.UUID
}

func newAccessWorld() accessWorld {
	w := newHostWorld()
	// For THIS module the GM's Active Campaign is the guest-facing one.
	w.active.campaign = w.guestCampaign
	w.active.forUser = w.guestCampaign
	store := newFakeAccessStore()
	store.operator = "42"
	gmUser := storage.User{ID: uuid.New(), DiscordUserID: "42", Name: "GM Gil"}
	return accessWorld{
		hostWorld: w, store: store,
		srv:  rpc.NewCampaignServerWith(rpc.CampaignStores{Active: w.active, Access: store}),
		gm:   auth.WithTenant(auth.WithUser(context.Background(), gmUser), w.tenant),
		gmID: gmUser.ID,
	}
}

// TestCampaignAccess_GuestsAreRefusedInTheHandler: none of the six procedures
// is in the guest table, so the gate refuses a guest before these handlers
// run. They refuse again themselves — a guest who could manage guests could
// grant themselves everything — and never touch the store.
func TestCampaignAccess_GuestsAreRefusedInTheHandler(t *testing.T) {
	t.Parallel()
	w := newAccessWorld()
	everything := asGuest(w.tenant, w.guestCampaign.ID,
		auth.RightWikiEdit, auth.RightMapsEdit, auth.RightTranscriptsRead, auth.RightHighlightsRead, auth.RightPlanningUse)
	target := uuid.NewString()
	calls := map[string]func(ctx context.Context) error{
		"ListCampaignAccess": func(ctx context.Context) error {
			_, err := w.srv.ListCampaignAccess(ctx, connect.NewRequest(&managementv1.ListCampaignAccessRequest{}))
			return err
		},
		"AddCampaignGuest": func(ctx context.Context) error {
			_, err := w.srv.AddCampaignGuest(ctx, connect.NewRequest(&managementv1.AddCampaignGuestRequest{
				DiscordUserId: "888", Rights: []managementv1.GuestRight{managementv1.GuestRight_GUEST_RIGHT_WIKI_EDIT},
			}))
			return err
		},
		"UpdateCampaignGuest": func(ctx context.Context) error {
			_, err := w.srv.UpdateCampaignGuest(ctx, connect.NewRequest(&managementv1.UpdateCampaignGuestRequest{UserId: target}))
			return err
		},
		"RemoveCampaignGuest": func(ctx context.Context) error {
			_, err := w.srv.RemoveCampaignGuest(ctx, connect.NewRequest(&managementv1.RemoveCampaignGuestRequest{UserId: target}))
			return err
		},
		"CreateCampaignInvitation": func(ctx context.Context) error {
			_, err := w.srv.CreateCampaignInvitation(ctx, connect.NewRequest(&managementv1.CreateCampaignInvitationRequest{}))
			return err
		},
		"RevokeCampaignInvitation": func(ctx context.Context) error {
			_, err := w.srv.RevokeCampaignInvitation(ctx, connect.NewRequest(&managementv1.RevokeCampaignInvitationRequest{Id: target}))
			return err
		},
	}
	if len(calls) != 6 {
		t.Fatalf("covering %d procedures, want all 6", len(calls))
	}
	for name, call := range calls {
		if err := call(everything); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("%s as a guest holding every right: err = %v, want CodePermissionDenied", name, err)
		}
		if err := call(auth.WithTenant(context.Background(), w.tenant)); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("%s with no user: err = %v, want CodeUnauthenticated", name, err)
		}
	}
	if len(w.store.calls) != 0 {
		t.Fatalf("a refused caller reached the store: %v", w.store.calls)
	}
}

func TestCampaignAccess_List(t *testing.T) {
	t.Parallel()
	w := newAccessWorld()
	mika, noor := uuid.New(), uuid.New()
	one := 1
	past := adminNow.Add(-time.Hour)
	w.store.guests[w.guestCampaign.ID] = []storage.CampaignGuest{
		{CampaignID: w.guestCampaign.ID, UserID: mika, DiscordUserID: "888", Name: "Mika", Avatar: "https://cdn/m.png",
			Rights: []string{"maps.edit"}, AddedAt: adminNow, HasSignedIn: true},
		{CampaignID: w.guestCampaign.ID, UserID: noor, DiscordUserID: "777", Rights: []string{}, AddedAt: adminNow},
	}
	w.store.guests[w.otherCampaign.ID] = []storage.CampaignGuest{{UserID: uuid.New(), DiscordUserID: "666"}}
	w.store.invitations[w.guestCampaign.ID] = []storage.Invitation{
		{ID: uuid.New(), Kind: storage.InvitationCampaign, Label: "players", Rights: []string{"planning.use"}, CreatedAt: adminNow},
		{ID: uuid.New(), Kind: storage.InvitationCampaign, MaxUses: &one, Uses: 1, CreatedAt: adminNow},
		{ID: uuid.New(), Kind: storage.InvitationCampaign, RevokedAt: &past, CreatedAt: adminNow},
	}

	resp, err := w.srv.ListCampaignAccess(w.gm, connect.NewRequest(&managementv1.ListCampaignAccessRequest{}))
	if err != nil {
		t.Fatalf("ListCampaignAccess: %v", err)
	}
	guests := resp.Msg.GetGuests()
	if len(guests) != 2 {
		t.Fatalf("guests = %v, want the Active Campaign's two (never the other campaign's)", guests)
	}
	// maps.edit is reported with the maps.read it implies.
	wantRights := []managementv1.GuestRight{managementv1.GuestRight_GUEST_RIGHT_MAPS_READ, managementv1.GuestRight_GUEST_RIGHT_MAPS_EDIT}
	if g := guests[0]; g.GetUserId() != mika.String() || g.GetDiscordUserId() != "888" || g.GetName() != "Mika" ||
		g.GetAvatar() == "" || !g.GetHasSignedIn() || g.GetAddedAt() == nil || !slices.Equal(g.GetRights(), wantRights) {
		t.Errorf("guest[0] = %+v", g)
	}
	if g := guests[1]; g.GetHasSignedIn() || g.GetName() != "" || len(g.GetRights()) != 0 {
		t.Errorf("guest[1] (added by snowflake, no rights) = %+v", g)
	}
	invs := resp.Msg.GetInvitations()
	if len(invs) != 3 || invs[0].GetKind() != managementv1.InvitationKind_INVITATION_KIND_CAMPAIGN {
		t.Fatalf("invitations = %v", invs)
	}
	wantInvRights := []managementv1.GuestRight{
		managementv1.GuestRight_GUEST_RIGHT_WIKI_READ, managementv1.GuestRight_GUEST_RIGHT_WIKI_SECRETS, managementv1.GuestRight_GUEST_RIGHT_PLANNING_USE,
	}
	if !slices.Equal(invs[0].GetRights(), wantInvRights) || !invs[0].GetLive() || invs[1].GetLive() || invs[2].GetLive() {
		t.Errorf("invitation rights/live = %v %v / %v %v", invs[0].GetRights(), invs[0].GetLive(), invs[1].GetLive(), invs[2].GetLive())
	}
	// The response carries no token anywhere — storage does not have it.
	if strings.Contains(resp.Msg.String(), "token") {
		t.Errorf("the list response mentions a token: %s", resp.Msg.String())
	}

	w.store.err = errors.New("db down")
	if _, err := w.srv.ListCampaignAccess(w.gm, connect.NewRequest(&managementv1.ListCampaignAccessRequest{})); connect.CodeOf(err) != connect.CodeInternal || strings.Contains(err.Error(), "db down") {
		t.Errorf("store failure = %v, want an opaque CodeInternal", err)
	}
}

func TestCampaignAccess_AddUpdateRemoveGuest(t *testing.T) {
	t.Parallel()
	w := newAccessWorld()

	resp, err := w.srv.AddCampaignGuest(w.gm, connect.NewRequest(&managementv1.AddCampaignGuestRequest{
		DiscordUserId: " 300000000000000888 ",
		// Duplicates, an unknown number, UNSPECIFIED and an implying right.
		Rights: []managementv1.GuestRight{
			managementv1.GuestRight_GUEST_RIGHT_PLANNING_USE, managementv1.GuestRight_GUEST_RIGHT_PLANNING_USE,
			managementv1.GuestRight_GUEST_RIGHT_UNSPECIFIED, managementv1.GuestRight(4242),
			managementv1.GuestRight_GUEST_RIGHT_MAPS_READ,
		},
	}))
	if err != nil {
		t.Fatalf("AddCampaignGuest: %v", err)
	}
	wantStored := []string{"wiki.read", "wiki.secrets", "maps.read", "planning.use"}
	if w.store.added.campaign != w.guestCampaign.ID || w.store.added.id != "300000000000000888" ||
		w.store.added.by != w.gmID || !slices.Equal(w.store.added.rights, wantStored) {
		t.Errorf("stored add = %+v; want the Active Campaign, the trimmed snowflake, the GM, normalized rights %v", w.store.added, wantStored)
	}
	if resp.Msg.GetGuest().GetDiscordUserId() != "300000000000000888" || len(resp.Msg.GetGuest().GetRights()) != 4 {
		t.Errorf("returned guest = %+v", resp.Msg.GetGuest())
	}

	// No rights at all is allowed and stored as an empty, non-nil set.
	if _, err := w.srv.AddCampaignGuest(w.gm, connect.NewRequest(&managementv1.AddCampaignGuestRequest{DiscordUserId: "300000000000000889"})); err != nil {
		t.Fatalf("adding a guest with no rights: %v", err)
	}
	if w.store.added.rights == nil || len(w.store.added.rights) != 0 {
		t.Errorf("rights for a rights-less guest = %#v, want an empty non-nil slice", w.store.added.rights)
	}

	before := len(w.store.calls)
	for _, bad := range []string{"", "   ", "mika#1234", "@mika", "12 34", "123456789012345678901", "-5", "１２３"} {
		_, err := w.srv.AddCampaignGuest(w.gm, connect.NewRequest(&managementv1.AddCampaignGuestRequest{DiscordUserId: bad}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("discord_user_id %q: err = %v, want CodeInvalidArgument", bad, err)
		}
	}
	if len(w.store.calls) != before {
		t.Errorf("a malformed snowflake reached the store: %v", w.store.calls[before:])
	}

	// The Tenant's own operator.
	if _, err := w.srv.AddCampaignGuest(w.gm, connect.NewRequest(&managementv1.AddCampaignGuestRequest{DiscordUserId: "42"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("adding the tenant's operator: err = %v, want CodeInvalidArgument", err)
	}

	// Update and remove are scoped to the Active Campaign: a guest of the
	// OTHER campaign is not found.
	mika, elsewhere := uuid.New(), uuid.New()
	w.store.guests[w.guestCampaign.ID] = []storage.CampaignGuest{{CampaignID: w.guestCampaign.ID, UserID: mika, DiscordUserID: "888", AddedAt: adminNow}}
	w.store.guests[w.otherCampaign.ID] = []storage.CampaignGuest{{CampaignID: w.otherCampaign.ID, UserID: elsewhere, DiscordUserID: "777", AddedAt: adminNow}}

	upd, err := w.srv.UpdateCampaignGuest(w.gm, connect.NewRequest(&managementv1.UpdateCampaignGuestRequest{
		UserId: mika.String(), Rights: []managementv1.GuestRight{managementv1.GuestRight_GUEST_RIGHT_WIKI_EDIT},
	}))
	if err != nil {
		t.Fatalf("UpdateCampaignGuest: %v", err)
	}
	if w.store.updated.campaign != w.guestCampaign.ID || !slices.Equal(w.store.updated.rights, []string{"wiki.read", "wiki.secrets", "wiki.edit"}) {
		t.Errorf("stored update = %+v", w.store.updated)
	}
	if len(upd.Msg.GetGuest().GetRights()) != 3 {
		t.Errorf("returned rights = %v", upd.Msg.GetGuest().GetRights())
	}
	// An empty set: the guest stays, with nothing.
	if _, err := w.srv.UpdateCampaignGuest(w.gm, connect.NewRequest(&managementv1.UpdateCampaignGuestRequest{UserId: mika.String()})); err != nil {
		t.Fatalf("clearing a guest's rights: %v", err)
	}
	if w.store.updated.rights == nil || len(w.store.updated.rights) != 0 {
		t.Errorf("cleared rights = %#v, want an empty non-nil slice", w.store.updated.rights)
	}
	if _, err := w.srv.UpdateCampaignGuest(w.gm, connect.NewRequest(&managementv1.UpdateCampaignGuestRequest{UserId: elsewhere.String()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("updating a guest of another campaign: err = %v, want CodeNotFound", err)
	}
	if _, err := w.srv.UpdateCampaignGuest(w.gm, connect.NewRequest(&managementv1.UpdateCampaignGuestRequest{UserId: "nope"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("malformed user id: err = %v, want CodeInvalidArgument", err)
	}

	if _, err := w.srv.RemoveCampaignGuest(w.gm, connect.NewRequest(&managementv1.RemoveCampaignGuestRequest{UserId: elsewhere.String()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("removing a guest of another campaign: err = %v, want CodeNotFound", err)
	}
	if _, err := w.srv.RemoveCampaignGuest(w.gm, connect.NewRequest(&managementv1.RemoveCampaignGuestRequest{UserId: mika.String()})); err != nil {
		t.Fatalf("RemoveCampaignGuest: %v", err)
	}
	if w.store.removed.campaign != w.guestCampaign.ID || w.store.removed.user != mika {
		t.Errorf("stored removal = %+v", w.store.removed)
	}
	if _, err := w.srv.RemoveCampaignGuest(w.gm, connect.NewRequest(&managementv1.RemoveCampaignGuestRequest{UserId: ""})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("empty user id: err = %v, want CodeInvalidArgument", err)
	}
}

func TestCampaignAccess_Invitations(t *testing.T) {
	t.Parallel()
	w := newAccessWorld()
	w.srv.SetAccessClockAndMintForTest(func() time.Time { return adminNow }, func() (string, error) { return "minted-campaign-token", nil })

	resp, err := w.srv.CreateCampaignInvitation(w.gm, connect.NewRequest(&managementv1.CreateCampaignInvitationRequest{
		Label: "  Friday players  ", MaxUses: 5, ExpiresInDays: 30, DiscordUserId: " 300000000000000888 ",
		Rights: []managementv1.GuestRight{managementv1.GuestRight_GUEST_RIGHT_MAPS_EDIT, managementv1.GuestRight_GUEST_RIGHT_TRANSCRIPTS_READ},
	}))
	if err != nil {
		t.Fatalf("CreateCampaignInvitation: %v", err)
	}
	n := w.store.minted
	if n.Kind != storage.InvitationCampaign || n.CampaignID == nil || *n.CampaignID != w.guestCampaign.ID {
		t.Fatalf("minted %+v, want a campaign invitation for the Active Campaign", n)
	}
	if n.Label != "Friday players" || n.MaxUses == nil || *n.MaxUses != 5 || n.DiscordUserID != "300000000000000888" ||
		n.CreatedBy != w.gmID || n.PlatformKeys || n.Token != "minted-campaign-token" {
		t.Errorf("minted = %+v", n)
	}
	if n.ExpiresAt == nil || !n.ExpiresAt.Equal(adminNow.Add(30*24*time.Hour)) {
		t.Errorf("expires_at = %v, want 30 days out", n.ExpiresAt)
	}
	if !slices.Equal(n.Rights, []string{"maps.read", "maps.edit", "transcripts.read"}) {
		t.Errorf("stored rights = %v, want them normalized", n.Rights)
	}
	if resp.Msg.GetToken() != "minted-campaign-token" || !resp.Msg.GetInvitation().GetLive() ||
		resp.Msg.GetInvitation().GetKind() != managementv1.InvitationKind_INVITATION_KIND_CAMPAIGN || len(resp.Msg.GetInvitation().GetRights()) != 3 {
		t.Errorf("response = %+v", resp.Msg)
	}

	// Defaults: unlimited, never expires, unpinned.
	if _, err := w.srv.CreateCampaignInvitation(w.gm, connect.NewRequest(&managementv1.CreateCampaignInvitationRequest{})); err != nil {
		t.Fatalf("minimal invitation: %v", err)
	}
	if d := w.store.minted; d.MaxUses != nil || d.ExpiresAt != nil || d.DiscordUserID != "" || d.Rights == nil || len(d.Rights) != 0 {
		t.Errorf("minimal invitation minted %+v", d)
	}
	// The bounds themselves are accepted.
	for _, days := range []int32{1, 365} {
		if _, err := w.srv.CreateCampaignInvitation(w.gm, connect.NewRequest(&managementv1.CreateCampaignInvitationRequest{ExpiresInDays: days})); err != nil {
			t.Errorf("expires_in_days=%d: %v", days, err)
		}
	}
	if _, err := w.srv.CreateCampaignInvitation(w.gm, connect.NewRequest(&managementv1.CreateCampaignInvitationRequest{Label: strings.Repeat("ä", 200)})); err != nil {
		t.Errorf("a 200-character label: %v", err)
	}

	before := len(w.store.calls)
	for name, req := range map[string]*managementv1.CreateCampaignInvitationRequest{
		"a 201-character label": {Label: strings.Repeat("ä", 201)},
		"negative max uses":     {MaxUses: -1},
		"negative expiry":       {ExpiresInDays: -1},
		"expiry past a year":    {ExpiresInDays: 366},
		"a non-numeric pin":     {DiscordUserId: "mika#1234"},
	} {
		if _, err := w.srv.CreateCampaignInvitation(w.gm, connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: err = %v, want CodeInvalidArgument", name, err)
		}
	}
	if len(w.store.calls) != before {
		t.Errorf("an invalid invitation reached the store: %v", w.store.calls[before:])
	}

	// Revoke: scoped to the Active Campaign.
	mine, theirs := uuid.New(), uuid.New()
	w.store.invitations[w.guestCampaign.ID] = []storage.Invitation{{ID: mine}}
	w.store.invitations[w.otherCampaign.ID] = []storage.Invitation{{ID: theirs}}
	if _, err := w.srv.RevokeCampaignInvitation(w.gm, connect.NewRequest(&managementv1.RevokeCampaignInvitationRequest{Id: mine.String()})); err != nil {
		t.Fatalf("RevokeCampaignInvitation: %v", err)
	}
	if w.store.revoked.campaign != w.guestCampaign.ID || w.store.revoked.id != mine {
		t.Errorf("stored revoke = %+v", w.store.revoked)
	}
	if _, err := w.srv.RevokeCampaignInvitation(w.gm, connect.NewRequest(&managementv1.RevokeCampaignInvitationRequest{Id: theirs.String()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("revoking another campaign's link: err = %v, want CodeNotFound", err)
	}
	if _, err := w.srv.RevokeCampaignInvitation(w.gm, connect.NewRequest(&managementv1.RevokeCampaignInvitationRequest{Id: "nope"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("malformed invitation id: err = %v, want CodeInvalidArgument", err)
	}
}

// TestCampaignAccess_NoActiveCampaign: with no campaign to manage, every
// procedure answers NotFound and nothing is written.
func TestCampaignAccess_NoActiveCampaign(t *testing.T) {
	t.Parallel()
	active := newFakeActive()
	active.campErr = storage.ErrNotFound
	store := newFakeAccessStore()
	srv := rpc.NewCampaignServerWith(rpc.CampaignStores{Active: active, Access: store})
	gm := asGM(uuid.New())

	if _, err := srv.ListCampaignAccess(gm, connect.NewRequest(&managementv1.ListCampaignAccessRequest{})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("List: err = %v, want CodeNotFound", err)
	}
	if _, err := srv.AddCampaignGuest(gm, connect.NewRequest(&managementv1.AddCampaignGuestRequest{DiscordUserId: "888"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("Add: err = %v, want CodeNotFound", err)
	}
	if _, err := srv.CreateCampaignInvitation(gm, connect.NewRequest(&managementv1.CreateCampaignInvitationRequest{})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("CreateInvitation: err = %v, want CodeNotFound", err)
	}
	if len(store.calls) != 0 {
		t.Errorf("the store was reached with no campaign: %v", store.calls)
	}
}

// TestAdminOverview_GuestStatus: someone who signs in only as a Campaign Guest
// is reported GUEST — including, on an allowlist deployment, a GM whose Access
// Grant was revoked but who is still someone's guest (the guest Campaigns are
// all they can open). In open mode operating a Tenant makes them a SIGNUP.
func TestAdminOverview_GuestStatus(t *testing.T) {
	t.Parallel()
	tenant := uuid.New()
	people := []storage.AccessPerson{
		{DiscordUserID: "300000000000000011", GuestCampaignCount: 1}, // added by snowflake, never signed in
		{DiscordUserID: "300000000000000012", Known: true, Name: "Mika", GuestCampaignCount: 3},
		{DiscordUserID: "300000000000000013", Known: true, Name: "Ex-GM", TenantID: &tenant, TenantName: "Old Table", GuestCampaignCount: 1},
		{DiscordUserID: "300000000000000014", Known: true, Name: "Ex-GM, no guest rows", TenantID: &tenant},
		{DiscordUserID: "300000000000000015", Known: true, Name: "Granted and a guest", HasGrant: true, TenantID: &tenant, GuestCampaignCount: 2},
	}
	guest, none := managementv1.AccessStatus_ACCESS_STATUS_GUEST, managementv1.AccessStatus_ACCESS_STATUS_NONE
	signup, granted := managementv1.AccessStatus_ACCESS_STATUS_SIGNUP, managementv1.AccessStatus_ACCESS_STATUS_GRANTED
	for mode, want := range map[auth.AdmissionMode]map[string]managementv1.AccessStatus{
		auth.AdmissionAllowlist: {
			"300000000000000011": guest, "300000000000000012": guest, "300000000000000013": guest,
			"300000000000000014": none, "300000000000000015": granted,
		},
		auth.AdmissionOpen: {
			"300000000000000011": guest, "300000000000000012": guest, "300000000000000013": signup,
			"300000000000000014": signup, "300000000000000015": granted,
		},
	} {
		_, by := overview(t, &fakeAdminStore{people: people}, mode)
		for id, wantStatus := range want {
			p := by[id]
			if p == nil {
				t.Fatalf("%s: %s missing from the overview", mode, id)
			}
			if p.GetStatus() != wantStatus {
				t.Errorf("%s: %s (%s) status = %v, want %v", mode, id, p.GetName(), p.GetStatus(), wantStatus)
			}
		}
		if p := by["300000000000000011"]; p.GetHasSignedIn() || p.GetGuestCampaignCount() != 1 || p.GetTenantId() != "" {
			t.Errorf("%s: never-signed-in guest = %+v", mode, p)
		}
		if p := by["300000000000000012"]; !p.GetHasSignedIn() || p.GetGuestCampaignCount() != 3 {
			t.Errorf("%s: guest = %+v", mode, p)
		}
	}
}
