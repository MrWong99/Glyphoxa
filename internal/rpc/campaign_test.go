package rpc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeReader is a keyless, deterministic campaignReader for the default gate.
// forUser/forUserErr back the durable /glyphoxa use selection the profile-first
// resolution reads first (#222); campaign is the most-recent fallback.
type fakeReader struct {
	campaign   storage.Campaign
	err        error
	forUser    storage.Campaign
	forUserErr error
}

func (f fakeReader) GetActiveCampaign(context.Context) (storage.Campaign, error) {
	return f.campaign, f.err
}

func (f fakeReader) GetActiveCampaignForUser(context.Context, string) (storage.Campaign, error) {
	if f.forUserErr != nil {
		return storage.Campaign{}, f.forUserErr
	}
	if f.forUser.ID == uuid.Nil {
		return storage.Campaign{}, storage.ErrNotFound
	}
	return f.forUser, nil
}

func (f fakeReader) GetCampaign(context.Context, uuid.UUID) (storage.Campaign, error) {
	return f.campaign, f.err
}

// The campaign management half of campaignStore (#264) is unused by the
// GetActiveCampaign tests; stub it so fakeReader still satisfies the widened
// interface. The management handlers exercise it via fakeCampaignStore.
func (fakeReader) ListCampaigns(context.Context) ([]storage.Campaign, error) {
	return nil, nil
}
func (fakeReader) CreateCampaign(context.Context, storage.NewCampaign) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (fakeReader) UpdateCampaign(context.Context, storage.CampaignUpdate) (storage.Campaign, error) {
	return storage.Campaign{}, nil
}
func (fakeReader) SetActiveCampaign(context.Context, string, uuid.UUID) error {
	return nil
}

// The archive lifecycle half of campaignStore (#269) is unused by the
// GetActiveCampaign tests; stub it so fakeReader still satisfies the widened
// interface. The archive handlers exercise it via fakeCampaignStore.
func (fakeReader) ListAllCampaigns(context.Context) ([]storage.Campaign, error) {
	return nil, nil
}
func (fakeReader) ArchiveCampaign(context.Context, uuid.UUID) (storage.Campaign, error) {
	return storage.Campaign{}, nil
}
func (fakeReader) UnarchiveCampaign(context.Context, uuid.UUID) (storage.Campaign, error) {
	return storage.Campaign{}, nil
}
func (fakeReader) DeleteCampaign(context.Context, uuid.UUID) error {
	return nil
}

