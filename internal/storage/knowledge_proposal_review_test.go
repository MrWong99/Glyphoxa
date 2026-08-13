//go:build integration

package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
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
		V: kgvocab.ProposalWriteVersion, Kind: "node", NodeType: "note", Name: "Rumor", Body: "x",
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
		V: kgvocab.ProposalWriteVersion, Kind: "fact", NodeID: node.ID.String(), Subject: "Bart", Fact: "He fears the dark.",
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, id); err != nil {
		t.Fatalf("ApproveKnowledgeProposal: %v", err)
	}

	// #542: approval appends an ASPECT row; the free-form body is left untouched.
	if got := nodeBody(t, st, campaignID, node.ID); got != "An innkeeper." {
		t.Errorf("body = %q, want the untouched original — a fact lands as an aspect now", got)
	}
	aspects := nodeAspects(t, st, campaignID, node.ID)
	if len(aspects) != 1 || aspects[0].Value != "He fears the dark." {
		t.Fatalf("aspects = %+v, want one row carrying the approved fact", aspects)
	}
	if aspects[0].Key == "" || aspects[0].GMPrivate {
		t.Errorf("approved aspect = %+v, want a non-empty key and public visibility", aspects[0])
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
		V: kgvocab.ProposalWriteVersion, Kind: "fact", Subject: "  neverWINTER ", Fact: "A cold city.",
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, id); err != nil {
		t.Fatalf("Approve (subject): %v", err)
	}
	if got := nodeAspects(t, st, campaignID, loc.ID); len(got) != 1 || got[0].Value != "A cold city." {
		t.Errorf("aspects = %+v, want the approved fact as one aspect row", got)
	}

	// Unknown subject → blocked, row stays pending, KG untouched.
	unknownID := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: kgvocab.ProposalWriteVersion, Kind: "fact", Subject: "Waterdeep", Fact: "A big city.",
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
		V: kgvocab.ProposalWriteVersion, Kind: "fact", Subject: "Ring", Fact: "It is gold.",
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
		V: kgvocab.ProposalWriteVersion, Kind: "edge", NodeID: npc.ID.String(), Subject: "Bart", Relation: "resides_in", Target: "Inn",
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
		V: kgvocab.ProposalWriteVersion, Kind: "edge", NodeID: npc.ID.String(), Subject: "Bart", Relation: "resides_in", Target: "Inn",
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, dupID); !errors.As(err, &blocked) || !strings.Contains(blocked.Reason, "already exists") {
		t.Errorf("approve duplicate: got %v, want already-exists blocked", err)
	}
	if !pendingIDs(t, st, campaignID)[dupID] {
		t.Error("duplicate proposal must stay pending")
	}

	// Matrix violation: resides_in → Faction is invalid.
	badID := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: kgvocab.ProposalWriteVersion, Kind: "edge", NodeID: npc.ID.String(), Subject: "Bart", Relation: "resides_in", Target: "Guild",
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, badID); !errors.As(err, &blocked) {
		t.Errorf("approve matrix violation: got %v, want blocked", err)
	}

	// Missing target: no entry named "Nowhere".
	missID := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: kgvocab.ProposalWriteVersion, Kind: "edge", NodeID: npc.ID.String(), Subject: "Bart", Relation: "knows", Target: "Nowhere",
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, missID); !errors.As(err, &blocked) || !strings.Contains(blocked.Reason, "no wiki entry named") {
		t.Errorf("approve missing target: got %v, want no-entry blocked", err)
	}

	// Dangling node_id: a syntactically-valid uuid that no longer exists.
	danglingID := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: kgvocab.ProposalWriteVersion, Kind: "edge", NodeID: uuid.New().String(), Subject: "Ghost", Relation: "knows", Target: "Inn",
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
		V: kgvocab.ProposalWriteVersion, Kind: "node", NodeType: "faction", Name: "Zhentarim", Body: "A shadowy network.",
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

	// An UNRECOGNISED write version → unreadable, blocked, stays pending. Pinned
	// against ProposalWriteVersion rather than a literal so the bump that made
	// aspects the fact payload (#542) could not silently make this case pass.
	badID := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: kgvocab.ProposalWriteVersion + 1, Kind: "node", NodeType: "note", Name: "Future", Body: "y",
	})
	err := st.ApproveKnowledgeProposal(ctx, campaignID, badID)
	var blocked *storage.ProposalBlockedError
	if !errors.As(err, &blocked) || !strings.Contains(blocked.Reason, "unreadable") {
		t.Errorf("approve unknown write version: got %v, want unreadable blocked", err)
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
		V: kgvocab.ProposalWriteVersion, Kind: "node", NodeType: "note", Name: "Once", Body: "x",
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

	fileProposal(t, st, campaignID, butler, tool.ProposedWrite{V: kgvocab.ProposalWriteVersion, Kind: "node", NodeType: "note", Name: "n", Body: "b"})
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
		V: kgvocab.ProposalWriteVersion, Kind: "fact", Subject: "Rumor", Fact: "It grows.",
	})
	if _, err := st.DeleteNode(ctx, campaignID, node.ID); err != nil {
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

// nodeAspects reads one Node's Aspects back through the GM-facing read.
func nodeAspects(t *testing.T, st *storage.Store, campaignID, id uuid.UUID) []storage.KGNodeAspect {
	t.Helper()
	aspects, err := st.ListNodeAspects(context.Background(), campaignID, id)
	if err != nil {
		t.Fatalf("ListNodeAspects: %v", err)
	}
	return aspects
}

// TestApproveRejectsStaleWriteVersion pins the #542 version gate: a proposal
// stored under the OLD write shape is refused as unreadable rather than
// reinterpreted under the new one. That is the whole reason ProposalWriteVersion
// exists — a v1 fact was "append this prose to the body", and silently landing it
// as an aspect row would put words in the GM's wiki under a shape nobody wrote.
func TestApproveRejectsStaleWriteVersion(t *testing.T) {
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

	stale := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: kgvocab.ProposalWriteVersion - 1, Kind: "fact",
		NodeID: node.ID.String(), Subject: "Bart", Fact: "He fears the dark.",
	})
	err = st.ApproveKnowledgeProposal(ctx, campaignID, stale)
	var blocked *storage.ProposalBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("approve stale-version proposal: got %v, want ProposalBlockedError", err)
	}
	if !strings.Contains(blocked.Reason, "unreadable") {
		t.Errorf("reason = %q, want the unreadable-proposal message", blocked.Reason)
	}
	if got := nodeAspects(t, st, campaignID, node.ID); len(got) != 0 {
		t.Errorf("stale proposal wrote %d aspects; it must write nothing", len(got))
	}
	if got := nodeBody(t, st, campaignID, node.ID); got != "An innkeeper." {
		t.Errorf("body = %q, want untouched", got)
	}
	if !pendingIDs(t, st, campaignID)[stale] {
		t.Error("blocked proposal must stay pending so the GM can reject it")
	}
}

