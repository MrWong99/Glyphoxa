package rpc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// mgmtClient stands up the CampaignService handler with an injected operator +
// tenant (the auth interceptor stack's resolved principal, ADR-0039) and an
// optional live Voice Session source, returning a Connect-JSON client. The
// management RPCs (#264) need both: CreateCampaign resolves the tenant, and
// SetActiveCampaign resolves the operator's DiscordUserID.
func mgmtClient(t *testing.T, store *fakeCampaignStore, user storage.User, tenantID uuid.UUID, sessions *fakeSessionManager) managementv1connect.CampaignServiceClient {
	t.Helper()
	srv := rpc.NewCampaignServer(store)
	if sessions != nil {
		srv.SetSessions(sessions)
	}
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if user.DiscordUserID != "" {
				ctx = auth.WithUser(ctx, user)
			}
			if tenantID != uuid.Nil {
				ctx = auth.WithTenant(ctx, tenantID)
			}
			return next(ctx, req)
		}
	})
	mux := http.NewServeMux()
	mux.Handle(srv.Handler(connect.WithInterceptors(inject)))
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, s.URL, connect.WithProtoJSON(),
	)
}

func TestListCampaigns_NameOrdered(t *testing.T) {
	t.Parallel()
	// The store already returns name-ordered rows (ListCampaigns SQL); the handler
	// maps them 1:1, so assert the order is preserved onto the wire.
	store := newFakeStore()
	store.campaignList = []storage.Campaign{
		{ID: uuid.New(), TenantID: uuid.New(), Name: "Alpha Quest", System: "dnd5e", Language: "en"},
		{ID: uuid.New(), TenantID: uuid.New(), Name: "Lost Mine", System: "pf2e", Language: "de"},
	}
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	resp, err := client.ListCampaigns(context.Background(),
		connect.NewRequest(&managementv1.ListCampaignsRequest{}))
	if err != nil {
		t.Fatalf("ListCampaigns: %v", err)
	}
	got := resp.Msg.GetCampaigns()
	if len(got) != 2 {
		t.Fatalf("campaigns len = %d, want 2", len(got))
	}
	if got[0].GetName() != "Alpha Quest" || got[1].GetName() != "Lost Mine" {
		t.Errorf("order not preserved: %q, %q", got[0].GetName(), got[1].GetName())
	}
	if got[1].GetSystem() != "pf2e" || got[1].GetLanguage() != "de" {
		t.Errorf("fields not mapped: %+v", got[1])
	}
}

func TestListCampaigns_Empty(t *testing.T) {
	t.Parallel()
	client := mgmtClient(t, newFakeStore(), storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	resp, err := client.ListCampaigns(context.Background(),
		connect.NewRequest(&managementv1.ListCampaignsRequest{}))
	if err != nil {
		t.Fatalf("ListCampaigns: %v", err)
	}
	if got := len(resp.Msg.GetCampaigns()); got != 0 {
		t.Errorf("campaigns len = %d, want 0", got)
	}
}

func TestListCampaigns_Internal(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.listCampaignErr = errAny
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	_, err := client.ListCampaigns(context.Background(),
		connect.NewRequest(&managementv1.ListCampaignsRequest{}))
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", got)
	}
}

func TestCreateCampaign_HappyPath(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	tenantID := uuid.New()
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, tenantID, nil)

	resp, err := client.CreateCampaign(context.Background(),
		connect.NewRequest(&managementv1.CreateCampaignRequest{
			Name: "New Campaign", System: "dnd5e", Language: "en",
		}))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	c := resp.Msg.GetCampaign()
	if c.GetName() != "New Campaign" || c.GetSystem() != "dnd5e" || c.GetLanguage() != "en" {
		t.Errorf("fields not mapped: %+v", c)
	}
	if c.GetTenantId() != tenantID.String() {
		t.Errorf("tenant_id = %q, want the server-resolved %q", c.GetTenantId(), tenantID)
	}
	// The tenant must come from the context, never the request — assert the store
	// was asked to create under the resolved tenant.
	if len(store.createdCampaigns) != 1 || store.createdCampaigns[0].TenantID != tenantID {
		t.Errorf("store create tenant = %+v, want %s", store.createdCampaigns, tenantID)
	}
	if store.createdCampaigns[0].Name != "New Campaign" {
		t.Errorf("store create name = %q", store.createdCampaigns[0].Name)
	}
}

