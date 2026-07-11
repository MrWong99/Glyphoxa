//go:build integration

package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// seedButlerAgent returns the auto-created Butler's id for a seeded campaign — a
// live agents row the proposal's authoring_agent_id FK can reference.
func seedButlerAgent(t *testing.T, st *storage.Store, campaignID uuid.UUID) uuid.UUID {
	t.Helper()
	b, err := st.GetButler(context.Background(), campaignID)
	if err != nil {
		t.Fatalf("GetButler: %v", err)
	}
	return b.ID
}

// fileProposal writes one pending proposal and returns its id (via the list read).
func fileProposal(t *testing.T, st *storage.Store, campaignID, agentID uuid.UUID, w tool.ProposedWrite) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	payload, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal proposed write: %v", err)
	}
	before, err := st.ListPendingKnowledgeProposals(ctx, campaignID)
	if err != nil {
		t.Fatalf("list before: %v", err)
	}
	if err := st.CreateKnowledgeProposal(ctx, campaignID, agentID, payload); err != nil {
		t.Fatalf("CreateKnowledgeProposal: %v", err)
	}
	after, err := st.ListPendingKnowledgeProposals(ctx, campaignID)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	// Find the id present in after but not before.
	seen := map[uuid.UUID]bool{}
	for _, p := range before {
		seen[p.ID] = true
	}
	for _, p := range after {
		if !seen[p.ID] {
			return p.ID
		}
	}
	t.Fatal("filed proposal not found in pending list")
	return uuid.Nil
}

func nodeBody(t *testing.T, st *storage.Store, campaignID, id uuid.UUID) string {
	t.Helper()
	nodes, err := st.ListNodes(context.Background(), campaignID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	for _, n := range nodes {
		if n.ID == id {
			return n.Body
		}
	}
	t.Fatalf("node %s not found", id)
	return ""
}

// pendingIDs is the set of pending-proposal ids for the campaign.
func pendingIDs(t *testing.T, st *storage.Store, campaignID uuid.UUID) map[uuid.UUID]bool {
	t.Helper()
	ps, err := st.ListPendingKnowledgeProposals(context.Background(), campaignID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	out := map[uuid.UUID]bool{}
	for _, p := range ps {
		out[p.ID] = true
	}
	return out
}

// TestRejectKnowledgeProposal: reject flips status (row dropped from pending) and
// touches no KG; a second reject / a missing id is ErrNotFound.
func TestRejectKnowledgeProposal(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	butler := seedButlerAgent(t, st, campaignID)

	id := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: 1, Kind: "node", NodeType: "note", Name: "Rumor", Body: "x",
	})

	if err := st.RejectKnowledgeProposal(ctx, campaignID, id); err != nil {
		t.Fatalf("RejectKnowledgeProposal: %v", err)
	}
	if pendingIDs(t, st, campaignID)[id] {
		t.Error("rejected proposal still pending")
	}
	// No node was created by a reject.
	nodes, _ := st.ListNodes(ctx, campaignID)
	if len(nodes) != 0 {
		t.Errorf("reject created %d nodes, want 0", len(nodes))
	}
	// A second reject is ErrNotFound (already reviewed).
	if err := st.RejectKnowledgeProposal(ctx, campaignID, id); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("double reject: got %v, want ErrNotFound", err)
	}
	// A random id is ErrNotFound.
	if err := st.RejectKnowledgeProposal(ctx, campaignID, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("reject missing: got %v, want ErrNotFound", err)
	}
}

