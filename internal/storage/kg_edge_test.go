//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// mkNode is a tiny helper: create a Node of a type in a campaign, failing the test
// on error.
func mkNode(t *testing.T, st *storage.Store, campaignID uuid.UUID, typ storage.KGNodeType, name string) storage.KGNode {
	t.Helper()
	n, err := st.CreateNode(context.Background(), storage.NewKGNode{
		CampaignID: campaignID, Type: typ, Name: name,
	})
	if err != nil {
		t.Fatalf("CreateNode %s %q: %v", typ, name, err)
	}
	return n
}

// TestCreateEdge is #132 AC2: a typed Edge persists between two same-Campaign
// Nodes; a duplicate is ErrConflict; an invalid (type, from, to) combination is
// ErrInvalidEdge; a self-edge is rejected; and a missing OR cross-campaign
// endpoint is ErrNotFound (the "cross-campaign Edge is impossible" AC).
func TestCreateEdge(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	char := mkNode(t, st, campaignID, storage.KGNodeCharacter, "Aldric")
	loc := mkNode(t, st, campaignID, storage.KGNodeLocation, "Barrow")
	fac := mkNode(t, st, campaignID, storage.KGNodeFaction, "The Cult")

	// Happy path: Aldric resides_in Barrow.
	edge, err := st.CreateEdge(ctx, storage.NewKGEdge{
		CampaignID: campaignID, FromNodeID: char.ID, ToNodeID: loc.ID, Type: storage.KGEdgeResidesIn,
	})
	if err != nil {
		t.Fatalf("CreateEdge resides_in: %v", err)
	}
	if edge.ID == uuid.Nil || edge.CampaignID != campaignID {
		t.Errorf("edge ids not set: %+v", edge)
	}
	if edge.FromNodeID != char.ID || edge.ToNodeID != loc.ID || edge.Type != storage.KGEdgeResidesIn {
		t.Errorf("edge fields not persisted: %+v", edge)
	}
	if edge.CreatedAt.IsZero() {
		t.Errorf("created_at not defaulted: %+v", edge)
	}

	// Duplicate (from, to, type) → ErrConflict.
	if _, err := st.CreateEdge(ctx, storage.NewKGEdge{
		CampaignID: campaignID, FromNodeID: char.ID, ToNodeID: loc.ID, Type: storage.KGEdgeResidesIn,
	}); !errors.Is(err, storage.ErrConflict) {
		t.Errorf("duplicate edge err = %v, want ErrConflict", err)
	}

	// Invalid combination: resides_in must point at a Location, not a Faction.
	if _, err := st.CreateEdge(ctx, storage.NewKGEdge{
		CampaignID: campaignID, FromNodeID: char.ID, ToNodeID: fac.ID, Type: storage.KGEdgeResidesIn,
	}); !errors.Is(err, storage.ErrInvalidEdge) {
		t.Errorf("invalid-combo edge err = %v, want ErrInvalidEdge", err)
	}

	// Self-edge is rejected.
	if _, err := st.CreateEdge(ctx, storage.NewKGEdge{
		CampaignID: campaignID, FromNodeID: char.ID, ToNodeID: char.ID, Type: storage.KGEdgeKnows,
	}); !errors.Is(err, storage.ErrInvalidEdge) {
		t.Errorf("self-edge err = %v, want ErrInvalidEdge", err)
	}

	// Missing endpoint → ErrNotFound.
	if _, err := st.CreateEdge(ctx, storage.NewKGEdge{
		CampaignID: campaignID, FromNodeID: char.ID, ToNodeID: uuid.New(), Type: storage.KGEdgeKnows,
	}); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("missing-endpoint edge err = %v, want ErrNotFound", err)
	}

	// Cross-campaign endpoint → ErrNotFound (the Edge is impossible). A second
	// campaign in the same tenant owns a Node invisible to campaign-scoped lookup.
	otherCampaign, err := st.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: tenantID, Name: "Other", System: "dnd5e", Language: "en",
	})
	if err != nil {
		t.Fatalf("CreateCampaign other: %v", err)
	}
	foreign := mkNode(t, st, otherCampaign, storage.KGNodeLocation, "Foreign Keep")
	if _, err := st.CreateEdge(ctx, storage.NewKGEdge{
		CampaignID: campaignID, FromNodeID: char.ID, ToNodeID: foreign.ID, Type: storage.KGEdgeResidesIn,
	}); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("cross-campaign edge err = %v, want ErrNotFound", err)
	}
}

