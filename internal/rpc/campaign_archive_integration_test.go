//go:build integration

// End-to-end archive/delete lifecycle (#269) over Connect-JSON against a real
// *storage.Store (testcontainers Postgres). Proves the two structural
// consequences of the archive filter that a fake store cannot: archiving the
// resolved Active Campaign makes GetActiveCampaign fall to the next campaign (or
// NotFound when none is left), and StartSession refuses when the only campaign is
// archived — so an archived campaign can never back a Voice Session. Tag-isolated
// behind `integration`; reuses startPostgres/seedStore + the session test helpers.

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

// TestArchiveThenResolveNext: archiving the resolved Active Campaign drops it from
// resolution, so GetActiveCampaign falls to the next campaign; archiving that one
// too leaves nothing and GetActiveCampaign is NotFound. Unarchiving brings it back.
func TestArchiveThenResolveNext(t *testing.T) {
	dsn := startPostgres(t)
	store, firstID := seedStore(t, dsn) // "Lost Mine"
	ctx := context.Background()

	seeded, err := store.GetCampaign(ctx, firstID)
	if err != nil {
		t.Fatalf("GetCampaign(seeded): %v", err)
	}
	const operator = "operator-269"
	client := mgmtIntegrationClient(t, store, seeded.TenantID, operator, nil)

	// A second, newer campaign becomes the most-recent resolution.
	created, err := client.CreateCampaign(ctx, connect.NewRequest(&managementv1.CreateCampaignRequest{Name: "Newer Quest"}))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	newer := created.Msg.GetCampaign()

	// GetActiveCampaign resolves the newer one (most-recent fallback).
	active, err := client.GetActiveCampaign(ctx, connect.NewRequest(&managementv1.GetActiveCampaignRequest{}))
	if err != nil {
		t.Fatalf("GetActiveCampaign: %v", err)
	}
	if active.Msg.GetCampaign().GetId() != newer.GetId() {
		t.Fatalf("active = %s, want the newer campaign %s", active.Msg.GetCampaign().GetId(), newer.GetId())
	}

	// Archive the newer (active) one → resolution falls to the seeded campaign.
	if _, err := client.ArchiveCampaign(ctx, connect.NewRequest(&managementv1.ArchiveCampaignRequest{Id: newer.GetId()})); err != nil {
		t.Fatalf("ArchiveCampaign(newer): %v", err)
	}
	active, err = client.GetActiveCampaign(ctx, connect.NewRequest(&managementv1.GetActiveCampaignRequest{}))
	if err != nil {
		t.Fatalf("GetActiveCampaign after archive: %v", err)
	}
	if active.Msg.GetCampaign().GetId() != firstID.String() {
		t.Errorf("active after archive = %s, want the seeded campaign %s", active.Msg.GetCampaign().GetId(), firstID)
	}

	// The default list excludes the archived one; include_archived surfaces it.
	list, err := client.ListCampaigns(ctx, connect.NewRequest(&managementv1.ListCampaignsRequest{}))
	if err != nil {
		t.Fatalf("ListCampaigns: %v", err)
	}
	if len(list.Msg.GetCampaigns()) != 1 || list.Msg.GetCampaigns()[0].GetId() != firstID.String() {
		t.Errorf("default list = %+v, want only the seeded campaign", list.Msg.GetCampaigns())
	}
	all, err := client.ListCampaigns(ctx, connect.NewRequest(&managementv1.ListCampaignsRequest{IncludeArchived: true}))
	if err != nil {
		t.Fatalf("ListCampaigns(include_archived): %v", err)
	}
	if len(all.Msg.GetCampaigns()) != 2 {
		t.Errorf("include_archived list len = %d, want 2", len(all.Msg.GetCampaigns()))
	}

	// Archive the seeded one too → nothing resolves.
	if _, err := client.ArchiveCampaign(ctx, connect.NewRequest(&managementv1.ArchiveCampaignRequest{Id: firstID.String()})); err != nil {
		t.Fatalf("ArchiveCampaign(seeded): %v", err)
	}
	if _, err := client.GetActiveCampaign(ctx, connect.NewRequest(&managementv1.GetActiveCampaignRequest{})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("GetActiveCampaign with all archived = %v, want CodeNotFound", err)
	}

	// Unarchive the seeded one → it resolves again.
	if _, err := client.UnarchiveCampaign(ctx, connect.NewRequest(&managementv1.UnarchiveCampaignRequest{Id: firstID.String()})); err != nil {
		t.Fatalf("UnarchiveCampaign(seeded): %v", err)
	}
	active, err = client.GetActiveCampaign(ctx, connect.NewRequest(&managementv1.GetActiveCampaignRequest{}))
	if err != nil || active.Msg.GetCampaign().GetId() != firstID.String() {
		t.Errorf("active after unarchive = %+v, %v; want the seeded campaign %s", active.Msg.GetCampaign(), err, firstID)
	}
}

// TestStartSessionRefusedWhenOnlyCampaignArchived proves the archived-can't-start
// guard structurally (#265): with the only campaign archived, StartSession's
// server-side campaign resolution finds nothing, so it fails with the same
// CodeFailedPrecondition "no active campaign" a truly empty install returns — the
// manager is never even asked to Start.
func TestStartSessionRefusedWhenOnlyCampaignArchived(t *testing.T) {
	dsn := startPostgres(t)
	store, tenantID, campaignID := seedStoreTenant(t, dsn)
	ctx := context.Background()

	// Archive the sole campaign directly. Inject the SEEDED tenant below so the only
	// reason resolution finds nothing is the archived-exclusion (#265), not a tenant
	// mismatch — the archived campaign belongs to this very tenant (#473).
	if _, err := store.ArchiveCampaign(ctx, tenantID, campaignID); err != nil {
		t.Fatalf("ArchiveCampaign: %v", err)
	}

	mgr := &fakeSessionManager{}
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			ctx = auth.WithTenant(ctx, tenantID)
			ctx = auth.WithUser(ctx, storage.User{ID: uuid.New(), DiscordUserID: "operator-269"})
			return next(ctx, req)
		}
	})
	mux := http.NewServeMux()
	mux.Handle(rpc.NewSessionServer(mgr, store, nil, nil).Handler(connect.WithInterceptors(inject)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := managementv1connect.NewSessionServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())

	_, err := client.StartSession(ctx, connect.NewRequest(&managementv1.StartSessionRequest{}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("StartSession with only-archived campaign = %v, want CodeFailedPrecondition", err)
	}
	if mgr.startCalls != 0 {
		t.Errorf("manager Start was called %d times, want 0 (resolution refused before Start)", mgr.startCalls)
	}
}