// setAspects models one editor save from scratch: the GM replaces whatever was on
// screen with this list, so every supplied row is new and every loaded row is known.
func setAspects(t *testing.T, st *storage.Store, campaignID, nodeID uuid.UUID, rows ...storage.NewKGNodeAspect) {
	t.Helper()
	if err := st.ReplaceNodeAspects(context.Background(), campaignID, nodeID,
		storage.KGNodeAspectWrite{Known: loadedAspectIDs(t, st, campaignID, nodeID), Rows: rows}); err != nil {
		t.Fatalf("ReplaceNodeAspects: %v", err)
	}
}

// loadedAspectIDs is what an open editor would have on screen.
func loadedAspectIDs(t *testing.T, st *storage.Store, campaignID, nodeID uuid.UUID) []uuid.UUID {
	t.Helper()
	loaded, err := st.ListNodeAspects(context.Background(), campaignID, nodeID)
	if err != nil {
		t.Fatalf("ListNodeAspects: %v", err)
	}
	known := make([]uuid.UUID, 0, len(loaded))
	for _, a := range loaded {
		known = append(known, a.ID)
	}
	return known
}

// TestReplaceNodeAspectsIsIdempotent pins the other half of the concurrency story.
// Bounding the delete to "rows the editor loaded" fixes the lost update, but if a
// save also ROTATED every row's id, a second save from a stale client — a second
// tab, or a save after a background refetch failed — would match nothing, delete
// nothing, and insert the whole list again. That turns a silent overwrite into
// silent duplication, which is worse. Rows are updated in place, so re-saving the
// same state is a no-op.
func TestReplaceNodeAspectsIsIdempotent(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	node := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart")
	setAspects(t, st, campaignID, node.ID,
		storage.NewKGNodeAspect{Key: "Role", Value: "Innkeeper"},
		storage.NewKGNodeAspect{Key: "Manner", Value: "Grumbles"},
	)
	first := nodeAspects(t, st, campaignID, node.ID)
	if len(first) != 2 {
		t.Fatalf("seeded %d aspects, want 2", len(first))
	}

	// One stale snapshot, replayed twice — a second tab saving the same state.
	stale := storage.KGNodeAspectWrite{
		Known: []uuid.UUID{first[0].ID, first[1].ID},
		Rows: []storage.NewKGNodeAspect{
			{ID: first[0].ID, Key: "Role", Value: "Runs the Rusty Anchor"},
			{ID: first[1].ID, Key: "Manner", Value: "Grumbles"},
		},
	}
	for i := range 2 {
		if err := st.ReplaceNodeAspects(ctx, campaignID, node.ID, stale); err != nil {
			t.Fatalf("ReplaceNodeAspects (save %d): %v", i+1, err)
		}
	}

	got := nodeAspects(t, st, campaignID, node.ID)
	if len(got) != 2 {
		t.Fatalf("aspects = %+v, want 2 — a replayed save duplicated the list", got)
	}
	if got[0].ID != first[0].ID || got[1].ID != first[1].ID {
		t.Errorf("row ids rotated across saves (%v → %v); a stale client would then duplicate everything",
			[]uuid.UUID{first[0].ID, first[1].ID}, []uuid.UUID{got[0].ID, got[1].ID})
	}
	if got[0].Value != "Runs the Rusty Anchor" {
		t.Errorf("aspect[0] = %+v, want the edit applied in place", got[0])
	}
}