// TestApproveFactViaNodeID: an own_node fact anchored on node_id appends to the
// Node's body (joined by a blank line onto existing prose) and resets the
// embedding so the row re-enters the backfill queue.
func TestApproveFactViaNodeID(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	butler := seedButlerAgent(t, st, campaignID)

	node, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeNPC, Name: "Bart", Body: "An innkeeper.",
	})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	// Give it an embedding so we can prove approval resets it.
	if err := st.SetNodeEmbedding(ctx, node.ID, unitVec(0), "m", node.UpdatedAt); err != nil {
		t.Fatalf("SetNodeEmbedding: %v", err)
	}

	id := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: 1, Kind: "fact", NodeID: node.ID.String(), Subject: "Bart", Fact: "He fears the dark.",
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, id); err != nil {
		t.Fatalf("ApproveKnowledgeProposal: %v", err)
	}

	want := "An innkeeper.\n\nHe fears the dark."
	if got := nodeBody(t, st, campaignID, node.ID); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if pendingIDs(t, st, campaignID)[id] {
		t.Error("approved proposal still pending")
	}
	// Embedding reset → node back in the unembedded queue.
	un, err := st.ListUnembeddedNodes(ctx, 10)
	if err != nil {
		t.Fatalf("ListUnembeddedNodes: %v", err)
	}
	found := false
	for _, n := range un {
		if n.ID == node.ID {
			found = true
		}
	}
	if !found {
		t.Error("approved fact did not reset the node's embedding (not in unembedded queue)")
	}
}

// TestApproveFactViaSubject covers name resolution: a case-insensitive/trimmed
// match lands onto a blank body (no separator); an unknown subject is blocked and
// the row stays pending (tx rollback); an ambiguous subject is blocked.
func TestApproveFactViaSubject(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	butler := seedButlerAgent(t, st, campaignID)

	loc, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeLocation, Name: "Neverwinter",
	})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	// Case-insensitive + trimmed subject resolves; blank body → fact alone.
	id := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: 1, Kind: "fact", Subject: "  neverWINTER ", Fact: "A cold city.",
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, id); err != nil {
		t.Fatalf("Approve (subject): %v", err)
	}
	if got := nodeBody(t, st, campaignID, loc.ID); got != "A cold city." {
		t.Errorf("body = %q, want %q", got, "A cold city.")
	}

	// Unknown subject → blocked, row stays pending, KG untouched.
	unknownID := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: 1, Kind: "fact", Subject: "Waterdeep", Fact: "A big city.",
	})
	err = st.ApproveKnowledgeProposal(ctx, campaignID, unknownID)
	var blocked *storage.ProposalBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("approve unknown subject: got %v, want ProposalBlockedError", err)
	}
	if !strings.Contains(blocked.Reason, "no wiki entry named") {
		t.Errorf("reason = %q, want no-entry message", blocked.Reason)
	}
	if !pendingIDs(t, st, campaignID)[unknownID] {
		t.Error("blocked proposal was consumed; must stay pending (tx rollback)")
	}

	// Ambiguous subject: two entries share a name → blocked.
	if _, err := st.CreateNode(ctx, storage.NewKGNode{CampaignID: campaignID, Type: storage.KGNodeItem, Name: "Ring"}); err != nil {
		t.Fatalf("CreateNode ring1: %v", err)
	}
	if _, err := st.CreateNode(ctx, storage.NewKGNode{CampaignID: campaignID, Type: storage.KGNodeNote, Name: "ring"}); err != nil {
		t.Fatalf("CreateNode ring2: %v", err)
	}
	ambID := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: 1, Kind: "fact", Subject: "Ring", Fact: "It is gold.",
	})
	err = st.ApproveKnowledgeProposal(ctx, campaignID, ambID)
	if !errors.As(err, &blocked) || !strings.Contains(blocked.Reason, "multiple entries named") {
		t.Errorf("approve ambiguous: got %v, want multiple-entries blocked", err)
	}
	if !pendingIDs(t, st, campaignID)[ambID] {
		t.Error("ambiguous proposal must stay pending")
	}
}

