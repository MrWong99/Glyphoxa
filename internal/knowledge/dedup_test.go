package knowledge_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/knowledge"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

func pendingRow(cid uuid.UUID, w tool.ProposedWrite) storage.KnowledgeProposal {
	b, _ := json.Marshal(w)
	return storage.KnowledgeProposal{CampaignID: cid, ProposedWrite: b, Status: "pending"}
}

// ExistingKnowledge gathers, for an own_node proposal's target, the target Node's
// established body facts (split into lines) and the salient text of the pending
// proposals addressing the SAME target — and excludes pending proposals about a
// different target.
func TestExistingKnowledge_OwnNodeGathersPendingAndEstablished(t *testing.T) {
	cid := uuid.New()
	aid := uuid.New()
	ownNodeID := uuid.New()
	store := &fakeStore{
		allNodes: []storage.KGNode{
			{ID: ownNodeID, Name: "Gesa", Body: "Gesa liebt Kuchen\nGesa wohnt im Wald"},
		},
		pending: []storage.KnowledgeProposal{
			pendingRow(cid, tool.ProposedWrite{V: kgvocab.ProposalWriteVersion, Kind: "fact", NodeID: ownNodeID.String(), Subject: "Gesa", Fact: "ist die Schwester von Arturus"}),
			pendingRow(cid, tool.ProposedWrite{V: kgvocab.ProposalWriteVersion, Kind: "fact", NodeID: uuid.New().String(), Subject: "Arturus", Fact: "ist ein Ritter"}), // different target
		},
	}
	adapter := knowledge.New(store, store)

	w := tool.ProposedWrite{V: kgvocab.ProposalWriteVersion, Kind: "fact", NodeID: ownNodeID.String(), Subject: "Gesa", Fact: "something new"}
	known, err := adapter.ExistingKnowledge(liveCtx(cid), aid.String(), w)
	if err != nil {
		t.Fatalf("ExistingKnowledge: %v", err)
	}
	if store.gotPendCID != cid {
		t.Errorf("pending scoped to %v, want active campaign %v", store.gotPendCID, cid)
	}
	if len(known.Established) != 2 {
		t.Errorf("established = %q, want the 2 body lines", known.Established)
	}
	if len(known.Pending) != 1 || known.Pending[0] != "ist die Schwester von Arturus" {
		t.Errorf("pending = %q, want only the same-target proposal's salient", known.Pending)
	}
}

// For a campaign proposal (no anchor node), the established facts come from the
// subject Node found by normalized name, and pending is filtered by subject.
func TestExistingKnowledge_CampaignBySubjectName(t *testing.T) {
	cid := uuid.New()
	store := &fakeStore{
		allNodes: []storage.KGNode{
			{ID: uuid.New(), Name: "The Duke", Body: "rules the city"},
			{ID: uuid.New(), Name: "Someone Else", Body: "irrelevant"},
		},
		pending: []storage.KnowledgeProposal{
			pendingRow(cid, tool.ProposedWrite{V: kgvocab.ProposalWriteVersion, Kind: "fact", Subject: "the duke", Fact: "is old"}),
		},
	}
	adapter := knowledge.New(store, store)

	w := tool.ProposedWrite{V: kgvocab.ProposalWriteVersion, Kind: "fact", Subject: "The Duke", Fact: "new fact"}
	known, err := adapter.ExistingKnowledge(liveCtx(cid), uuid.New().String(), w)
	if err != nil {
		t.Fatalf("ExistingKnowledge: %v", err)
	}
	if len(known.Established) != 1 || known.Established[0] != "rules the city" {
		t.Errorf("established = %q, want the Duke's body", known.Established)
	}
	if len(known.Pending) != 1 || known.Pending[0] != "is old" {
		t.Errorf("pending = %q, want the same-subject proposal", known.Pending)
	}
}

// Cross-path unification (#411): an own_node pending row (keyed by node id) must
// suppress a Butler campaign re-proposal of the same fact (keyed by subject name),
// because the subject name resolves to the same Node. Without unification the two
// keys diverge and the duplicate slips through invisibly.
func TestExistingKnowledge_UnifiesOwnNodeAndCampaignKeys(t *testing.T) {
	cid := uuid.New()
	gesaID := uuid.New()
	store := &fakeStore{
		allNodes: []storage.KGNode{{ID: gesaID, Name: "Gesa"}},
		pending: []storage.KnowledgeProposal{
			// An own_node proposal the NPC already made (keyed by node id).
			pendingRow(cid, tool.ProposedWrite{V: kgvocab.ProposalWriteVersion, Kind: "fact", NodeID: gesaID.String(), Subject: "Gesa", Fact: "ist die Schwester von Arturus"}),
		},
	}
	adapter := knowledge.New(store, store)

	// The Butler now re-proposes the same fact campaign-scoped (no node id, subject
	// by name) — it must see the NPC's pending row via the unified key.
	w := tool.ProposedWrite{V: kgvocab.ProposalWriteVersion, Kind: "fact", Subject: "Gesa", Fact: "ist die Schwester von Arturus"}
	known, err := adapter.ExistingKnowledge(liveCtx(cid), uuid.New().String(), w)
	if err != nil {
		t.Fatalf("ExistingKnowledge: %v", err)
	}
	if len(known.Pending) != 1 || known.Pending[0] != "ist die Schwester von Arturus" {
		t.Errorf("campaign proposal did not see the own_node pending row: %q", known.Pending)
	}
}

