//go:build integration

package storage_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// linkAgent creates a Character Agent in the campaign and links it to the NPC Node,
// returning the Agent id. It fails the test on error.
func linkAgent(t *testing.T, st *storage.Store, campaignID, npcID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	agentID, err := st.CreateAgent(context.Background(), storage.NewAgent{
		CampaignID: campaignID, Role: storage.AgentRoleCharacter, Name: name,
	})
	if err != nil {
		t.Fatalf("CreateAgent %q: %v", name, err)
	}
	if _, err := st.SetNodeAgent(context.Background(), campaignID, npcID,
		uuid.NullUUID{UUID: agentID, Valid: true}); err != nil {
		t.Fatalf("SetNodeAgent %q: %v", name, err)
	}
	return agentID
}

// mkEdge creates a typed Edge between two same-Campaign Nodes, failing on error.
func mkEdge(t *testing.T, st *storage.Store, campaignID, from, to uuid.UUID, typ storage.KGEdgeType) {
	t.Helper()
	if _, err := st.CreateEdge(context.Background(), storage.NewKGEdge{
		CampaignID: campaignID, FromNodeID: from, ToNodeID: to, Type: typ,
	}); err != nil {
		t.Fatalf("CreateEdge %s: %v", typ, err)
	}
}

// nodeIDSet projects a Node slice to id→count for membership assertions.
func nodeIDSet(nodes []storage.KGNode) map[uuid.UUID]int {
	m := map[uuid.UUID]int{}
	for _, n := range nodes {
		m[n.ID]++
	}
	return m
}

// TestAgentNodeFacts is #133 AC1: an NPC's Hot Context is its own linked Node's
// facts plus its edge-adjacent Nodes' facts (BOTH edge directions), and nothing
// further. Coverage: own public node surfaces first (hop 0); an outgoing and an
// incoming neighbour both surface; a gm_private neighbour is excluded even though
// its Edge exists (ADR-0008 amendment); a two-hop node is absent (strictly single
// hop); a multi-edge neighbour is deduped to one row (min hop).
func TestAgentNodeFacts(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	own := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart the Innkeeper")
	agentID := linkAgent(t, st, campaignID, own.ID, "Bart")

	// Outgoing neighbour: Bart resides_in The Inn (public Location).
	inn := mkNode(t, st, campaignID, storage.KGNodeLocation, "The Inn")
	mkEdge(t, st, campaignID, own.ID, inn.ID, storage.KGEdgeResidesIn)
	// A SECOND edge to the same neighbour must dedupe to one surfaced row.
	mkEdge(t, st, campaignID, own.ID, inn.ID, storage.KGEdgeKnows)

	// Incoming neighbour: Cyra knows Bart (edge points AT Bart).
	cyra := mkNode(t, st, campaignID, storage.KGNodeCharacter, "Cyra")
	mkEdge(t, st, campaignID, cyra.ID, own.ID, storage.KGEdgeKnows)

	// gm_private neighbour: Bart resides_in a hidden Location — the Edge exists but
	// the Node must NOT surface (ADR-0008 amendment: gm_private filters surfacing).
	secret, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeLocation, Name: "The Hidden Cellar",
		Body: "Where the smugglers meet.", GMPrivate: true,
	})
	if err != nil {
		t.Fatalf("CreateNode secret: %v", err)
	}
	mkEdge(t, st, campaignID, own.ID, secret.ID, storage.KGEdgeResidesIn)

	// Two-hop node: The Inn member_of a Faction. The Faction is a neighbour-of-a-
	// neighbour and must be absent (strictly single hop).
	guild := mkNode(t, st, campaignID, storage.KGNodeFaction, "The Innkeepers' Guild")
	mkEdge(t, st, campaignID, inn.ID, guild.ID, storage.KGEdgeMemberOf)

	facts, err := st.AgentNodeFacts(ctx, agentID)
	if err != nil {
		t.Fatalf("AgentNodeFacts: %v", err)
	}

	got := nodeIDSet(facts)
	// Own node + both-direction public neighbours surface.
	if got[own.ID] != 1 {
		t.Errorf("own node not surfaced exactly once: %v", got[own.ID])
	}
	if got[inn.ID] != 1 {
		t.Errorf("outgoing neighbour The Inn not surfaced exactly once (multi-edge dedup): %v", got[inn.ID])
	}
	if got[cyra.ID] != 1 {
		t.Errorf("incoming neighbour Cyra not surfaced: %v", got[cyra.ID])
	}
	// Excluded: gm_private neighbour and the two-hop Faction.
	if got[secret.ID] != 0 {
		t.Errorf("gm_private neighbour leaked into the fact set: %+v", facts)
	}
	if got[guild.ID] != 0 {
		t.Errorf("two-hop Faction surfaced — traversal is not strictly single hop: %+v", facts)
	}
	if len(facts) != 3 {
		t.Fatalf("fact set = %d nodes, want 3 (own + inn + cyra): %+v", len(facts), facts)
	}
	// Own node ranks first (hop 0), neighbours after (hop 1).
	if facts[0].ID != own.ID {
		t.Errorf("own node did not rank first: got %q", facts[0].Name)
	}
}

// TestAgentNodeFacts_GMPrivateOwnNode pins that traversal STARTS from the linked
// Node regardless of its gm_private: a gm_private OWN Node is excluded from
// surfacing, but its public neighbours still surface (the Edge is walked, only the
// Node's own visibility is filtered).
func TestAgentNodeFacts_GMPrivateOwnNode(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// The NPC's own Node is gm_private.
	own, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeNPC, Name: "The Masked Broker",
		Body: "A GM-only identity.", GMPrivate: true,
	})
	if err != nil {
		t.Fatalf("CreateNode own: %v", err)
	}
	agentID := linkAgent(t, st, campaignID, own.ID, "Broker")

	// A public neighbour of the hidden own Node.
	market := mkNode(t, st, campaignID, storage.KGNodeLocation, "The Night Market")
	mkEdge(t, st, campaignID, own.ID, market.ID, storage.KGEdgeResidesIn)

	facts, err := st.AgentNodeFacts(ctx, agentID)
	if err != nil {
		t.Fatalf("AgentNodeFacts: %v", err)
	}
	got := nodeIDSet(facts)
	if got[own.ID] != 0 {
		t.Errorf("gm_private own node surfaced: %+v", facts)
	}
	if got[market.ID] != 1 {
		t.Errorf("public neighbour of the hidden own node did not surface: %+v", facts)
	}
	if len(facts) != 1 {
		t.Fatalf("fact set = %d nodes, want 1 (the public neighbour only): %+v", len(facts), facts)
	}
}

// TestAgentNodeFacts_Unlinked pins that an Agent with no linked Node gets an empty
// fact set — a Character NPC that was never linked injects no wiki facts.
func TestAgentNodeFacts_Unlinked(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// A public Node exists in the campaign, but no Node links this Agent.
	mkNode(t, st, campaignID, storage.KGNodeLocation, "Unrelated Keep")
	agentID, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaignID, Role: storage.AgentRoleCharacter, Name: "Nobody",
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	facts, err := st.AgentNodeFacts(ctx, agentID)
	if err != nil {
		t.Fatalf("AgentNodeFacts: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("unlinked agent got facts: %+v", facts)
	}
}