// TestApproveEdge covers the happy edge plus each structural refusal: matrix
// violation, duplicate, missing target, dangling node_id.
func TestApproveEdge(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	butler := seedButlerAgent(t, st, campaignID)

	npc, _ := st.CreateNode(ctx, storage.NewKGNode{CampaignID: campaignID, Type: storage.KGNodeNPC, Name: "Bart"})
	loc, _ := st.CreateNode(ctx, storage.NewKGNode{CampaignID: campaignID, Type: storage.KGNodeLocation, Name: "Inn"})
	fac, _ := st.CreateNode(ctx, storage.NewKGNode{CampaignID: campaignID, Type: storage.KGNodeFaction, Name: "Guild"})
	_ = fac

	// Happy: Bart resides_in Inn (resides_in → Location, valid).
	okID := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: 1, Kind: "edge", NodeID: npc.ID.String(), Subject: "Bart", Relation: "resides_in", Target: "Inn",
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, okID); err != nil {
		t.Fatalf("approve valid edge: %v", err)
	}
	out, _, err := st.NodeEdges(ctx, campaignID, npc.ID)
	if err != nil || len(out) != 1 || out[0].ToNodeID != loc.ID {
		t.Fatalf("edge not created: out=%v err=%v", out, err)
	}

	var blocked *storage.ProposalBlockedError

	// Duplicate: same (from,to,type) again → blocked.
	dupID := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: 1, Kind: "edge", NodeID: npc.ID.String(), Subject: "Bart", Relation: "resides_in", Target: "Inn",
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, dupID); !errors.As(err, &blocked) || !strings.Contains(blocked.Reason, "already exists") {
		t.Errorf("approve duplicate: got %v, want already-exists blocked", err)
	}
	if !pendingIDs(t, st, campaignID)[dupID] {
		t.Error("duplicate proposal must stay pending")
	}

	// Matrix violation: resides_in → Faction is invalid.
	badID := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: 1, Kind: "edge", NodeID: npc.ID.String(), Subject: "Bart", Relation: "resides_in", Target: "Guild",
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, badID); !errors.As(err, &blocked) {
		t.Errorf("approve matrix violation: got %v, want blocked", err)
	}

	// Missing target: no entry named "Nowhere".
	missID := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: 1, Kind: "edge", NodeID: npc.ID.String(), Subject: "Bart", Relation: "knows", Target: "Nowhere",
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, missID); !errors.As(err, &blocked) || !strings.Contains(blocked.Reason, "no wiki entry named") {
		t.Errorf("approve missing target: got %v, want no-entry blocked", err)
	}

	// Dangling node_id: a syntactically-valid uuid that no longer exists.
	danglingID := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: 1, Kind: "edge", NodeID: uuid.New().String(), Subject: "Ghost", Relation: "knows", Target: "Inn",
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, danglingID); !errors.As(err, &blocked) || !strings.Contains(blocked.Reason, "no longer exists") {
		t.Errorf("approve dangling node_id: got %v, want dangling blocked", err)
	}
}

// TestApproveNode: a new-entry proposal inserts a gm_public Node; a v≠1 payload is
// blocked and stays pending.
func TestApproveNode(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	butler := seedButlerAgent(t, st, campaignID)

	id := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: 1, Kind: "node", NodeType: "faction", Name: "Zhentarim", Body: "A shadowy network.",
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, id); err != nil {
		t.Fatalf("approve node: %v", err)
	}
	nodes, _ := st.ListNodes(ctx, campaignID)
	var created *storage.KGNode
	for i := range nodes {
		if nodes[i].Name == "Zhentarim" {
			created = &nodes[i]
		}
	}
	if created == nil {
		t.Fatal("approved node not created")
	}
	if created.Type != storage.KGNodeFaction || created.Body != "A shadowy network." || created.GMPrivate {
		t.Errorf("created node wrong: %+v", created)
	}

	// v≠1 → unreadable, blocked, stays pending.
	badID := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: 2, Kind: "node", NodeType: "note", Name: "Future", Body: "y",
	})
	err := st.ApproveKnowledgeProposal(ctx, campaignID, badID)
	var blocked *storage.ProposalBlockedError
	if !errors.As(err, &blocked) || !strings.Contains(blocked.Reason, "unreadable") {
		t.Errorf("approve v!=1: got %v, want unreadable blocked", err)
	}
	if !pendingIDs(t, st, campaignID)[badID] {
		t.Error("unreadable proposal must stay pending")
	}
}

// TestApproveDoubleIsNotFound: the second approve of the same id sees no pending
// row (already approved) and returns ErrNotFound.
func TestApproveDoubleIsNotFound(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	butler := seedButlerAgent(t, st, campaignID)

	id := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: 1, Kind: "node", NodeType: "note", Name: "Once", Body: "x",
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, id); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, id); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("double approve: got %v, want ErrNotFound", err)
	}
}