// TestReplaceNodeAspectsCapCountsSurvivors pins the cap against the path the first
// fix missed: the authored list fits on its own, but a concurrently approved fact
// pushes the total over. Silently exceeding it would leave the entry unsaveable
// forever, since the editor validates the same cap.
func TestReplaceNodeAspectsCapCountsSurvivors(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	butler := seedButlerAgent(t, st, campaignID)

	node := mkNode(t, st, campaignID, storage.KGNodeNote, "Nearly full")
	rows := make([]storage.NewKGNodeAspect, 0, kgvocab.MaxAspectsPerNode)
	for i := range kgvocab.MaxAspectsPerNode - 1 {
		rows = append(rows, storage.NewKGNodeAspect{Key: "k", Value: fmt.Sprintf("v%d", i)})
	}
	setAspects(t, st, campaignID, node.ID, rows...)
	known := loadedAspectIDs(t, st, campaignID, node.ID)

	// An approval lands the last free slot while the editor is open.
	id := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: kgvocab.ProposalWriteVersion, Kind: "fact", NodeID: node.ID.String(),
		Subject: "Nearly full", AspectKey: "Rumour", Fact: "one more",
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, id); err != nil {
		t.Fatalf("ApproveKnowledgeProposal: %v", err)
	}

	// The GM adds one row: their list fits, but with the survivor it would not.
	over := append(append([]storage.NewKGNodeAspect(nil), rows...),
		storage.NewKGNodeAspect{Key: "k", Value: "mine"})
	err := st.ReplaceNodeAspects(ctx, campaignID, node.ID,
		storage.KGNodeAspectWrite{Known: known, Rows: over})
	if !errors.Is(err, storage.ErrAspectsFull) {
		t.Fatalf("save past the cap: got %v, want ErrAspectsFull", err)
	}
	if got := nodeAspects(t, st, campaignID, node.ID); len(got) != kgvocab.MaxAspectsPerNode {
		t.Errorf("aspect count = %d, want the cap %d held with nothing written",
			len(got), kgvocab.MaxAspectsPerNode)
	}
}