// The roster + CRUD half of campaignStore is unused by the GetActiveCampaign
// tests; stub it so fakeReader still satisfies the widened interface. The CRUD
// handlers have their own fake (campaign_crud_test.go).
func (fakeReader) GetButler(context.Context, uuid.UUID) (storage.Agent, error) {
	return storage.Agent{}, storage.ErrNotFound
}
func (fakeReader) CharacterAgents(context.Context, uuid.UUID) ([]storage.Agent, error) {
	return nil, nil
}
func (fakeReader) GetAgent(context.Context, uuid.UUID) (storage.Agent, error) {
	return storage.Agent{}, storage.ErrNotFound
}
func (fakeReader) CreateAgent(context.Context, storage.NewAgent) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (fakeReader) UpdateAgent(context.Context, storage.AgentUpdate) (storage.Agent, error) {
	return storage.Agent{}, storage.ErrNotFound
}
func (fakeReader) DeleteAgent(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (fakeReader) CreateNode(context.Context, storage.NewKGNode) (storage.KGNode, error) {
	return storage.KGNode{}, nil
}
func (fakeReader) ListNodes(context.Context, uuid.UUID) ([]storage.KGNode, error) {
	return nil, nil
}
func (fakeReader) UpdateNode(context.Context, storage.KGNodeUpdate) (storage.KGNode, error) {
	return storage.KGNode{}, nil
}
func (fakeReader) DeleteNode(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (fakeReader) CreateEdge(context.Context, storage.NewKGEdge) (storage.KGEdge, error) {
	return storage.KGEdge{}, nil
}
func (fakeReader) DeleteEdge(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (fakeReader) NodeEdges(context.Context, uuid.UUID, uuid.UUID) ([]storage.KGEdgeWithNodes, []storage.KGEdgeWithNodes, error) {
	return nil, nil, nil
}
func (fakeReader) SetNodeAgent(context.Context, uuid.UUID, uuid.UUID, uuid.NullUUID) (storage.KGNode, error) {
	return storage.KGNode{}, nil
}
func (fakeReader) SearchNodes(context.Context, uuid.UUID, string, int) ([]storage.KGNode, error) {
	return nil, nil
}
func (fakeReader) ListToolGrants(context.Context, uuid.UUID) ([]storage.ToolGrant, error) {
	return nil, nil
}
func (fakeReader) UpsertToolGrant(context.Context, storage.NewToolGrant) error {
	return nil
}
func (fakeReader) DeleteToolGrant(context.Context, uuid.UUID, string) error {
	return nil
}
func (fakeReader) ListCharacters(context.Context, uuid.UUID) ([]storage.Character, error) {
	return nil, nil
}
func (fakeReader) CreateCharacter(context.Context, storage.NewCharacter) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (fakeReader) UpdateCharacter(context.Context, storage.CharacterUpdate) (storage.Character, error) {
	return storage.Character{}, nil
}
func (fakeReader) DeleteCharacter(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

// newClient stands up the CampaignServer handler behind an httptest server and
// returns a Connect-JSON client for it. WithProtoJSON forces the JSON codec on
// the wire (the default is binary proto), so this also asserts the RPC is
// reachable over Connect-JSON.
func newClient(t *testing.T, reader fakeReader) managementv1connect.CampaignServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(rpc.NewCampaignServer(reader).Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, srv.URL, connect.WithProtoJSON(),
	)
}

// newClientAs is newClient plus an injected authenticated operator, so the
// profile-first resolution's durable-selection lookup sees a Discord identity
// (#222). A zero user injects nothing (the legacy no-user path).
func newClientAs(t *testing.T, reader fakeReader, user storage.User) managementv1connect.CampaignServiceClient {
	t.Helper()
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if user.DiscordUserID != "" {
				ctx = auth.WithUser(ctx, user)
			}
			return next(ctx, req)
		}
	})
	mux := http.NewServeMux()
	mux.Handle(rpc.NewCampaignServer(reader).Handler(connect.WithInterceptors(inject)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, srv.URL, connect.WithProtoJSON(),
	)
}

// TestGetActiveCampaignHonorsDurableSelection is #222: the Session-screen header
// resolves the operator's durable /glyphoxa use selection (campaign A), not the
// most-recently-created default (campaign B), so the header names the campaign
// Start would run.
func TestGetActiveCampaignHonorsDurableSelection(t *testing.T) {
	t.Parallel()
	selected := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Selected A"}
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer B"}
	client := newClientAs(t, fakeReader{forUser: selected, campaign: newer}, storage.User{DiscordUserID: "999"})

	resp, err := client.GetActiveCampaign(context.Background(),
		connect.NewRequest(&managementv1.GetActiveCampaignRequest{}))
	if err != nil {
		t.Fatalf("GetActiveCampaign: %v", err)
	}
	if got := resp.Msg.GetCampaign().GetId(); got != selected.ID.String() {
		t.Errorf("header campaign = %s, want the durable selection %s (not the newer %s)", got, selected.ID, newer.ID)
	}
}

// TestGetActiveCampaignFallsBackWithoutSelection pins the fallback half of #222:
// an operator with no /glyphoxa use selection sees the most-recently-created
// campaign in the header (GetActiveCampaign).
func TestGetActiveCampaignFallsBackWithoutSelection(t *testing.T) {
	t.Parallel()
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer B"}
	client := newClientAs(t, fakeReader{forUserErr: storage.ErrNotFound, campaign: newer}, storage.User{DiscordUserID: "999"})

	resp, err := client.GetActiveCampaign(context.Background(),
		connect.NewRequest(&managementv1.GetActiveCampaignRequest{}))
	if err != nil {
		t.Fatalf("GetActiveCampaign: %v", err)
	}
	if got := resp.Msg.GetCampaign().GetId(); got != newer.ID.String() {
		t.Errorf("header campaign = %s, want the fallback %s", got, newer.ID)
	}
}

func TestGetActiveCampaign_HappyPath(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	tenantID := uuid.New()
	created := time.Date(2026, 6, 23, 10, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 6, 23, 11, 30, 0, 0, time.UTC)

	client := newClient(t, fakeReader{campaign: storage.Campaign{
		ID:       id,
		TenantID: tenantID,
		// GMMemberID intentionally set to assert it is NOT mapped to the wire.
		GMMemberID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		Name:       "Lost Mine",
		System:     "dnd5e",
		Language:   "en",
		CreatedAt:  created,
		UpdatedAt:  updated,
	}})

	resp, err := client.GetActiveCampaign(
		context.Background(),
		connect.NewRequest(&managementv1.GetActiveCampaignRequest{}),
	)
	if err != nil {
		t.Fatalf("GetActiveCampaign: %v", err)
	}

	got := resp.Msg.GetCampaign()
	if got == nil {
		t.Fatal("response campaign is nil")
	}
	if got.GetId() != id.String() {
		t.Errorf("id = %q, want %q", got.GetId(), id.String())
	}
	if got.GetTenantId() != tenantID.String() {
		t.Errorf("tenant_id = %q, want %q", got.GetTenantId(), tenantID.String())
	}
	if got.GetName() != "Lost Mine" {
		t.Errorf("name = %q, want %q", got.GetName(), "Lost Mine")
	}
	if got.GetSystem() != "dnd5e" {
		t.Errorf("system = %q, want %q", got.GetSystem(), "dnd5e")
	}
	if got.GetLanguage() != "en" {
		t.Errorf("language = %q, want %q", got.GetLanguage(), "en")
	}
	if got.GetCreatedAt() == nil || !got.GetCreatedAt().AsTime().Equal(created) {
		t.Errorf("created_at = %v, want %v", got.GetCreatedAt().AsTime(), created)
	}
	if got.GetUpdatedAt() == nil || !got.GetUpdatedAt().AsTime().Equal(updated) {
		t.Errorf("updated_at = %v, want %v", got.GetUpdatedAt().AsTime(), updated)
	}
}

func TestGetActiveCampaign_NotFound(t *testing.T) {
	t.Parallel()

	client := newClient(t, fakeReader{err: storage.ErrNotFound})

	_, err := client.GetActiveCampaign(
		context.Background(),
		connect.NewRequest(&managementv1.GetActiveCampaignRequest{}),
	)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want %v", got, connect.CodeNotFound)
	}
}

func TestGetActiveCampaign_Internal(t *testing.T) {
	t.Parallel()

	// A non-ErrNotFound storage failure maps to CodeInternal (the raw cause is
	// logged server-side, not returned to the client).
	client := newClient(t, fakeReader{err: errors.New("boom")})

	_, err := client.GetActiveCampaign(
		context.Background(),
		connect.NewRequest(&managementv1.GetActiveCampaignRequest{}),
	)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("code = %v, want %v", got, connect.CodeInternal)
	}
}