func TestCreateCampaign_EmptyNameInvalid(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	// Whitespace-only is treated as empty (trimmed), like CreateNode.
	_, err := client.CreateCampaign(context.Background(),
		connect.NewRequest(&managementv1.CreateCampaignRequest{Name: "   "}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
	if len(store.createdCampaigns) != 0 {
		t.Errorf("store should not have been asked to create: %+v", store.createdCampaigns)
	}
}

func TestCreateCampaign_NoTenantUnauthenticated(t *testing.T) {
	t.Parallel()
	// No tenant injected → the handler treats it as unauthenticated.
	client := mgmtClient(t, newFakeStore(), storage.User{DiscordUserID: "999"}, uuid.Nil, nil)

	_, err := client.CreateCampaign(context.Background(),
		connect.NewRequest(&managementv1.CreateCampaignRequest{Name: "x"}))
	if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
		t.Errorf("code = %v, want Unauthenticated", got)
	}
}

func TestCreateCampaign_Internal(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.createCampaignErr = errAny
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	_, err := client.CreateCampaign(context.Background(),
		connect.NewRequest(&managementv1.CreateCampaignRequest{Name: "x"}))
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", got)
	}
}

func TestUpdateCampaign_HappyPath(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	id := uuid.New()
	store.campaignsByID = map[uuid.UUID]storage.Campaign{
		id: {ID: id, TenantID: uuid.New(), Name: "Old", System: "old-sys", Language: "en"},
	}
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	resp, err := client.UpdateCampaign(context.Background(),
		connect.NewRequest(&managementv1.UpdateCampaignRequest{
			Id: id.String(), Name: "Renamed", System: "dnd5e", Language: "de",
		}))
	if err != nil {
		t.Fatalf("UpdateCampaign: %v", err)
	}
	c := resp.Msg.GetCampaign()
	if c.GetName() != "Renamed" || c.GetSystem() != "dnd5e" || c.GetLanguage() != "de" {
		t.Errorf("update not applied: %+v", c)
	}
	// System/Language written opaquely — verbatim, no validation/curation.
	if len(store.updatedCampaigns) != 1 {
		t.Fatalf("store updates = %d, want 1", len(store.updatedCampaigns))
	}
	u := store.updatedCampaigns[0]
	if u.System != "dnd5e" || u.Language != "de" || u.Name != "Renamed" {
		t.Errorf("store update not opaque round-trip: %+v", u)
	}
}

// TestUpdateCampaign_OpaqueFreeText pins the #264 opacity rule: an arbitrary,
// non-vocabulary system/language string reaches storage verbatim — no validation
// rejects it (curation is the settings-editor slice's call).
func TestUpdateCampaign_OpaqueFreeText(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	id := uuid.New()
	store.campaignsByID = map[uuid.UUID]storage.Campaign{id: {ID: id, Name: "Old"}}
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	const gibberishSystem = "Homebrew: 3d6-in-order ⚔️"
	const gibberishLang = "Middle Draconic (made up)"
	resp, err := client.UpdateCampaign(context.Background(),
		connect.NewRequest(&managementv1.UpdateCampaignRequest{
			Id: id.String(), Name: "Kept", System: gibberishSystem, Language: gibberishLang,
		}))
	if err != nil {
		t.Fatalf("UpdateCampaign: %v", err)
	}
	if resp.Msg.GetCampaign().GetSystem() != gibberishSystem || resp.Msg.GetCampaign().GetLanguage() != gibberishLang {
		t.Errorf("opaque strings not preserved: %+v", resp.Msg.GetCampaign())
	}
}