// TestReplaceNodeAspects covers the editor's save path: the authored order becomes
// dense positions, a second save rewrites (reorder + edit + delete in one), an
// empty list clears, and the write is campaign-scoped.
func TestReplaceNodeAspects(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	node := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart")
	setAspects(t, st, campaignID, node.ID,
		storage.NewKGNodeAspect{Key: "Role", Value: "Runs the Rusty Anchor"},
		storage.NewKGNodeAspect{Key: "Manner", Value: "Grumbles"},
		storage.NewKGNodeAspect{Key: "Secret", Value: "Took a bribe", GMPrivate: true},
	)
	got := nodeAspects(t, st, campaignID, node.ID)
	if len(got) != 3 {
		t.Fatalf("got %d aspects, want 3", len(got))
	}
	for i, a := range got {
		if a.Position != i {
			t.Errorf("aspect %d position = %d, want dense %d", i, a.Position, i)
		}
	}
	if got[0].Key != "Role" || !got[2].GMPrivate {
		t.Errorf("aspects did not round-trip in order with their privacy: %+v", got)
	}

	// One save covers reorder, edit and delete together.
	setAspects(t, st, campaignID, node.ID,
		storage.NewKGNodeAspect{Key: "Secret", Value: "Took a bribe in Eastmonth", GMPrivate: true},
		storage.NewKGNodeAspect{Key: "Role", Value: "Runs the Rusty Anchor"},
	)
	got = nodeAspects(t, st, campaignID, node.ID)
	if len(got) != 2 || got[0].Key != "Secret" || got[0].Value != "Took a bribe in Eastmonth" || got[1].Key != "Role" {
		t.Fatalf("rewrite = %+v, want the reordered/edited pair with Manner deleted", got)
	}

	// Another campaign cannot clear this Node's aspects.
	_, _, otherCampaign := seedCampaign(t, dsn)
	if err := st.ReplaceNodeAspects(ctx, otherCampaign, node.ID, storage.KGNodeAspectWrite{}); err != nil {
		t.Fatalf("cross-campaign ReplaceNodeAspects should be a no-op, got: %v", err)
	}
	if got := nodeAspects(t, st, campaignID, node.ID); len(got) != 2 {
		t.Errorf("a cross-campaign write left %d aspects; the scope must refuse it", len(got))
	}

	// Empty list clears, and the free-form body survives.
	setAspects(t, st, campaignID, node.ID)
	if got := nodeAspects(t, st, campaignID, node.ID); len(got) != 0 {
		t.Errorf("clear left %d aspects", len(got))
	}
}

// TestReplaceNodeAspectsKeepsUnseenRows is the lost-update pin: an editor save
// must not destroy an Aspect a Knowledge Proposal approval appended while the
// editor was open. The GM approves a fact in one panel and saves the entry in the
// other; before this guard the approved fact vanished with no trace, and the
// approval had already reported success.
func TestReplaceNodeAspectsKeepsUnseenRows(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	butler := seedButlerAgent(t, st, campaignID)

	node := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart")
	setAspects(t, st, campaignID, node.ID, storage.NewKGNodeAspect{Key: "Role", Value: "Innkeeper"})

	// The editor loads what is on screen NOW.
	loaded, err := st.ListNodeAspects(ctx, campaignID, node.ID)
	if err != nil {
		t.Fatalf("ListNodeAspects: %v", err)
	}
	known := []uuid.UUID{loaded[0].ID}

	// Meanwhile, an approval appends a fact the editor never saw.
	id := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: kgvocab.ProposalWriteVersion, Kind: "fact", NodeID: node.ID.String(),
		Subject: "Bart", AspectKey: "Rumour", Fact: "Fears the harbourmaster.",
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, id); err != nil {
		t.Fatalf("ApproveKnowledgeProposal: %v", err)
	}

	// The GM now saves the editor, having edited only the row they had.
	if err := st.ReplaceNodeAspects(ctx, campaignID, node.ID, storage.KGNodeAspectWrite{
		Known: known,
		Rows:  []storage.NewKGNodeAspect{{Key: "Role", Value: "Runs the Rusty Anchor"}},
	}); err != nil {
		t.Fatalf("ReplaceNodeAspects: %v", err)
	}

	got := nodeAspects(t, st, campaignID, node.ID)
	if len(got) != 2 {
		t.Fatalf("aspects = %+v, want the edited row PLUS the approved one", got)
	}
	if got[0].Key != "Role" || got[0].Value != "Runs the Rusty Anchor" {
		t.Errorf("aspect[0] = %+v, want the GM's edit at the front", got[0])
	}
	if got[1].Key != "Rumour" {
		t.Errorf("aspect[1] = %+v, want the concurrently approved fact preserved at the end", got[1])
	}
}

