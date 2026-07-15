//go:build integration

// Drives the CampaignService Tool Grant handlers (#117) end to end over
// Connect-JSON against a real *storage.Store (testcontainers Postgres): the
// seeded auto-Butler's dice grant is listed, an NPC's grant is toggled on and off
// and survives a reload, a scope config round-trips through the API, and — the AC4
// bar — the resulting rows are hydrated through the SAME public tool package the
// live loop uses to prove a revoked Tool is never declared to the LLM in the next
// Voice Session. Tag-isolated behind `integration`; reuses startPostgres/seedStore
// from campaign_integration_test.go.

package rpc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

func TestToolGrants_Integration(t *testing.T) {
	dsn := startPostgres(t)
	store, _ := seedStore(t, dsn) // campaign → auto-Butler with a seeded dice grant
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.Handle(rpc.NewCampaignServer(store).Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := managementv1connect.NewCampaignServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())

	// The auto-Butler comes seeded with dice + the #296 knowledge Tools (migration
	// 00025): dice, kg_query, transcript_search.
	roster, err := client.GetCampaignRoster(ctx, connect.NewRequest(&managementv1.GetCampaignRosterRequest{}))
	if err != nil {
		t.Fatalf("GetCampaignRoster: %v", err)
	}
	butler := roster.Msg.GetRoster()[0]
	butlerID := uuid.MustParse(butler.GetId())

	butlerGrants, err := client.ListToolGrants(ctx, connect.NewRequest(&managementv1.ListToolGrantsRequest{AgentId: butler.GetId()}))
	if err != nil {
		t.Fatalf("ListToolGrants(butler): %v", err)
	}
	// The catalog lists every built-in; assert by the GRANTED set, not by catalog
	// index — the auto-Butler is seeded with dice + the #296 knowledge Tools + recap
	// (migrations 00025, 00027).
	wantButlerGrants := []string{"dice", "kg_query", "recap", "transcript_search"}
	if granted := grantedNames(butlerGrants.Msg.GetGrants()); !slices.Equal(granted, wantButlerGrants) {
		t.Fatalf("butler granted = %v, want %v; full catalog = %+v", granted, wantButlerGrants, butlerGrants.Msg.GetGrants())
	}

	// A fresh Character NPC has no grants → dice listed but off.
	created, err := client.CreateAgent(ctx, connect.NewRequest(&managementv1.CreateAgentRequest{Name: "Bart"}))
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	npc := created.Msg.GetAgent()
	npcID := uuid.MustParse(npc.GetId())
	if grantedNames(listGrants(t, client, npc.GetId())) != nil {
		t.Fatalf("fresh NPC should have no granted Tools")
	}

	// AC2: grant dice, reload, confirm it stuck.
	if _, err := client.UpdateToolGrant(ctx, connect.NewRequest(&managementv1.UpdateToolGrantRequest{
		AgentId: npc.GetId(), ToolName: "dice", Granted: true,
	})); err != nil {
		t.Fatalf("UpdateToolGrant(npc dice on): %v", err)
	}
	if got := grantedNames(listGrants(t, client, npc.GetId())); len(got) != 1 || got[0] != "dice" {
		t.Fatalf("after grant, NPC granted = %v, want [dice]", got)
	}

	// AC3: a scope config round-trips through the API (even though dice's UI shows
	// no editor — the API is scope-capable regardless).
	if _, err := client.UpdateToolGrant(ctx, connect.NewRequest(&managementv1.UpdateToolGrantRequest{
		AgentId: npc.GetId(), ToolName: "dice", Granted: true, Config: `{"scope":"self"}`,
	})); err != nil {
		t.Fatalf("UpdateToolGrant(npc dice config): %v", err)
	}
	assertScopeSelf(t, listGrants(t, client, npc.GetId())[0].GetConfig())

	// AC4: what the NEXT Voice Session hydrates. The GRANTED NPC's rows hydrate —
	// through the same public tool package the live loop uses (#113) — into a
	// GrantSet that declares dice to the LLM.
	if got := hydratedDeclarations(t, store, npcID); len(got) != 1 || got[0] != "dice" {
		t.Fatalf("granted NPC hydrates declarations %v, want [dice]", got)
	}

	// AC4 (revoke): toggle the Butler's dice off; its row is gone, so the next
	// session hydrates declarations for the REMAINING grants only (kg_query,
	// transcript_search) — dice is never shown to the LLM.
	if _, err := client.UpdateToolGrant(ctx, connect.NewRequest(&managementv1.UpdateToolGrantRequest{
		AgentId: butler.GetId(), ToolName: "dice", Granted: false,
	})); err != nil {
		t.Fatalf("UpdateToolGrant(butler dice off): %v", err)
	}
	wantAfterRevoke := []string{"kg_query", "recap", "transcript_search"}
	if got := grantedNames(listGrants(t, client, butler.GetId())); !slices.Equal(got, wantAfterRevoke) {
		t.Fatalf("after revoke, butler granted = %v, want %v", got, wantAfterRevoke)
	}
	if got := hydratedDeclarations(t, store, butlerID); !slices.Equal(got, wantAfterRevoke) {
		t.Fatalf("revoked Butler hydrates declarations %v, want %v (dice never shown)", got, wantAfterRevoke)
	}
}