// Established facts on a gm_private Node are NEVER surfaced — echoing a body line
// would leak a GM secret into a prompt (ADR-0008).
func TestExistingKnowledge_SkipsGMPrivateEstablished(t *testing.T) {
	cid := uuid.New()
	secretID := uuid.New()
	store := &fakeStore{
		allNodes: []storage.KGNode{{ID: secretID, Name: "The Traitor", Body: "is secretly the spy", GMPrivate: true}},
	}
	adapter := knowledge.New(store, store)
	w := tool.ProposedWrite{V: kgvocab.ProposalWriteVersion, Kind: "fact", Subject: "The Traitor", Fact: "is secretly the spy"}
	known, err := adapter.ExistingKnowledge(liveCtx(cid), uuid.New().String(), w)
	if err != nil {
		t.Fatalf("ExistingKnowledge: %v", err)
	}
	if len(known.Established) != 0 {
		t.Errorf("gm_private body leaked into established facts: %q", known.Established)
	}
}

// No active session is a clean error the handler can fail open on.
func TestExistingKnowledge_NoSessionErrors(t *testing.T) {
	adapter := knowledge.New(&fakeStore{}, &fakeStore{})
	if _, err := adapter.ExistingKnowledge(context.Background(), uuid.New().String(), tool.ProposedWrite{V: kgvocab.ProposalWriteVersion, Kind: "fact", Subject: "X", Fact: "y"}); err == nil {
		t.Error("want error with no active session")
	}
}

// TestExistingKnowledge_IgnoresStaleWriteVersion pins the #542 version gate on the
// dedup read: a pending proposal stamped with an unrecognised write version is
// UNREADABLE — the approve and review paths both refuse it — so it must not be
// compared against either. Otherwise a doomed row the GM can only reject would
// silently suppress a fresh, approvable proposal of the same fact.
func TestExistingKnowledge_IgnoresStaleWriteVersion(t *testing.T) {
	cid := uuid.New()
	ownNodeID := uuid.New()
	store := &fakeStore{
		allNodes: []storage.KGNode{{ID: ownNodeID, CampaignID: cid, Name: "Gesa"}},
		pending: []storage.KnowledgeProposal{
			pendingRow(cid, tool.ProposedWrite{
				V: kgvocab.ProposalWriteVersion - 1, Kind: "fact",
				NodeID: ownNodeID.String(), Subject: "Gesa", Fact: "ist die Schwester von Arturus",
			}),
		},
	}
	adapter := knowledge.New(store, store)

	known, err := adapter.ExistingKnowledge(liveCtx(cid), uuid.New().String(),
		tool.ProposedWrite{V: kgvocab.ProposalWriteVersion, Kind: "fact", NodeID: ownNodeID.String(), Subject: "Gesa", Fact: "x"})
	if err != nil {
		t.Fatalf("ExistingKnowledge: %v", err)
	}
	if len(known.Pending) != 0 {
		t.Errorf("pending = %q, want the unreadable row ignored", known.Pending)
	}
}

// TestExistingKnowledge_EchoesOnlyCallerPending pins the echo boundary: every
// same-target pending proposal feeds the silent dedup (Pending), but only the
// rows the CALLING Agent authored are marked for the echo (OwnPending). A
// proposal the Butler filed from the GM-only planning chat is a secret until
// the GM approves it and must never be quoted into the NPC's prompt (ADR-0062).
func TestExistingKnowledge_EchoesOnlyCallerPending(t *testing.T) {
	cid := uuid.New()
	caller, butler := uuid.New(), uuid.New()
	node := uuid.New()
	own := pendingRow(cid, tool.ProposedWrite{V: kgvocab.ProposalWriteVersion, Kind: "fact", NodeID: node.String(), Subject: "Bart", Fact: "the players bought me a drink"})
	own.AuthoringAgentID = caller
	secret := pendingRow(cid, tool.ProposedWrite{V: kgvocab.ProposalWriteVersion, Kind: "fact", NodeID: node.String(), Subject: "Bart", Fact: "is the hidden leader of the Red Hand cult"})
	secret.AuthoringAgentID = butler
	store := &fakeStore{
		allNodes: []storage.KGNode{{ID: node, Name: "Bart"}},
		pending:  []storage.KnowledgeProposal{own, secret},
	}
	adapter := knowledge.New(store, store)

	w := tool.ProposedWrite{V: kgvocab.ProposalWriteVersion, Kind: "fact", NodeID: node.String(), Subject: "Bart", Fact: "something new"}
	known, err := adapter.ExistingKnowledge(liveCtx(cid), caller.String(), w)
	if err != nil {
		t.Fatalf("ExistingKnowledge: %v", err)
	}
	if len(known.Pending) != 2 {
		t.Errorf("pending = %q, want both same-target rows for the dedup check", known.Pending)
	}
	if len(known.OwnPending) != 1 || known.OwnPending[0] != "the players bought me a drink" {
		t.Errorf("own pending = %q, want only the caller's row", known.OwnPending)
	}
}