// TestNodeEdgesAndDelete is #132 AC3: NodeEdges splits incident Edges into
// outgoing/incoming with joined endpoint names/types; DeleteEdge removes one and
// yields ErrNotFound on a repeat; and deleting a Node cascades its incident Edges.
func TestNodeEdgesAndDelete(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	aldric := mkNode(t, st, campaignID, storage.KGNodeCharacter, "Aldric")
	barrow := mkNode(t, st, campaignID, storage.KGNodeLocation, "Barrow")
	cyra := mkNode(t, st, campaignID, storage.KGNodeCharacter, "Cyra")

	// Outgoing from Aldric, and one incoming to Aldric.
	out, err := st.CreateEdge(ctx, storage.NewKGEdge{
		CampaignID: campaignID, FromNodeID: aldric.ID, ToNodeID: barrow.ID, Type: storage.KGEdgeResidesIn,
	})
	if err != nil {
		t.Fatalf("CreateEdge outgoing: %v", err)
	}
	if _, err := st.CreateEdge(ctx, storage.NewKGEdge{
		CampaignID: campaignID, FromNodeID: cyra.ID, ToNodeID: aldric.ID, Type: storage.KGEdgeKnows,
	}); err != nil {
		t.Fatalf("CreateEdge incoming: %v", err)
	}

	outgoing, incoming, err := st.NodeEdges(ctx, aldric.ID)
	if err != nil {
		t.Fatalf("NodeEdges: %v", err)
	}
	if len(outgoing) != 1 || len(incoming) != 1 {
		t.Fatalf("NodeEdges split = %d out / %d in, want 1/1", len(outgoing), len(incoming))
	}
	if outgoing[0].ToName != "Barrow" || outgoing[0].ToType != storage.KGNodeLocation {
		t.Errorf("outgoing endpoint join wrong: %+v", outgoing[0])
	}
	if outgoing[0].Type != storage.KGEdgeResidesIn {
		t.Errorf("outgoing type = %q, want resides_in", outgoing[0].Type)
	}
	if incoming[0].FromName != "Cyra" || incoming[0].FromType != storage.KGNodeCharacter {
		t.Errorf("incoming endpoint join wrong: %+v", incoming[0])
	}

	// DeleteEdge removes the outgoing edge; a repeat is ErrNotFound.
	if err := st.DeleteEdge(ctx, campaignID, out.ID); err != nil {
		t.Fatalf("DeleteEdge: %v", err)
	}
	if err := st.DeleteEdge(ctx, campaignID, out.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("second DeleteEdge err = %v, want ErrNotFound", err)
	}
	outgoing, incoming, err = st.NodeEdges(ctx, aldric.ID)
	if err != nil {
		t.Fatalf("NodeEdges after delete: %v", err)
	}
	if len(outgoing) != 0 || len(incoming) != 1 {
		t.Errorf("after DeleteEdge = %d out / %d in, want 0/1", len(outgoing), len(incoming))
	}

	// Deleting Aldric cascades the remaining incident Edge (Cyra knows Aldric).
	if err := st.DeleteNode(ctx, campaignID, aldric.ID); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	outC, inC, err := st.NodeEdges(ctx, cyra.ID)
	if err != nil {
		t.Fatalf("NodeEdges cyra: %v", err)
	}
	if len(outC) != 0 || len(inC) != 0 {
		t.Errorf("deleting a Node did not cascade its edges: %d out / %d in", len(outC), len(inC))
	}
}