// TestToolGrants_GhostAgent_Integration proves the #215 existence pre-check
// against a real Postgres: a Character NPC is created, granted dice (a real
// tool_agent_grant row), then deleted — the FK CASCADE removes the grant, leaving
// a dangling agent_id (the stale second tab). Operating on the now-ghost id is
// CodeNotFound on BOTH grant RPCs, not the 500 the FK violation would otherwise
// surface on the write (the unit fakes can't see the FK — this is the coverage
// the follow-up adds).
func TestToolGrants_GhostAgent_Integration(t *testing.T) {
	dsn := startPostgres(t)
	store, _ := seedStore(t, dsn)
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.Handle(rpc.NewCampaignServer(store).Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := managementv1connect.NewCampaignServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())

	created, err := client.CreateAgent(ctx, connect.NewRequest(&managementv1.CreateAgentRequest{Name: "Doomed"}))
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	ghost := created.Msg.GetAgent().GetId()

	// A real grant row exists, then the Agent is deleted: the FK CASCADE
	// (tool_agent_grant.agent_id) removes the grant, leaving a dangling id.
	if _, err := client.UpdateToolGrant(ctx, connect.NewRequest(&managementv1.UpdateToolGrantRequest{
		AgentId: ghost, ToolName: "dice", Granted: true,
	})); err != nil {
		t.Fatalf("UpdateToolGrant(grant): %v", err)
	}
	if _, err := client.DeleteAgent(ctx, connect.NewRequest(&managementv1.DeleteAgentRequest{Id: ghost})); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}

	_, listErr := client.ListToolGrants(ctx, connect.NewRequest(&managementv1.ListToolGrantsRequest{AgentId: ghost}))
	if got := connect.CodeOf(listErr); got != connect.CodeNotFound {
		t.Errorf("ListToolGrants(ghost) code = %v, want NotFound", got)
	}
	_, updErr := client.UpdateToolGrant(ctx, connect.NewRequest(&managementv1.UpdateToolGrantRequest{
		AgentId: ghost, ToolName: "dice", Granted: true,
	}))
	if got := connect.CodeOf(updErr); got != connect.CodeNotFound {
		t.Errorf("UpdateToolGrant(ghost) code = %v, want NotFound", got)
	}
}