func TestUpdateCampaign_InvalidID(t *testing.T) {
	t.Parallel()
	client := mgmtClient(t, newFakeStore(), storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	_, err := client.UpdateCampaign(context.Background(),
		connect.NewRequest(&managementv1.UpdateCampaignRequest{Id: "not-a-uuid", Name: "x"}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestUpdateCampaign_EmptyNameInvalid(t *testing.T) {
	t.Parallel()
	client := mgmtClient(t, newFakeStore(), storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	_, err := client.UpdateCampaign(context.Background(),
		connect.NewRequest(&managementv1.UpdateCampaignRequest{Id: uuid.New().String(), Name: "  "}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestUpdateCampaign_UnknownIDNotFound(t *testing.T) {
	t.Parallel()
	// campaignsByID is empty, so UpdateCampaign returns ErrNotFound.
	client := mgmtClient(t, newFakeStore(), storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	_, err := client.UpdateCampaign(context.Background(),
		connect.NewRequest(&managementv1.UpdateCampaignRequest{Id: uuid.New().String(), Name: "x"}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

func TestSetActiveCampaign_HappyPath(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	target := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Target"}
	store.campaignsByID = map[uuid.UUID]storage.Campaign{target.ID: target}
	// No live session and no durable selection lookup wired: the resolved read
	// falls through to the durable selection, which the fake returns from forUser.
	store.forUser = target
	client := mgmtClient(t, store, storage.User{DiscordUserID: "operator-42"}, uuid.New(), nil)

	resp, err := client.SetActiveCampaign(context.Background(),
		connect.NewRequest(&managementv1.SetActiveCampaignRequest{CampaignId: target.ID.String()}))
	if err != nil {
		t.Fatalf("SetActiveCampaign: %v", err)
	}
	if got := resp.Msg.GetCampaign().GetId(); got != target.ID.String() {
		t.Errorf("resolved campaign = %s, want %s", got, target.ID)
	}
	// The selection is keyed on the operator's DiscordUserID — the SAME row
	// /glyphoxa use writes (migration 00014).
	if len(store.setActiveCalls) != 1 {
		t.Fatalf("SetActiveCampaign calls = %d, want 1", len(store.setActiveCalls))
	}
	call := store.setActiveCalls[0]
	if call.discordUserID != "operator-42" || call.campaignID != target.ID {
		t.Errorf("durable write = %+v, want {operator-42 %s}", call, target.ID)
	}
}

func TestSetActiveCampaign_InvalidID(t *testing.T) {
	t.Parallel()
	client := mgmtClient(t, newFakeStore(), storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	_, err := client.SetActiveCampaign(context.Background(),
		connect.NewRequest(&managementv1.SetActiveCampaignRequest{CampaignId: "nope"}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestSetActiveCampaign_UnknownIDNotFound(t *testing.T) {
	t.Parallel()
	// campaignsByID is empty → the pre-write GetCampaign validation returns
	// ErrNotFound, and the selection is never persisted.
	store := newFakeStore()
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), nil)

	_, err := client.SetActiveCampaign(context.Background(),
		connect.NewRequest(&managementv1.SetActiveCampaignRequest{CampaignId: uuid.New().String()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
	if len(store.setActiveCalls) != 0 {
		t.Errorf("unknown id must not persist a selection: %+v", store.setActiveCalls)
	}
}

// TestSetActiveCampaignLiveFirstWins pins the live-first resolution rule (#222,
// #264): setting a durable selection (D) while a Voice Session is live (bound to
// L) returns the LIVE campaign, not the one just selected — the durable write
// still happens (both surfaces stay in lockstep), but the resolved Active Campaign
// honors the live session.
func TestSetActiveCampaignLiveFirstWins(t *testing.T) {
	t.Parallel()
	live := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Live L"}
	durable := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Durable D"}
	store := newFakeStore()
	store.campaignsByID = map[uuid.UUID]storage.Campaign{live.ID: live, durable.ID: durable}
	store.forUser = durable // the durable selection, were live-first not in force
	client := mgmtClient(t, store, storage.User{DiscordUserID: "999"}, uuid.New(), liveMgr(live.ID))

	resp, err := client.SetActiveCampaign(context.Background(),
		connect.NewRequest(&managementv1.SetActiveCampaignRequest{CampaignId: durable.ID.String()}))
	if err != nil {
		t.Fatalf("SetActiveCampaign: %v", err)
	}
	// The durable selection was still written (lockstep with /glyphoxa use)...
	if len(store.setActiveCalls) != 1 || store.setActiveCalls[0].campaignID != durable.ID {
		t.Errorf("durable selection not written: %+v", store.setActiveCalls)
	}
	// ...but the resolved Active Campaign is the LIVE session's, not D.
	if got := resp.Msg.GetCampaign().GetId(); got != live.ID.String() {
		t.Errorf("resolved campaign = %s, want the LIVE session campaign %s (not the just-selected %s)",
			got, live.ID, durable.ID)
	}
}