// TestApproveFactRefusesWhenAspectsFull pins the cap symmetry: the approve path
// refuses at MaxAspectsPerNode rather than pushing the entry past a limit the
// EDITOR also enforces — which would leave the GM unable to save that entry at all
// until they deleted facts they never chose to add.
func TestApproveFactRefusesWhenAspectsFull(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	butler := seedButlerAgent(t, st, campaignID)

	node := mkNode(t, st, campaignID, storage.KGNodeNote, "Crowded")
	full := make([]storage.NewKGNodeAspect, 0, kgvocab.MaxAspectsPerNode)
	for i := range kgvocab.MaxAspectsPerNode {
		full = append(full, storage.NewKGNodeAspect{Key: "k", Value: fmt.Sprintf("v%d", i)})
	}
	setAspects(t, st, campaignID, node.ID, full...)

	id := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: kgvocab.ProposalWriteVersion, Kind: "fact", NodeID: node.ID.String(),
		Subject: "Crowded", AspectKey: "One", Fact: "too many",
	})
	err := st.ApproveKnowledgeProposal(ctx, campaignID, id)
	var blocked *storage.ProposalBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("approve into a full node: got %v, want ProposalBlockedError", err)
	}
	if got := nodeAspects(t, st, campaignID, node.ID); len(got) != kgvocab.MaxAspectsPerNode {
		t.Errorf("aspect count = %d, want the cap %d held", len(got), kgvocab.MaxAspectsPerNode)
	}
	if !pendingIDs(t, st, campaignID)[id] {
		t.Error("a refused approval must stay pending")
	}
}

// TestAspectsAreFulltextSearchable is the regression pin for the biggest hazard in
// moving facts out of kg_node.body: kg_node.fts is a GENERATED column over
// name + body, so without the aspect_text sync every fact approved after this
// slice would be unfindable by both the GM wiki search and the Butler's kg_query.
//
// It also pins the privacy half: a prompt-facing search must not MATCH on a
// gm_private aspect, or returning the entry would leak the secret through the hit
// itself even though the payload correctly withholds the text.
func TestAspectsAreFulltextSearchable(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	view := st.PromptKG()

	bart := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart")
	setAspects(t, st, campaignID, bart.ID,
		storage.NewKGNodeAspect{Key: "Role", Value: "Runs the Rusty Anchor"},
		storage.NewKGNodeAspect{Key: "Secret", Value: "Took the smugglers bribe", GMPrivate: true},
	)

	// A public aspect is findable from BOTH sides — nothing about the entry's name
	// or body mentions the anchor.
	gm, err := st.SearchNodes(ctx, campaignID, "anchor", 10)
	if err != nil {
		t.Fatalf("SearchNodes: %v", err)
	}
	if nodeIDSet(gm)[bart.ID] != 1 {
		t.Error("GM search cannot find a public aspect's text")
	}
	pub, err := view.SearchPublicNodes(ctx, campaignID, "anchor", 10)
	if err != nil {
		t.Fatalf("SearchPublicNodes: %v", err)
	}
	if nodeIDSet(pub)[bart.ID] != 1 {
		t.Error("prompt-facing search cannot find a public aspect's text — approved facts are invisible")
	}

	// The GM can find their own secret; a prompt-facing search must not, not even
	// as a hit with the text withheld.
	gmSecret, err := st.SearchNodes(ctx, campaignID, "smugglers", 10)
	if err != nil {
		t.Fatalf("SearchNodes (secret): %v", err)
	}
	if nodeIDSet(gmSecret)[bart.ID] != 1 {
		t.Error("GM search cannot find a private aspect's text")
	}
	leaked, err := view.SearchPublicNodes(ctx, campaignID, "smugglers", 10)
	if err != nil {
		t.Fatalf("SearchPublicNodes (secret): %v", err)
	}
	if nodeIDSet(leaked)[bart.ID] != 0 {
		t.Error("prompt-facing search MATCHED a gm_private aspect — the hit itself leaks the secret")
	}

	// Deleting an aspect withdraws it from the index too.
	setAspects(t, st, campaignID, bart.ID)
	after, err := st.SearchNodes(ctx, campaignID, "anchor", 10)
	if err != nil {
		t.Fatalf("SearchNodes (after clear): %v", err)
	}
	if nodeIDSet(after)[bart.ID] != 0 {
		t.Error("a deleted aspect is still fulltext-indexed")
	}
}

