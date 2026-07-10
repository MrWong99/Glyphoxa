package knowledge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/knowledge"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeStore is a scripted knowledge.Store: it records the campaign/agent it was
// scoped to and replays fixed rows, so the adapter's session-scoping and
// gm_private filter are pinned without a DB.
type fakeStore struct {
	nodes        []storage.KGNode // SearchNodes result (GM-facing, unfiltered)
	agentNodes   []storage.KGNode // AgentNodeFacts result
	lines        []storage.TranscriptLine
	gotNodeCID   uuid.UUID
	gotLineCID   uuid.UUID
	gotAgentID   uuid.UUID
	agentQueried bool
}

func (f *fakeStore) SearchNodes(_ context.Context, cid uuid.UUID, _ string, _ int) ([]storage.KGNode, error) {
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

// TestSearchFactsDropsGMPrivate is the load-bearing ADR-0008 pin: SearchNodes is
// GM-facing (returns gm_private Nodes), so the adapter MUST drop them before they
// reach an NPC prompt.
func TestSearchFactsDropsGMPrivate(t *testing.T) {
	cid := uuid.New()
	store := &fakeStore{nodes: []storage.KGNode{
		{Name: "Public Duke", Type: storage.KGNodeNPC, Body: "rules openly", GMPrivate: false},
		{Name: "Secret Cabal", Type: storage.KGNodeFaction, Body: "GM eyes only", GMPrivate: true},
	}}
	adapter := knowledge.New(store, liveSession(cid))

	facts, err := adapter.SearchFacts(context.Background(), "duke", 5)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if store.gotNodeCID != cid {
		t.Errorf("SearchNodes scoped to %v, want active session's campaign %v", store.gotNodeCID, cid)
	}
	if len(facts) != 1 || facts[0].Name != "Public Duke" {
		t.Fatalf("facts = %+v, want only the public Node (gm_private dropped)", facts)
	}
	if facts[0].Type != "NPC" {
		t.Errorf("type label = %q, want NPC", facts[0].Type)
	}
}

// TestSearchFactsNoSessionErrors pins the no-active-session error path.
func TestSearchFactsNoSessionErrors(t *testing.T) {
	adapter := knowledge.New(&fakeStore{}, fakeSessions{live: false})
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
	adapter := knowledge.New(store, liveSession(cid))

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

	idle := knowledge.New(store, fakeSessions{live: false})
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
	adapter := knowledge.New(store, liveSession(uuid.New()))

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