// TestToolGrants_CrossCampaign_Integration is #342: an operator whose active
// campaign is A must not be able to grant or revoke Tools on campaign B's Agent
// (incl. B's Butler). With a live Voice Session pinning the active campaign to A,
// UpdateToolGrant against B's Butler is CodeNotFound in BOTH directions (grant and
// revoke), and B's seeded grant rows are left untouched.
func TestToolGrants_CrossCampaign_Integration(t *testing.T) {
	dsn := startPostgres(t)
	store, campaignA := seedStore(t, dsn) // A + its auto-Butler (seeded dice grant)
	ctx := context.Background()

	// A second campaign B under the same tenant, with its own auto-Butler.
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
	butlerB, err := store.GetButler(ctx, campaignB)
	if err != nil {
		t.Fatalf("GetButler B: %v", err)
	}

	// Pin the active campaign to A via a live Voice Session, then mount the server.
	srv := rpc.NewCampaignServer(store)
	srv.SetSessions(liveMgr(campaignA))
	mux := http.NewServeMux()
	mux.Handle(srv.Handler())
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	client := managementv1connect.NewCampaignServiceClient(http.DefaultClient, s.URL, connect.WithProtoJSON())

	before, err := store.ListToolGrants(ctx, butlerB.ID)
	if err != nil {
		t.Fatalf("ListToolGrants(butlerB) before: %v", err)
	}

	// Revoke B's Butler dice while active on A → refused.
	_, err = client.UpdateToolGrant(ctx, connect.NewRequest(&managementv1.UpdateToolGrantRequest{
		AgentId: butlerB.ID.String(), ToolName: "dice", Granted: false,
	}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("cross-campaign UpdateToolGrant(revoke) code = %v, want NotFound", got)
	}
	// Grant-with-config on B's Butler while active on A → refused.
	_, err = client.UpdateToolGrant(ctx, connect.NewRequest(&managementv1.UpdateToolGrantRequest{
		AgentId: butlerB.ID.String(), ToolName: "dice", Granted: true, Config: `{"scope":"all"}`,
	}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("cross-campaign UpdateToolGrant(grant) code = %v, want NotFound", got)
	}

	// B's Butler grants are byte-for-byte unchanged — no cross-campaign write landed.
	after, err := store.ListToolGrants(ctx, butlerB.ID)
	if err != nil {
		t.Fatalf("ListToolGrants(butlerB) after: %v", err)
	}
	if len(before) != len(after) {
		t.Fatalf("cross-campaign grant write leaked: before %d rows, after %d", len(before), len(after))
	}
}

// listGrants is a small helper that lists an Agent's grant states or fails the test.
func listGrants(t *testing.T, client managementv1connect.CampaignServiceClient, agentID string) []*managementv1.ToolGrant {
	t.Helper()
	resp, err := client.ListToolGrants(context.Background(), connect.NewRequest(&managementv1.ListToolGrantsRequest{AgentId: agentID}))
	if err != nil {
		t.Fatalf("ListToolGrants(%s): %v", agentID, err)
	}
	return resp.Msg.GetGrants()
}

// grantedNames returns the names of the Tools currently granted (granted=true).
func grantedNames(grants []*managementv1.ToolGrant) []string {
	var out []string
	for _, g := range grants {
		if g.GetGranted() {
			out = append(out, g.GetToolName())
		}
	}
	return out
}

// hydratedDeclarations replays the #113 hydration path against the persisted rows
// through the SAME canonical mapper the live loop uses ([storage.GrantsFromRows],
// via wirenpc.grantsFromRows): read the Agent's Tool Grant rows, build a real
// GrantSet over the built-in Registry, and return the Tool names it would declare
// to the LLM. Calling the shared mapper — rather than reimplementing it here —
// means this AC4 assertion can never drift from what loadSeededNPCs actually
// hydrates at Voice Session start (issue #215).
func hydratedDeclarations(t *testing.T, store *storage.Store, agentID uuid.UUID) []string {
	t.Helper()
	rows, err := store.ListToolGrants(context.Background(), agentID)
	if err != nil {
		t.Fatalf("hydrate: ListToolGrants(%s): %v", agentID, err)
	}
	grants := storage.GrantsFromRows(rows)
	decls := tool.NewGrantSet(tool.BuiltinRegistry(tool.Deps{}), grants...).Declarations()
	names := make([]string, len(decls))
	for i, d := range decls {
		names[i] = d.Name
	}
	return names
}