// TestListPendingCarriesAgentName: the list read joins the authoring Agent's name.
func TestListPendingCarriesAgentName(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	butler := seedButlerAgent(t, st, campaignID)

	fileProposal(t, st, campaignID, butler, tool.ProposedWrite{V: 1, Kind: "node", NodeType: "note", Name: "n", Body: "b"})
	ps, err := st.ListPendingKnowledgeProposals(ctx, campaignID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ps) != 1 {
		t.Fatalf("len = %d, want 1", len(ps))
	}
	if ps[0].AuthoringAgentName != "Glyphoxa" {
		t.Errorf("authoring name = %q, want Glyphoxa (the Butler)", ps[0].AuthoringAgentName)
	}
	// GetPendingKnowledgeProposal carries the name too; a random id is ErrNotFound.
	got, err := st.GetPendingKnowledgeProposal(ctx, campaignID, ps[0].ID)
	if err != nil || got.AuthoringAgentName != "Glyphoxa" {
		t.Errorf("GetPending: got %+v err %v", got, err)
	}
	if _, err := st.GetPendingKnowledgeProposal(ctx, campaignID, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetPending missing: got %v, want ErrNotFound", err)
	}
}

// TestNodeEmbeddingRoundTrip: a new Node lists unembedded; setting its embedding
// removes it from the queue and drops the count; UpdateNode resets it back into
// the queue.
func TestNodeEmbeddingRoundTrip(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	node, err := st.CreateNode(ctx, storage.NewKGNode{CampaignID: campaignID, Type: storage.KGNodeNote, Name: "N", Body: "b"})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if n, _ := st.CountUnembeddedNodes(ctx); n != 1 {
		t.Errorf("initial unembedded count = %d, want 1", n)
	}
	if err := st.SetNodeEmbedding(ctx, node.ID, unitVec(3), "m", node.UpdatedAt); err != nil {
		t.Fatalf("SetNodeEmbedding: %v", err)
	}
	if n, _ := st.CountUnembeddedNodes(ctx); n != 0 {
		t.Errorf("after embed count = %d, want 0", n)
	}
	// A gm_private-only toggle (name+body unchanged) must NOT reset the embedding.
	if _, err := st.UpdateNode(ctx, storage.KGNodeUpdate{ID: node.ID, CampaignID: campaignID, Name: "N", Body: "b", GMPrivate: true}); err != nil {
		t.Fatalf("UpdateNode (gm toggle): %v", err)
	}
	if n, _ := st.CountUnembeddedNodes(ctx); n != 0 {
		t.Errorf("after gm_private toggle count = %d, want 0 (embedding NOT reset — text unchanged)", n)
	}

	// A text edit (body changed) DOES reset the embedding.
	if _, err := st.UpdateNode(ctx, storage.KGNodeUpdate{ID: node.ID, CampaignID: campaignID, Name: "N", Body: "b2", GMPrivate: true}); err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}
	if n, _ := st.CountUnembeddedNodes(ctx); n != 1 {
		t.Errorf("after edit count = %d, want 1 (embedding reset)", n)
	}
}

// TestSetNodeEmbeddingStaleGuard: a SetNodeEmbedding whose updated_at no longer
// matches the row (a concurrent edit bumped it) writes 0 rows and leaves the row
// unembedded — the embedworker's next pass re-embeds the fresh text. This closes
// the mutable-node stale-embedding race (a chunk is immutable; a Node is not).
func TestSetNodeEmbeddingStaleGuard(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	node, err := st.CreateNode(ctx, storage.NewKGNode{CampaignID: campaignID, Type: storage.KGNodeNote, Name: "N", Body: "v1"})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	staleUpdatedAt := node.UpdatedAt

	// A concurrent edit lands (new text, bumped updated_at, embedding NULLed).
	edited, err := st.UpdateNode(ctx, storage.KGNodeUpdate{ID: node.ID, CampaignID: campaignID, Name: "N", Body: "v2"})
	if err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}

	// The worker (which listed v1) writes with the STALE updated_at → 0 rows, row
	// stays unembedded.
	if err := st.SetNodeEmbedding(ctx, node.ID, unitVec(0), "m", staleUpdatedAt); err != nil {
		t.Fatalf("SetNodeEmbedding (stale): %v", err)
	}
	if n, _ := st.CountUnembeddedNodes(ctx); n != 1 {
		t.Errorf("after stale write count = %d, want 1 (stale vector must NOT install)", n)
	}

	// A write with the CURRENT updated_at succeeds.
	if err := st.SetNodeEmbedding(ctx, node.ID, unitVec(0), "m", edited.UpdatedAt); err != nil {
		t.Fatalf("SetNodeEmbedding (current): %v", err)
	}
	if n, _ := st.CountUnembeddedNodes(ctx); n != 0 {
		t.Errorf("after fresh write count = %d, want 0", n)
	}
}

