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
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeReader is a keyless, deterministic campaignReader for the default gate.
type fakeReader struct {
	campaign storage.Campaign
	err      error
}

func (f fakeReader) GetActiveCampaign(context.Context) (storage.Campaign, error) {
	return f.campaign, f.err
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
func (fakeReader) DeleteAgent(context.Context, uuid.UUID) error {
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
func (fakeReader) DeleteNode(context.Context, uuid.UUID) error {
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