// TestSetNodeAgent is #132's NPC-Node ↔ Agent link: it links an NPC Node to a
// same-Campaign Character Agent; rejects a non-NPC Node (CHECK → ErrInvalidEdge);
// rejects a second Node claiming the same Agent (UNIQUE → ErrConflict); unlinks;
// clears the link when the Agent is deleted (ON DELETE SET NULL); and treats a
// missing or cross-campaign Agent as ErrNotFound.
func TestSetNodeAgent(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	npc := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart the Innkeeper")
	loc := mkNode(t, st, campaignID, storage.KGNodeLocation, "The Inn")

	agentID, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaignID, Role: storage.AgentRoleCharacter, Name: "Bart",
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// Link the NPC Node to the Agent.
	linked, err := st.SetNodeAgent(ctx, campaignID, npc.ID, uuid.NullUUID{UUID: agentID, Valid: true})
	if err != nil {
		t.Fatalf("SetNodeAgent link: %v", err)
	}
	if !linked.AgentID.Valid || linked.AgentID.UUID != agentID {
		t.Errorf("link not persisted: %+v", linked.AgentID)
	}

	// A non-NPC Node cannot carry the link (DB CHECK).
	if _, err := st.SetNodeAgent(ctx, campaignID, loc.ID, uuid.NullUUID{UUID: agentID, Valid: true}); !errors.Is(err, storage.ErrInvalidEdge) {
		t.Errorf("link on non-NPC node err = %v, want ErrInvalidEdge", err)
	}

	// A second NPC Node claiming the same Agent trips the UNIQUE index.
	npc2 := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart's Twin")
	if _, err := st.SetNodeAgent(ctx, campaignID, npc2.ID, uuid.NullUUID{UUID: agentID, Valid: true}); !errors.Is(err, storage.ErrConflict) {
		t.Errorf("second link to same agent err = %v, want ErrConflict", err)
	}

	// Unlink.
	unlinked, err := st.SetNodeAgent(ctx, campaignID, npc.ID, uuid.NullUUID{})
	if err != nil {
		t.Fatalf("SetNodeAgent unlink: %v", err)
	}
	if unlinked.AgentID.Valid {
		t.Errorf("unlink left agent_id set: %+v", unlinked.AgentID)
	}

	// Relink, then delete the Agent: the link is SET NULL, the Node survives.
	if _, err := st.SetNodeAgent(ctx, campaignID, npc.ID, uuid.NullUUID{UUID: agentID, Valid: true}); err != nil {
		t.Fatalf("SetNodeAgent relink: %v", err)
	}
	if err := st.DeleteAgent(ctx, campaignID, agentID); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	nodes, err := st.ListNodes(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	for _, n := range nodes {
		if n.ID == npc.ID && n.AgentID.Valid {
			t.Errorf("deleting the Agent did not SET NULL the link: %+v", n.AgentID)
		}
	}

	// A missing Agent → ErrNotFound.
	if _, err := st.SetNodeAgent(ctx, campaignID, npc.ID, uuid.NullUUID{UUID: uuid.New(), Valid: true}); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("link to missing agent err = %v, want ErrNotFound", err)
	}

	// A cross-campaign Agent → ErrNotFound (same-Campaign guard).
	otherCampaign, err := st.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: tenantID, Name: "Other", System: "dnd5e", Language: "en",
	})
	if err != nil {
		t.Fatalf("CreateCampaign other: %v", err)
	}
	foreignAgent, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: otherCampaign, Role: storage.AgentRoleCharacter, Name: "Stranger",
	})
	if err != nil {
		t.Fatalf("CreateAgent foreign: %v", err)
	}
	if _, err := st.SetNodeAgent(ctx, campaignID, npc.ID, uuid.NullUUID{UUID: foreignAgent, Valid: true}); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("link to cross-campaign agent err = %v, want ErrNotFound", err)
	}
}