// TestApproveFactSubjectDeletedBeforeApprove: a fact proposal whose subject entry
// is deleted before approval is blocked (the fact never silently vanishes) and the
// row stays pending — the user-visible half of the applyProposedFact RowsAffected
// backstop.
func TestApproveFactSubjectDeletedBeforeApprove(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	butler := seedButlerAgent(t, st, campaignID)

	node, err := st.CreateNode(ctx, storage.NewKGNode{CampaignID: campaignID, Type: storage.KGNodeNote, Name: "Rumor", Body: "x"})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	id := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: 1, Kind: "fact", Subject: "Rumor", Fact: "It grows.",
	})
	if err := st.DeleteNode(ctx, campaignID, node.ID); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}

	err = st.ApproveKnowledgeProposal(ctx, campaignID, id)
	var blocked *storage.ProposalBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("approve after delete: got %v, want ProposalBlockedError", err)
	}
	if !pendingIDs(t, st, campaignID)[id] {
		t.Error("blocked proposal must stay pending (fact never silently lost)")
	}
}

// TestSimilarNodes: scoping (campaign + non-null embedding), order (nearest first),
// limit.
func TestSimilarNodes(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// Node A embedded along axis 0 (nearest to a query along axis 0); B along axis
	// 1 (farther); C left unembedded (must be excluded).
	a, _ := st.CreateNode(ctx, storage.NewKGNode{CampaignID: campaignID, Type: storage.KGNodeNote, Name: "A"})
	b, _ := st.CreateNode(ctx, storage.NewKGNode{CampaignID: campaignID, Type: storage.KGNodeNote, Name: "B"})
	st.CreateNode(ctx, storage.NewKGNode{CampaignID: campaignID, Type: storage.KGNodeNote, Name: "C"})
	if err := st.SetNodeEmbedding(ctx, a.ID, unitVec(0), "m", a.UpdatedAt); err != nil {
		t.Fatalf("embed A: %v", err)
	}
	if err := st.SetNodeEmbedding(ctx, b.ID, unitVec(1), "m", b.UpdatedAt); err != nil {
		t.Fatalf("embed B: %v", err)
	}

	// A node in another campaign must never appear (scoping).
	var other uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name) VALUES ($1, 'Other') RETURNING id`, tenantID).Scan(&other); err != nil {
		t.Fatalf("insert other campaign: %v", err)
	}
	on, _ := st.CreateNode(ctx, storage.NewKGNode{CampaignID: other, Type: storage.KGNodeNote, Name: "Other-A"})
	st.SetNodeEmbedding(ctx, on.ID, unitVec(0), "m", on.UpdatedAt)

	got, err := st.SimilarNodes(ctx, campaignID, unitVec(0), 5)
	if err != nil {
		t.Fatalf("SimilarNodes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (C unembedded + other-campaign excluded)", len(got))
	}
	if got[0].ID != a.ID || got[1].ID != b.ID {
		t.Errorf("order = [%s %s], want [A=%s B=%s] (nearest first)", got[0].Name, got[1].Name, a.ID, b.ID)
	}
	// Limit honoured.
	lim, err := st.SimilarNodes(ctx, campaignID, unitVec(0), 1)
	if err != nil || len(lim) != 1 || lim[0].ID != a.ID {
		t.Errorf("limit 1: got %v err %v, want [A]", lim, err)
	}
}

// unitVec returns a 768-dim unit vector with 1.0 at index axis, 0 elsewhere — a
// deterministic embedding whose cosine distance to another axis is maximal and to
// itself is zero.
func unitVec(axis int) []float32 {
	v := make([]float32, 768)
	v[axis] = 1
	return v
}