// TestDeleteNodeCascadesAspects pins that a Node delete reaps its Aspects through
// the composite FK, so no orphan row survives its entry.
func TestDeleteNodeCascadesAspects(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	node := mkNode(t, st, campaignID, storage.KGNodeNote, "Doomed")
	setAspects(t, st, campaignID, node.ID, storage.NewKGNodeAspect{Key: "Note", Value: "about to vanish"})
	if _, err := st.DeleteNode(ctx, campaignID, node.ID); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	if got := nodeAspects(t, st, campaignID, node.ID); len(got) != 0 {
		t.Errorf("%d aspect rows outlived their node", len(got))
	}
}

// TestApproveEdgeCarriesTexture pins #546's approval path: an approved
// "I now distrust her" must land WITH its feeling. An approval that quietly
// discarded what was approved would be worse than refusing it.
func TestApproveEdgeCarriesTexture(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	butler := seedButlerAgent(t, st, campaignID)

	bart := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart")
	mira := mkNode(t, st, campaignID, storage.KGNodeNPC, "Mira")

	id := fileProposal(t, st, campaignID, butler, tool.ProposedWrite{
		V: kgvocab.ProposalWriteVersion, Kind: "edge",
		NodeID: bart.ID.String(), Subject: "Bart", Relation: "knows", Target: "Mira",
		Note: "she cheated him at cards", Disposition: -2,
	})
	if err := st.ApproveKnowledgeProposal(ctx, campaignID, id); err != nil {
		t.Fatalf("ApproveKnowledgeProposal: %v", err)
	}

	edges, err := st.ListEdges(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(edges))
	}
	if edges[0].Note != "she cheated him at cards" || edges[0].Disposition != -2 {
		t.Errorf("edge = %+v, want the proposal's texture carried through", edges[0])
	}
	if edges[0].FromNodeID != bart.ID || edges[0].ToNodeID != mira.ID {
		t.Errorf("edge endpoints = %v→%v, want Bart→Mira", edges[0].FromNodeID, edges[0].ToNodeID)
	}
}

// TestAgentNodeFactsCarriesOutgoingRelation pins that only the Agent's OWN
// outgoing edge contributes a feeling: an Edge is a one-way assertion, so how
// someone else feels about a Node is not this NPC's feeling.
func TestAgentNodeFactsCarriesOutgoingRelation(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	own := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart")
	agentID := linkAgent(t, st, campaignID, own.ID, "Bart")
	mira := mkNode(t, st, campaignID, storage.KGNodeNPC, "Mira")
	admirer := mkNode(t, st, campaignID, storage.KGNodeNPC, "Admirer")

	newEdge := func(from, to uuid.UUID) storage.KGEdge {
		t.Helper()
		e, err := st.CreateEdge(ctx, storage.NewKGEdge{
			CampaignID: campaignID, FromNodeID: from, ToNodeID: to, Type: storage.KGEdgeKnows,
		})
		if err != nil {
			t.Fatalf("CreateEdge: %v", err)
		}
		return e
	}
	out := newEdge(own.ID, mira.ID)
	if _, err := st.UpdateEdgeDetails(ctx, campaignID, out.ID, "she cheated him", -2); err != nil {
		t.Fatalf("UpdateEdgeDetails outgoing: %v", err)
	}
	in := newEdge(admirer.ID, own.ID)
	if _, err := st.UpdateEdgeDetails(ctx, campaignID, in.ID, "worships him", 2); err != nil {
		t.Fatalf("UpdateEdgeDetails incoming: %v", err)
	}

	facts, err := st.AgentNodeFacts(ctx, agentID)
	if err != nil {
		t.Fatalf("AgentNodeFacts: %v", err)
	}
	byID := map[uuid.UUID]storage.KGNode{}
	for _, n := range facts {
		byID[n.ID] = n
	}
	if got := byID[mira.ID]; got.RelationDisposition != -2 || got.RelationNote != "she cheated him" {
		t.Errorf("outgoing neighbour = %+v, want Bart's own feeling carried", got)
	}
	if got := byID[admirer.ID]; got.RelationDisposition != 0 || got.RelationNote != "" {
		t.Errorf("incoming neighbour = %+v, want NO feeling — that is the admirer's, not Bart's", got)
	}
}