// TestListEdges is #288: ListEdges returns a Campaign's Edges ordered (created_at,
// id) for the Bundle exporter; an empty Campaign yields an empty slice, not an
// error; and it is Campaign-scoped (#342) so a second Campaign's Edges never leak.
func TestListEdges(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignA := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// An empty Campaign lists no Edges (empty slice, not error).
	edges, err := st.ListEdges(ctx, campaignA)
	if err != nil {
		t.Fatalf("ListEdges empty: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("ListEdges on empty campaign = %d, want 0", len(edges))
	}

	// Seed three Edges in A. created_at defaults to now() at insert, so insertion
	// order is the (created_at, id) order.
	aldric := mkNode(t, st, campaignA, storage.KGNodeCharacter, "Aldric")
	barrow := mkNode(t, st, campaignA, storage.KGNodeLocation, "Barrow")
	cult := mkNode(t, st, campaignA, storage.KGNodeFaction, "The Cult")
	cyra := mkNode(t, st, campaignA, storage.KGNodeCharacter, "Cyra")

	e1, err := st.CreateEdge(ctx, storage.NewKGEdge{
		CampaignID: campaignA, FromNodeID: aldric.ID, ToNodeID: barrow.ID, Type: storage.KGEdgeResidesIn,
	})
	if err != nil {
		t.Fatalf("CreateEdge e1: %v", err)
	}
	e2, err := st.CreateEdge(ctx, storage.NewKGEdge{
		CampaignID: campaignA, FromNodeID: aldric.ID, ToNodeID: cult.ID, Type: storage.KGEdgeMemberOf,
	})
	if err != nil {
		t.Fatalf("CreateEdge e2: %v", err)
	}
	e3, err := st.CreateEdge(ctx, storage.NewKGEdge{
		CampaignID: campaignA, FromNodeID: cyra.ID, ToNodeID: aldric.ID, Type: storage.KGEdgeKnows,
	})
	if err != nil {
		t.Fatalf("CreateEdge e3: %v", err)
	}

	// A second Campaign (same tenant) with its own Edge must never appear in A's list.
	campaignB, err := st.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: tenantID, Name: "Other", System: "dnd5e", Language: "en",
	})
	if err != nil {
		t.Fatalf("CreateCampaign B: %v", err)
	}
	bFrom := mkNode(t, st, campaignB, storage.KGNodeCharacter, "Foreigner")
	bTo := mkNode(t, st, campaignB, storage.KGNodeLocation, "Foreign Keep")
	if _, err := st.CreateEdge(ctx, storage.NewKGEdge{
		CampaignID: campaignB, FromNodeID: bFrom.ID, ToNodeID: bTo.ID, Type: storage.KGEdgeResidesIn,
	}); err != nil {
		t.Fatalf("CreateEdge B: %v", err)
	}

	edges, err = st.ListEdges(ctx, campaignA)
	if err != nil {
		t.Fatalf("ListEdges A: %v", err)
	}
	if len(edges) != 3 {
		t.Fatalf("ListEdges A = %d edges, want 3 (no B leakage)", len(edges))
	}
	wantIDs := []uuid.UUID{e1.ID, e2.ID, e3.ID}
	for i, e := range edges {
		if e.ID != wantIDs[i] {
			t.Errorf("edge[%d].ID = %s, want %s (created_at, id order)", i, e.ID, wantIDs[i])
		}
		if e.CampaignID != campaignA {
			t.Errorf("edge[%d].CampaignID = %s, want %s (no cross-campaign leak)", i, e.CampaignID, campaignA)
		}
	}
	// Ordering is non-decreasing on created_at.
	for i := 1; i < len(edges); i++ {
		if edges[i].CreatedAt.Before(edges[i-1].CreatedAt) {
			t.Errorf("edges not ordered by created_at at %d", i)
		}
	}

	// B lists exactly its own single Edge.
	bEdges, err := st.ListEdges(ctx, campaignB)
	if err != nil {
		t.Fatalf("ListEdges B: %v", err)
	}
	if len(bEdges) != 1 {
		t.Fatalf("ListEdges B = %d, want 1", len(bEdges))
	}
}

