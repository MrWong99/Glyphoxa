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
	"reflect"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// TestUpdateAgent_CrossCampaign_Integration is #356: the load-bearing GetAgent
// pre-read (kept for voice preservation) is scoped to the active campaign. With a
// live Voice Session pinning the active campaign to A, an operator cannot update —
// or read the voice of — campaign B's NPC by id: UpdateAgent is CodeNotFound
// before any write, and B's NPC is left byte-for-byte untouched.
func TestUpdateAgent_CrossCampaign_Integration(t *testing.T) {
	dsn := startPostgres(t)
	store, campaignA := seedStore(t, dsn)
	ctx := context.Background()

	a, err := store.GetActiveCampaign(ctx) // == A, carries the tenant id
	if err != nil {
		t.Fatalf("GetActiveCampaign: %v", err)
	}
	campaignB, err := store.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: a.TenantID, Name: "Other Table", System: "dnd5e", Language: "en",
	})
	if err != nil {
		t.Fatalf("CreateCampaign B: %v", err)
	}
	// Seed a tuned voice so the load-bearing vector (voice bytes) is non-trivial.
	voiceB, err := storage.VoiceToJSON(ttseleven.DefaultVoice("secret-voice", "en"))
	if err != nil {
		t.Fatalf("VoiceToJSON: %v", err)
	}
	npcBID, err := store.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaignB, Role: storage.AgentRoleCharacter, Name: "Bandit", Persona: "Lurks.", Voice: voiceB,
	})
	if err != nil {
		t.Fatalf("CreateAgent B: %v", err)
	}
	before, err := store.GetAgent(ctx, npcBID)
	if err != nil {
		t.Fatalf("GetAgent B before: %v", err)
	}

	// Pin the active campaign to A via a live Voice Session, then mount the server.
	srv := rpc.NewCampaignServer(store)
	srv.SetSessions(liveMgr(campaignA))
	mux := http.NewServeMux()
	mux.Handle(srv.Handler())
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	client := managementv1connect.NewCampaignServiceClient(http.DefaultClient, s.URL, connect.WithProtoJSON())

	_, err = client.UpdateAgent(ctx, connect.NewRequest(&managementv1.UpdateAgentRequest{
		Id: npcBID.String(), Name: "Hijacked", Persona: "Rewritten.",
	}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("cross-campaign UpdateAgent code = %v, want NotFound", got)
	}

	// B's NPC is byte-for-byte unchanged — no cross-campaign write landed. Compare
	// the WHOLE struct (incl. Voice bytes, the load-bearing vector in this fix), not
	// just the edited fields.
	after, err := store.GetAgent(ctx, npcBID)
	if err != nil {
		t.Fatalf("GetAgent B after: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("cross-campaign UpdateAgent mutated B's NPC:\n before %+v\n after  %+v", before, after)
	}
}

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
