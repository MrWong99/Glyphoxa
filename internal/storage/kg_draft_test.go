//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// findNodeByAgent returns the campaign Node linked to agentID, if any.
func findNodeByAgent(t *testing.T, st *storage.Store, campaignID, agentID uuid.UUID) (storage.KGNode, bool) {
	t.Helper()
	nodes, err := st.ListNodes(context.Background(), campaignID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	for _, n := range nodes {
		if n.AgentID.Valid && n.AgentID.UUID == agentID {
			return n, true
		}
	}
	return storage.KGNode{}, false
}

// TestCreateAgentWithNPCNode is the #479 / ADR-0008-second-amendment core: a
// Character create lands the Agent AND a linked NPC Node (named after it, empty
// public body) in one transaction, and deleting the Agent later leaves the Node
// as a wiki-only entry (ON DELETE SET NULL).
func TestCreateAgentWithNPCNode(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	agentID, err := st.CreateAgentWithNPCNode(ctx, storage.NewAgent{
		CampaignID: campaignID, Role: storage.AgentRoleCharacter, Name: "Bart",
	})
	if err != nil {
		t.Fatalf("CreateAgentWithNPCNode: %v", err)
	}

	node, ok := findNodeByAgent(t, st, campaignID, agentID)
	if !ok {
		t.Fatal("no Node linked to the created Agent")
	}
	if node.Type != storage.KGNodeNPC || node.Name != "Bart" {
		t.Errorf("auto node = %+v, want an NPC Node named Bart", node)
	}
	if node.Body != "" || node.GMPrivate {
		t.Errorf("auto node must start empty and public: %+v", node)
	}

	// Deleting the Agent unlinks, never deletes, the wiki entry.
	if err := st.DeleteAgent(ctx, campaignID, agentID); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	nodes, err := st.ListNodes(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	var survived *storage.KGNode
	for i, n := range nodes {
		if n.ID == node.ID {
			survived = &nodes[i]
		}
	}
	if survived == nil {
		t.Fatal("auto node deleted with the agent, want it to survive wiki-only")
	}
	if survived.AgentID.Valid {
		t.Errorf("auto node still linked after agent delete: %+v", survived)
	}
}

// TestCreateAgentWithNPCNode_ButlerCreatesNoNode: only a Character gets the
// auto-node; the butler role does not. The campaign_auto_butler trigger
// (ADR-0009, migration 00002) already seeded a Butler with the campaign, so
// that row is removed by raw SQL first — the store's DeleteAgent refuses
// butlers by design — to free the one-butler-per-campaign slot for the create
// under test.
func TestCreateAgentWithNPCNode_ButlerCreatesNoNode(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	if _, err := pool.Exec(ctx,
		`DELETE FROM agents WHERE campaign_id = $1 AND agent_role = 'butler'`, campaignID); err != nil {
		t.Fatalf("remove auto-butler: %v", err)
	}

	agentID, err := st.CreateAgentWithNPCNode(ctx, storage.NewAgent{
		CampaignID: campaignID, Role: storage.AgentRoleButler, Name: "Glyphoxa",
	})
	if err != nil {
		t.Fatalf("CreateAgentWithNPCNode(butler): %v", err)
	}
	if _, ok := findNodeByAgent(t, st, campaignID, agentID); ok {
		t.Error("butler create must not create a Node")
	}
}

// TestRenameAgentNode: the rename follows while the Agent and Node names still
// match, resets the embedding pipeline state, and stops for good once the GM
// renames the Node independently.
func TestRenameAgentNode(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	agentID, err := st.CreateAgentWithNPCNode(ctx, storage.NewAgent{
		CampaignID: campaignID, Role: storage.AgentRoleCharacter, Name: "New NPC",
	})
	if err != nil {
		t.Fatalf("CreateAgentWithNPCNode: %v", err)
	}

	// Names match → the rename follows.
	if err := st.RenameAgentNode(ctx, campaignID, agentID, "New NPC", "Bart"); err != nil {
		t.Fatalf("RenameAgentNode: %v", err)
	}
	node, ok := findNodeByAgent(t, st, campaignID, agentID)
	if !ok || node.Name != "Bart" {
		t.Fatalf("node after follow = %+v, want name Bart", node)
	}

	// The GM diverges the Node name; a later Agent rename must NOT follow.
	if _, err := st.UpdateNode(ctx, storage.KGNodeUpdate{
		ID: node.ID, CampaignID: campaignID, Name: "Bart the Bold", Body: node.Body, GMPrivate: node.GMPrivate,
	}); err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}
	if err := st.RenameAgentNode(ctx, campaignID, agentID, "Bart", "Bartholomew"); err != nil {
		t.Fatalf("RenameAgentNode after divergence: %v", err)
	}
	node, _ = findNodeByAgent(t, st, campaignID, agentID)
	if node.Name != "Bart the Bold" {
		t.Errorf("diverged node renamed to %q, want it untouched", node.Name)
	}
}

// TestApplyKnowledgeDraft: the confirmed draft lands atomically — the happy path
// creates every Node and Edge; a draft whose edge violates the ADR-0008 matrix
// (or is a duplicate) rolls the WHOLE draft back, nodes included.
func TestApplyKnowledgeDraft(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	nodes, edges, err := st.ApplyKnowledgeDraft(ctx, campaignID,
		[]storage.NewKGNode{
			{Type: storage.KGNodeNPC, Name: "Wilhelmine", Body: "A fence."},
			{Type: storage.KGNodeFaction, Name: "The Grey Hands", GMPrivate: true},
		},
		[]storage.KnowledgeDraftEdge{{FromIndex: 0, ToIndex: 1, Type: storage.KGEdgeMemberOf}},
	)
	if err != nil {
		t.Fatalf("ApplyKnowledgeDraft: %v", err)
	}
	if len(nodes) != 2 || len(edges) != 1 {
		t.Fatalf("applied %d nodes / %d edges, want 2/1", len(nodes), len(edges))
	}
	if edges[0].FromNodeID != nodes[0].ID || edges[0].ToNodeID != nodes[1].ID || edges[0].Type != storage.KGEdgeMemberOf {
		t.Errorf("edge endpoints not the created nodes: %+v", edges[0])
	}
	listed, err := st.ListNodes(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d nodes, want the 2 applied", len(listed))
	}

	// A matrix-invalid edge (member_of must point AT a faction) rolls everything
	// back: no nodes from the failed draft may land.
	_, _, err = st.ApplyKnowledgeDraft(ctx, campaignID,
		[]storage.NewKGNode{
			{Type: storage.KGNodeNPC, Name: "Ghost"},
			{Type: storage.KGNodeNPC, Name: "Also Ghost"},
		},
		[]storage.KnowledgeDraftEdge{{FromIndex: 0, ToIndex: 1, Type: storage.KGEdgeMemberOf}},
	)
	if !errors.Is(err, storage.ErrInvalidEdge) {
		t.Fatalf("err = %v, want ErrInvalidEdge", err)
	}
	listed, err = st.ListNodes(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(listed) != 2 {
		t.Errorf("failed draft leaked nodes: %d listed, want still 2", len(listed))
	}

	// An out-of-range edge index is refused as ErrInvalidEdge too.
	if _, _, err := st.ApplyKnowledgeDraft(ctx, campaignID,
		[]storage.NewKGNode{{Type: storage.KGNodeNote, Name: "Lone"}},
		[]storage.KnowledgeDraftEdge{{FromIndex: 0, ToIndex: 5, Type: storage.KGEdgeKnows}},
	); !errors.Is(err, storage.ErrInvalidEdge) {
		t.Errorf("out-of-range index err = %v, want ErrInvalidEdge", err)
	}
}