// TestDeleteEdgeIsCampaignScoped is #342: DeleteEdge matches (id, campaign_id), so
// passing another Campaign's id refuses the delete with ErrNotFound and leaves the
// Edge; the owning Campaign then deletes it.
func TestDeleteEdgeIsCampaignScoped(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignA := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	var campaignB uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name) VALUES ($1, 'Other Table') RETURNING id`,
		tenantID).Scan(&campaignB); err != nil {
		t.Fatalf("insert campaign B: %v", err)
	}

	from := mkNode(t, st, campaignA, storage.KGNodeCharacter, "Aldric")
	to := mkNode(t, st, campaignA, storage.KGNodeLocation, "Barrow")
	edge, err := st.CreateEdge(ctx, storage.NewKGEdge{
		CampaignID: campaignA, FromNodeID: from.ID, ToNodeID: to.ID, Type: storage.KGEdgeResidesIn,
	})
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}

	// Delete scoped to campaign B must refuse and leave the Edge.
	if err := st.DeleteEdge(ctx, campaignB, edge.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cross-campaign DeleteEdge = %v, want ErrNotFound", err)
	}
	// The Edge is intact: the owning Campaign deletes it.
	if err := st.DeleteEdge(ctx, campaignA, edge.ID); err != nil {
		t.Fatalf("owner DeleteEdge after refused cross-campaign delete: %v", err)
	}
}

// TestSetNodeAgentIsCampaignScoped is #342: both the link and the unlink match the
// Node's campaign_id against the caller's campaign, so passing another Campaign's
// id refuses either with ErrNotFound and leaves the voiced-by link untouched.
func TestSetNodeAgentIsCampaignScoped(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignA := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	var campaignB uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name) VALUES ($1, 'Other Table') RETURNING id`,
		tenantID).Scan(&campaignB); err != nil {
		t.Fatalf("insert campaign B: %v", err)
	}

	npc := mkNode(t, st, campaignA, storage.KGNodeNPC, "Bart the Innkeeper")
	agentA, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaignA, Role: storage.AgentRoleCharacter, Name: "Bart",
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// Cross-campaign LINK refused: the Node is in A, the caller scopes to B.
	if _, err := st.SetNodeAgent(ctx, campaignB, npc.ID, uuid.NullUUID{UUID: agentA, Valid: true}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cross-campaign SetNodeAgent link = %v, want ErrNotFound", err)
	}

	// Legit link under the owning Campaign.
	linked, err := st.SetNodeAgent(ctx, campaignA, npc.ID, uuid.NullUUID{UUID: agentA, Valid: true})
	if err != nil {
		t.Fatalf("owner SetNodeAgent link: %v", err)
	}
	if !linked.AgentID.Valid || linked.AgentID.UUID != agentA {
		t.Fatalf("link not persisted: %+v", linked.AgentID)
	}

	// Cross-campaign UNLINK refused: the Node is in A, the caller scopes to B.
	if _, err := st.SetNodeAgent(ctx, campaignB, npc.ID, uuid.NullUUID{}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cross-campaign SetNodeAgent unlink = %v, want ErrNotFound", err)
	}

	// The link survived the refused cross-campaign unlink.
	nodes, err := st.ListNodes(ctx, campaignA)
	if err != nil {
		t.Fatalf("ListNodes A: %v", err)
	}
	var found bool
	for _, n := range nodes {
		if n.ID == npc.ID {
			found = true
			if !n.AgentID.Valid || n.AgentID.UUID != agentA {
				t.Errorf("cross-campaign unlink leaked through: %+v", n.AgentID)
			}
		}
	}
	if !found {
		t.Fatal("npc node vanished")
	}
}
