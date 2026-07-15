package knowledge_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/knowledge"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// fakeStore is a scripted knowledge.Store: it records the campaign/agent it was
// scoped to and replays fixed rows. The gm_private EXCLUSION lives in the SQL of
// SearchPublicNodes (proven against a real DB in the integration test); here the
// fake stands in for that already-filtered result, so these unit tests pin the
// adapter's scoping/routing without a DB.
type fakeStore struct {
	nodes        []storage.KGNode // SearchPublicNodes result (already gm_private-filtered by SQL)
	agentNodes   []storage.KGNode // AgentNodeFacts result
	lines        []storage.TranscriptLine
	gotNodeCID   uuid.UUID
	gotLineCID   uuid.UUID
	gotAgentID   uuid.UUID
	agentQueried bool

	// Knowledge Proposal write seam (#300).
	linkedNode    storage.KGNode
	linkedOK      bool
	gotLinkedAID  uuid.UUID
	proposalCID   uuid.UUID
	proposalAID   uuid.UUID
	proposalJSON  []byte
	proposalErrAt error // ctx.Err() observed INSIDE the store call
	createErr     error

	// Dedup read seam (#411).
	pending      []storage.KnowledgeProposal // ListPendingKnowledgeProposals result
	allNodes     []storage.KGNode            // ListNodes result
	gotPendCID   uuid.UUID
	gotNodesCID  uuid.UUID
	pendingErr   error
	listNodesErr error
}

func (f *fakeStore) ListPendingKnowledgeProposals(_ context.Context, cid uuid.UUID) ([]storage.KnowledgeProposal, error) {
	f.gotPendCID = cid
	return f.pending, f.pendingErr
}
func (f *fakeStore) ListNodes(_ context.Context, cid uuid.UUID) ([]storage.KGNode, error) {
	f.gotNodesCID = cid
	return f.allNodes, f.listNodesErr
}

func (f *fakeStore) AgentLinkedNode(_ context.Context, aid uuid.UUID) (storage.KGNode, bool, error) {
	f.gotLinkedAID = aid
	return f.linkedNode, f.linkedOK, nil
}
func (f *fakeStore) CreateKnowledgeProposal(ctx context.Context, cid, aid uuid.UUID, pw []byte) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.proposalCID = cid
	f.proposalAID = aid
	f.proposalJSON = pw
	f.proposalErrAt = ctx.Err()
	return nil
}

func (f *fakeStore) SearchPublicNodes(_ context.Context, cid uuid.UUID, _ string, _ int) ([]storage.KGNode, error) {
	f.gotNodeCID = cid
	return f.nodes, nil
}
func (f *fakeStore) SearchTranscriptLines(_ context.Context, cid uuid.UUID, _ string, _ int) ([]storage.TranscriptLine, error) {
	f.gotLineCID = cid
	return f.lines, nil
}
func (f *fakeStore) AgentNodeFacts(_ context.Context, aid uuid.UUID) ([]storage.KGNode, error) {
	f.agentQueried = true
	f.gotAgentID = aid
	return f.agentNodes, nil
}

// fakeSessions is a scripted active-session source.
type fakeSessions struct {
	sess storage.VoiceSession
	live bool
}

func (f fakeSessions) Snapshot() (storage.VoiceSession, bool) { return f.sess, f.live }

func liveSession(campaignID uuid.UUID) fakeSessions {
	return fakeSessions{sess: storage.VoiceSession{CampaignID: campaignID}, live: true}
}

