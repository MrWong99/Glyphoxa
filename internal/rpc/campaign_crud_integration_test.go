//go:build integration

// This drives the CampaignService CRUD + roster handlers end to end over
// Connect-JSON against a real *storage.Store (testcontainers Postgres), proving
// the wire → store → wire round-trip including the auto-Butler invariant
// (ADR-0009). Tag-isolated behind `integration`; reuses startPostgres/seedStore
// from campaign_integration_test.go.

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
	"github.com/MrWong99/Glyphoxa/internal/rpc"
)

func TestCampaignCRUD_Integration(t *testing.T) {
	dsn := startPostgres(t)
	store, _ := seedStore(t, dsn) // seedStore inserts a campaign → auto-Butler fires

	mux := http.NewServeMux()
	mux.Handle(rpc.NewCampaignServer(store).Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, srv.URL, connect.WithProtoJSON(),
	)
	ctx := context.Background()

	// Roster starts with the auto-Butler alone, Address-Only and first.
	roster, err := client.GetCampaignRoster(ctx, connect.NewRequest(&managementv1.GetCampaignRosterRequest{}))
	if err != nil {
		t.Fatalf("GetCampaignRoster (initial): %v", err)
	}
	if got := len(roster.Msg.GetRoster()); got != 1 {
		t.Fatalf("initial roster len = %d, want 1 (butler only)", got)
	}
	butler := roster.Msg.GetRoster()[0]
	if butler.GetRole() != "butler" || !butler.GetAddressOnly() {
		t.Fatalf("roster[0] is not the Address-Only Butler: %+v", butler)
	}
	if roster.Msg.GetCampaign().GetName() != "Lost Mine" || roster.Msg.GetCampaign().GetSystem() != "dnd5e" {
		t.Errorf("campaign title/system wrong: %+v", roster.Msg.GetCampaign())
	}

	// Add an NPC.
	created, err := client.CreateAgent(ctx, connect.NewRequest(&managementv1.CreateAgentRequest{
		Name: "Bart", Title: "Gruff innkeeper", Persona: "Grumbles.", Voice: "rachel",
	}))
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	bart := created.Msg.GetAgent()
	if bart.GetRole() != "character" || bart.GetTitle() != "Gruff innkeeper" || bart.GetVoice() != "rachel" {
		t.Errorf("created NPC fields wrong: %+v", bart)
	}

	// Edit every editor field, then reload the roster and assert it reloads
	// identically (the acceptance round-trip).
	if _, err := client.UpdateAgent(ctx, connect.NewRequest(&managementv1.UpdateAgentRequest{
		Id: bart.GetId(), Name: "Bartholomew", Title: "Keeper of the Inn",
		Persona: "Now grandiose.", Voice: "adam", AddressOnly: true, Aliases: []string{"Bart"},
	})); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}

	roster, err = client.GetCampaignRoster(ctx, connect.NewRequest(&managementv1.GetCampaignRosterRequest{}))
	if err != nil {
		t.Fatalf("GetCampaignRoster (after update): %v", err)
	}
	if got := len(roster.Msg.GetRoster()); got != 2 {
		t.Fatalf("roster len = %d, want 2 (butler + bart)", got)
	}
	reloaded := roster.Msg.GetRoster()[1]
	if reloaded.GetName() != "Bartholomew" || reloaded.GetTitle() != "Keeper of the Inn" {
		t.Errorf("update did not round-trip: %+v", reloaded)
	}
	if reloaded.GetVoice() != "adam" || !reloaded.GetAddressOnly() || len(reloaded.GetAliases()) != 1 {
		t.Errorf("voice/address_only/aliases did not round-trip: %+v", reloaded)
	}
	if reloaded.GetSpeakerColor() != bart.GetSpeakerColor() {
		t.Errorf("speaker_color flapped across update: %d → %d", bart.GetSpeakerColor(), reloaded.GetSpeakerColor())
	}

	// The Butler cannot be deleted (ADR-0009) — CodeFailedPrecondition.
	_, err = client.DeleteAgent(ctx, connect.NewRequest(&managementv1.DeleteAgentRequest{Id: butler.GetId()}))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("DeleteAgent(butler) code = %v, want FailedPrecondition", got)
	}

	// The NPC deletes cleanly; the roster shrinks back to the Butler alone.
	if _, err := client.DeleteAgent(ctx, connect.NewRequest(&managementv1.DeleteAgentRequest{Id: bart.GetId()})); err != nil {
		t.Fatalf("DeleteAgent(bart): %v", err)
	}
	roster, err = client.GetCampaignRoster(ctx, connect.NewRequest(&managementv1.GetCampaignRosterRequest{}))
	if err != nil {
		t.Fatalf("GetCampaignRoster (after delete): %v", err)
	}
	if got := len(roster.Msg.GetRoster()); got != 1 {
		t.Fatalf("roster len after delete = %d, want 1 (butler only)", got)
	}

	// Deleting a missing agent is CodeNotFound.
	_, err = client.DeleteAgent(ctx, connect.NewRequest(&managementv1.DeleteAgentRequest{Id: bart.GetId()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("DeleteAgent(already-gone) code = %v, want NotFound", got)
	}

	// Sanity: the Butler survived the rejected delete and is still Address-Only.
	campaignID := uuid.MustParse(butler.GetCampaignId())
	survivor, err := store.GetButler(ctx, campaignID)
	if err != nil {
		t.Fatalf("Butler missing after rejected delete: %v", err)
	}
	if !survivor.AddressOnly {
		t.Error("Butler lost Address-Only after the rejected delete")
	}
}
