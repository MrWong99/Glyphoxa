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

	// The auto-Butler comes seeded with dice (migration 00013).
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
	if g := butlerGrants.Msg.GetGrants(); len(g) != 1 || g[0].GetToolName() != "dice" || !g[0].GetGranted() {
		t.Fatalf("butler grants = %+v, want dice granted (seeded)", g)
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

	// AC4 (revoke): toggle the Butler's dice off; the row is gone, so the next
	// session hydrates ZERO declarations — the LLM is never shown the Tool.
	if _, err := client.UpdateToolGrant(ctx, connect.NewRequest(&managementv1.UpdateToolGrantRequest{
		AgentId: butler.GetId(), ToolName: "dice", Granted: false,
	})); err != nil {
		t.Fatalf("UpdateToolGrant(butler dice off): %v", err)
	}
	if got := grantedNames(listGrants(t, client, butler.GetId())); got != nil {
		t.Fatalf("after revoke, butler granted = %v, want none", got)
	}
	if got := hydratedDeclarations(t, store, butlerID); len(got) != 0 {
		t.Fatalf("revoked Butler hydrates declarations %v, want none (LLM shown no Tool)", got)
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
// using the PUBLIC tool package the live loop shares: read the Agent's Tool Grant
// rows, build a real GrantSet over the built-in Registry, and return the Tool
// names it would declare to the LLM. This is exactly what loadSeededNPCs does at
// Voice Session start, so it proves a grant mutation takes effect on the next
// session without new plumbing (AC4).
func hydratedDeclarations(t *testing.T, store *storage.Store, agentID uuid.UUID) []string {
	t.Helper()
	rows, err := store.ListToolGrants(context.Background(), agentID)
	if err != nil {
		t.Fatalf("hydrate: ListToolGrants(%s): %v", agentID, err)
	}
	grants := make([]tool.Grant, 0, len(rows))
	for _, r := range rows {
		g := tool.Grant{ToolName: r.ToolName}
		if len(r.Config) > 0 {
			g.Config = r.Config
		}
		grants = append(grants, g)
	}
	decls := tool.NewGrantSet(tool.BuiltinRegistry(), grants...).Declarations()
	names := make([]string, len(decls))
	for i, d := range decls {
		names[i] = d.Name
	}
	return names
}