// TestSearchFactsUsesPublicSearchScoped pins that SearchFacts routes through the
// gm_private-EXCLUDING search (SearchPublicNodes), scoped to the active session's
// Campaign, and maps the rows into facts. The actual gm_private exclusion is a SQL
// property proven in the integration test (a post-fetch Go filter would starve a
// public match past the LIMIT — the reviewer's finding).
func TestSearchFactsUsesPublicSearchScoped(t *testing.T) {
	cid := uuid.New()
	store := &fakeStore{nodes: []storage.KGNode{
		{Name: "Public Duke", Type: storage.KGNodeNPC, Body: "rules openly"},
	}}
	adapter := knowledge.New(store, store, liveSession(cid))

	facts, err := adapter.SearchFacts(context.Background(), "duke", 5)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if store.gotNodeCID != cid {
		t.Errorf("SearchPublicNodes scoped to %v, want active session's campaign %v", store.gotNodeCID, cid)
	}
	if len(facts) != 1 || facts[0].Name != "Public Duke" || facts[0].Type != "NPC" {
		t.Fatalf("facts = %+v, want the mapped public Duke (NPC)", facts)
	}
}

// TestSearchFactsNoSessionErrors pins the no-active-session error path.
func TestSearchFactsNoSessionErrors(t *testing.T) {
	adapter := knowledge.New(&fakeStore{}, &fakeStore{}, fakeSessions{live: false})
	if _, err := adapter.SearchFacts(context.Background(), "x", 5); !errors.Is(err, knowledge.ErrNoActiveSession) {
		t.Errorf("SearchFacts with no session = %v, want ErrNoActiveSession", err)
	}
}

// TestSearchTranscriptCampaignScoped pins the transcript search is scoped to the
// active session's Campaign and no session errors.
func TestSearchTranscriptCampaignScoped(t *testing.T) {
	cid := uuid.New()
	store := &fakeStore{lines: []storage.TranscriptLine{
		{Who: "Bart", Kind: "npc", Text: "I remember."},
	}}
	adapter := knowledge.New(store, store, liveSession(cid))

	hits, err := adapter.SearchTranscript(context.Background(), "remember", 5)
	if err != nil {
		t.Fatalf("SearchTranscript: %v", err)
	}
	if store.gotLineCID != cid {
		t.Errorf("transcript search scoped to %v, want %v", store.gotLineCID, cid)
	}
	if len(hits) != 1 || hits[0].Who != "Bart" || hits[0].Kind != "npc" {
		t.Errorf("hits = %+v, want the one Bart line", hits)
	}

	idle := knowledge.New(store, store, fakeSessions{live: false})
	if _, err := idle.SearchTranscript(context.Background(), "x", 5); !errors.Is(err, knowledge.ErrNoActiveSession) {
		t.Errorf("SearchTranscript with no session = %v, want ErrNoActiveSession", err)
	}
}

