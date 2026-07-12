package knowledge_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/knowledge"
	"github.com/MrWong99/Glyphoxa/internal/storage"
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
		linkedNode: storage.KGNode{ID: ownNodeID, Name: "Gesa", Body: "Gesa liebt Kuchen\nGesa wohnt im Wald"},
		linkedOK:   true,
		pending: []storage.KnowledgeProposal{
			pendingRow(cid, tool.ProposedWrite{Kind: "fact", NodeID: ownNodeID.String(), Subject: "Gesa", Fact: "ist die Schwester von Arturus"}),
			pendingRow(cid, tool.ProposedWrite{Kind: "fact", NodeID: uuid.New().String(), Subject: "Arturus", Fact: "ist ein Ritter"}), // different target
		},
	}
	adapter := knowledge.New(store, liveSession(cid))

	w := tool.ProposedWrite{Kind: "fact", NodeID: ownNodeID.String(), Subject: "Gesa", Fact: "something new"}
	known, err := adapter.ExistingKnowledge(context.Background(), aid.String(), w)
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
			pendingRow(cid, tool.ProposedWrite{Kind: "fact", Subject: "the duke", Fact: "is old"}),
		},
	}
	adapter := knowledge.New(store, liveSession(cid))

	w := tool.ProposedWrite{Kind: "fact", Subject: "The Duke", Fact: "new fact"}
	known, err := adapter.ExistingKnowledge(context.Background(), uuid.New().String(), w)
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

// No active session is a clean error the handler can fail open on.
func TestExistingKnowledge_NoSessionErrors(t *testing.T) {
	adapter := knowledge.New(&fakeStore{}, fakeSessions{live: false})
	if _, err := adapter.ExistingKnowledge(context.Background(), uuid.New().String(), tool.ProposedWrite{Kind: "fact", Subject: "X", Fact: "y"}); err == nil {
		t.Error("want error with no active session")
	}
}