// TestOwnNodeFactsResolvesAgent pins that OwnNodeFacts keys the read by the
// caller's agent id (not a campaign), and an empty/invalid id has no
// neighbourhood — it reads nothing rather than falling back to a wider scope.
func TestOwnNodeFactsResolvesAgent(t *testing.T) {
	aid := uuid.New()
	store := &fakeStore{agentNodes: []storage.KGNode{
		{Name: "Mara", Type: storage.KGNodeCharacter, Body: "owes you"},
	}}
	adapter := knowledge.New(store, store, liveSession(uuid.New()))

	facts, err := adapter.OwnNodeFacts(context.Background(), aid.String())
	if err != nil {
		t.Fatalf("OwnNodeFacts: %v", err)
	}
	if store.gotAgentID != aid {
		t.Errorf("AgentNodeFacts keyed by %v, want caller %v", store.gotAgentID, aid)
	}
	if len(facts) != 1 || facts[0].Name != "Mara" || facts[0].Type != "Character" {
		t.Errorf("facts = %+v, want the Mara Character fact", facts)
	}

	// Empty caller id → no neighbourhood, no store read, no error.
	store.agentQueried = false
	empty, err := adapter.OwnNodeFacts(context.Background(), "")
	if err != nil {
		t.Fatalf("OwnNodeFacts(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("empty caller should yield no facts, got %+v", empty)
	}
	if store.agentQueried {
		t.Error("empty caller must not hit the store")
	}
}

// TestOwnNodeResolvesLinkedNode pins that OwnNode keys AgentLinkedNode by the
// caller's agent id and projects it into a storage-free ref; an empty/invalid id
// or an unlinked Agent reports ok=false without a wider fallback.
func TestOwnNodeResolvesLinkedNode(t *testing.T) {
	aid := uuid.New()
	nid := uuid.New()
	store := &fakeStore{linkedNode: storage.KGNode{ID: nid, Name: "Bartholomew"}, linkedOK: true}
	adapter := knowledge.New(store, store, liveSession(uuid.New()))

	ref, ok, err := adapter.OwnNode(context.Background(), aid.String())
	if err != nil || !ok {
		t.Fatalf("OwnNode: ok=%v err=%v", ok, err)
	}
	if store.gotLinkedAID != aid {
		t.Errorf("AgentLinkedNode keyed by %v, want %v", store.gotLinkedAID, aid)
	}
	if ref.ID != nid.String() || ref.Name != "Bartholomew" {
		t.Errorf("ref = %+v, want id %s name Bartholomew", ref, nid)
	}

	if _, ok, err := adapter.OwnNode(context.Background(), ""); ok || err != nil {
		t.Errorf("empty caller: ok=%v err=%v, want false/nil", ok, err)
	}

	store.linkedOK = false
	if _, ok, _ := adapter.OwnNode(context.Background(), aid.String()); ok {
		t.Error("unlinked agent should report ok=false")
	}
}

// TestCreateProposalShapeAndScope pins the adapter's write path (test 9): the
// Campaign comes from the active session (never the caller), the proposed write
// is marshalled to the exact jsonb shape, and the insert runs under a
// cancel-immune context so a barge cannot roll it back (ADR-0052).
func TestCreateProposalShapeAndScope(t *testing.T) {
	cid := uuid.New()
	aid := uuid.New()
	store := &fakeStore{}
	adapter := knowledge.New(store, store, liveSession(cid))

	// The turn ctx is ALREADY cancelled (barge) when the write starts; the store's
	// write ctx must NOT observe it (context.WithoutCancel).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := tool.ProposedWrite{V: 1, Kind: "fact", NodeID: "node-1", Subject: "Bartholomew", Fact: "brews ale"}
	if err := adapter.CreateProposal(ctx, aid.String(), w); err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}
	if store.proposalCID != cid {
		t.Errorf("proposal scoped to %v, want active session's campaign %v", store.proposalCID, cid)
	}
	if store.proposalAID != aid {
		t.Errorf("proposal authored by %v, want %v", store.proposalAID, aid)
	}
	var got tool.ProposedWrite
	if err := json.Unmarshal(store.proposalJSON, &got); err != nil {
		t.Fatalf("stored jsonb not valid ProposedWrite: %v (%s)", err, store.proposalJSON)
	}
	if got != w {
		t.Errorf("stored write = %+v, want %+v", got, w)
	}
	// Absent-kind fields must be omitted, not stored as empty strings.
	if s := string(store.proposalJSON); strings.Contains(s, `"relation"`) || strings.Contains(s, `"target"`) || strings.Contains(s, `"body"`) {
		t.Errorf("empty fields not omitted from jsonb: %s", s)
	}

	if store.proposalErrAt != nil {
		t.Errorf("write ctx observed the barge cancel: %v (WithoutCancel not applied)", store.proposalErrAt)
	}
}

// TestCreateProposalNoSessionErrors pins the no-active-session error path for the
// write seam.
func TestCreateProposalNoSessionErrors(t *testing.T) {
	adapter := knowledge.New(&fakeStore{}, &fakeStore{}, fakeSessions{live: false})
	err := adapter.CreateProposal(context.Background(), uuid.New().String(),
		tool.ProposedWrite{V: 1, Kind: "fact", Fact: "x"})
	if !errors.Is(err, knowledge.ErrNoActiveSession) {
		t.Errorf("CreateProposal with no session = %v, want ErrNoActiveSession", err)
	}
}
