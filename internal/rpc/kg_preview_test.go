package rpc_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/kgfacts"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

func kgPreviewClient(t *testing.T, store *fakeKGPreviewStore) managementv1connect.CampaignServiceClient {
	t.Helper()
	return campaignClient(t, rpc.CampaignStores{Active: store, KGPreview: store})
}

// TestGetAgentFactPreview_RendersWhatTheTurnInjects pins the lens's whole reason
// to exist (#535): the preview must be the SAME render the voice loop injects,
// with the real budget accounting. A preview that merely resembles the prompt
// would confidently show a GM facts their NPC never receives.
func TestGetAgentFactPreview_RendersWhatTheTurnInjects(t *testing.T) {
	t.Parallel()
	store := newFakeKGPreviewStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Name: "Saltmarsh"}
	agentID := uuid.New()
	store.agents[agentID] = storage.Agent{ID: agentID, CampaignID: store.campaign.ID, Name: "Bart"}

	own := storage.KGNode{ID: uuid.New(), Type: storage.KGNodeNPC, Name: "Bart", Body: "An innkeeper."}
	neighbour := storage.KGNode{ID: uuid.New(), Type: storage.KGNodeLocation, Name: "Saltmarsh", Body: "A damp town."}
	store.linked[agentID] = own
	store.facts[agentID] = []storage.KGNode{own, neighbour}

	resp, err := kgPreviewClient(t, store).GetAgentFactPreview(context.Background(),
		connect.NewRequest(&managementv1.GetAgentFactPreviewRequest{AgentId: agentID.String()}))
	if err != nil {
		t.Fatalf("GetAgentFactPreview: %v", err)
	}
	msg := resp.Msg

	if !msg.GetLinked() || msg.GetLinkedNodeId() != own.ID.String() {
		t.Errorf("linked = %v / %q, want the Agent's own node", msg.GetLinked(), msg.GetLinkedNodeId())
	}
	want := kgfacts.RenderPreview([]storage.KGNode{own, neighbour})
	if len(msg.GetFacts()) != len(want.Facts) {
		t.Fatalf("got %d facts, want %d", len(msg.GetFacts()), len(want.Facts))
	}
	for i, f := range msg.GetFacts() {
		if f != want.Facts[i] {
			t.Errorf("fact[%d] = %q, want the renderer's %q", i, f, want.Facts[i])
		}
	}
	if !strings.Contains(msg.GetFacts()[0], "An innkeeper.") {
		t.Errorf("fact[0] lost its body: %q", msg.GetFacts()[0])
	}
	if len(msg.GetIncludedNodeIds()) != 2 || msg.GetIncludedNodeIds()[0] != own.ID.String() {
		t.Errorf("included = %v, want both nodes in injection order", msg.GetIncludedNodeIds())
	}
	if msg.GetTruncated() || len(msg.GetDroppedNodeIds()) != 0 {
		t.Errorf("a two-node neighbourhood reported truncation: %+v", msg)
	}
	if msg.GetMaxChars() != kgfacts.MaxBlockChars || msg.GetMaxFacts() != kgfacts.MaxFacts {
		t.Errorf("budget = %d/%d, want the real caps", msg.GetMaxChars(), msg.GetMaxFacts())
	}
	if msg.GetChars() <= 0 || msg.GetChars() > msg.GetMaxChars() {
		t.Errorf("chars = %d, want a real consumption inside the budget", msg.GetChars())
	}
	if msg.GetNeighbourhoodClipped() {
		t.Error("a two-node neighbourhood reported the read cap clipped it")
	}
	if msg.GetMaxNeighbours() != storage.MaxAgentFactNodes {
		t.Errorf("max_neighbours = %d, want the real read cap %d",
			msg.GetMaxNeighbours(), storage.MaxAgentFactNodes)
	}
}

// TestGetAgentFactPreview_ReportsReadCap pins the OTHER cap. The SQL read stops at
// its own row limit before the renderer sees anything, so a hub NPC past it would
// show its extra neighbours as merely "not adjacent" — and the GM would go hunting
// for an Edge that is not missing. It is reported separately from renderer
// truncation because the two have different fixes.
func TestGetAgentFactPreview_ReportsReadCap(t *testing.T) {
	t.Parallel()
	store := newFakeKGPreviewStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	agentID := uuid.New()
	store.agents[agentID] = storage.Agent{ID: agentID, CampaignID: store.campaign.ID}
	own := storage.KGNode{ID: uuid.New(), Type: storage.KGNodeNPC, Name: "Hub"}
	store.linked[agentID] = own

	// Exactly the cap's worth of rows — what the read returns when it clipped.
	nodes := make([]storage.KGNode, 0, storage.MaxAgentFactNodes)
	for i := range storage.MaxAgentFactNodes {
		nodes = append(nodes, storage.KGNode{
			ID: uuid.New(), Type: storage.KGNodeNote, Name: "N" + strconv.Itoa(i),
		})
	}
	store.facts[agentID] = nodes

	resp, err := kgPreviewClient(t, store).GetAgentFactPreview(context.Background(),
		connect.NewRequest(&managementv1.GetAgentFactPreviewRequest{AgentId: agentID.String()}))
	if err != nil {
		t.Fatalf("GetAgentFactPreview: %v", err)
	}
	if !resp.Msg.GetNeighbourhoodClipped() {
		t.Error("a neighbourhood at the read cap did not report being clipped")
	}
}

// TestGetAgentFactPreview_ReportsTruncation pins the visible half of the
// deterministic prefix-stop — the silent quality cliff this lens exists to expose.
func TestGetAgentFactPreview_ReportsTruncation(t *testing.T) {
	t.Parallel()
	store := newFakeKGPreviewStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	agentID := uuid.New()
	store.agents[agentID] = storage.Agent{ID: agentID, CampaignID: store.campaign.ID}
	own := storage.KGNode{ID: uuid.New(), Type: storage.KGNodeNPC, Name: "Bart"}
	store.linked[agentID] = own

	nodes := []storage.KGNode{own}
	for range 12 {
		nodes = append(nodes, storage.KGNode{
			ID: uuid.New(), Type: storage.KGNodeNote, Name: "Wall",
			Body: strings.Repeat("x", kgfacts.MaxFactChars),
		})
	}
	store.facts[agentID] = nodes

	resp, err := kgPreviewClient(t, store).GetAgentFactPreview(context.Background(),
		connect.NewRequest(&managementv1.GetAgentFactPreviewRequest{AgentId: agentID.String()}))
	if err != nil {
		t.Fatalf("GetAgentFactPreview: %v", err)
	}
	if !resp.Msg.GetTruncated() {
		t.Fatal("an over-budget neighbourhood did not report truncation")
	}
	if len(resp.Msg.GetDroppedNodeIds()) == 0 {
		t.Error("truncated preview named no dropped nodes; the GM cannot see what was cut")
	}
	if got := len(resp.Msg.GetIncludedNodeIds()) + len(resp.Msg.GetDroppedNodeIds()); got != len(nodes) {
		t.Errorf("accounted for %d of %d nodes; each must be in exactly one bucket", got, len(nodes))
	}
}

// TestGetAgentFactPreview_UnlinkedAgent pins the explicit empty state. An unlinked
// Character NPC legitimately gets nothing (AgentNodeFacts is keyed by Agent, with
// no campaign-wide fallback), and saying so beats an empty panel the GM has to
// interpret.
func TestGetAgentFactPreview_UnlinkedAgent(t *testing.T) {
	t.Parallel()
	store := newFakeKGPreviewStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	agentID := uuid.New()
	store.agents[agentID] = storage.Agent{ID: agentID, CampaignID: store.campaign.ID}

	resp, err := kgPreviewClient(t, store).GetAgentFactPreview(context.Background(),
		connect.NewRequest(&managementv1.GetAgentFactPreviewRequest{AgentId: agentID.String()}))
	if err != nil {
		t.Fatalf("GetAgentFactPreview: %v", err)
	}
	if resp.Msg.GetLinked() {
		t.Error("an unlinked agent reported linked")
	}
	if len(resp.Msg.GetFacts()) != 0 || resp.Msg.GetTruncated() {
		t.Errorf("unlinked preview = %+v, want an empty, untruncated state", resp.Msg)
	}
	if store.factCalls != 0 {
		t.Error("the facts read fired for an unlinked agent; there is nothing to read")
	}
}

// TestGetAgentFactPreview_Scoping pins that a crafted agent_id cannot preview
// another Campaign's world (#342), and that the refusal is indistinguishable from
// "no such agent" — another Campaign's roster is not something to probe for.
func TestGetAgentFactPreview_Scoping(t *testing.T) {
	t.Parallel()
	store := newFakeKGPreviewStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	foreign := uuid.New()
	store.agents[foreign] = storage.Agent{ID: foreign, CampaignID: uuid.New(), Name: "Elsewhere"}

	_, err := kgPreviewClient(t, store).GetAgentFactPreview(context.Background(),
		connect.NewRequest(&managementv1.GetAgentFactPreviewRequest{AgentId: foreign.String()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound for another campaign's agent", got)
	}
	if store.factCalls != 0 {
		t.Error("the facts read fired for a foreign agent before scoping refused it")
	}
}

func TestGetAgentFactPreview_ErrorMapping(t *testing.T) {
	t.Parallel()

	t.Run("bad uuid is InvalidArgument", func(t *testing.T) {
		store := newFakeKGPreviewStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		_, err := kgPreviewClient(t, store).GetAgentFactPreview(context.Background(),
			connect.NewRequest(&managementv1.GetAgentFactPreviewRequest{AgentId: "nope"}))
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Errorf("code = %v, want InvalidArgument", got)
		}
	})

	t.Run("unknown agent is NotFound", func(t *testing.T) {
		store := newFakeKGPreviewStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		_, err := kgPreviewClient(t, store).GetAgentFactPreview(context.Background(),
			connect.NewRequest(&managementv1.GetAgentFactPreviewRequest{AgentId: uuid.New().String()}))
		if got := connect.CodeOf(err); got != connect.CodeNotFound {
			t.Errorf("code = %v, want NotFound", got)
		}
	})

	t.Run("no active campaign is NotFound", func(t *testing.T) {
		store := newFakeKGPreviewStore()
		store.campErr = storage.ErrNotFound
		_, err := kgPreviewClient(t, store).GetAgentFactPreview(context.Background(),
			connect.NewRequest(&managementv1.GetAgentFactPreviewRequest{AgentId: uuid.New().String()}))
		if got := connect.CodeOf(err); got != connect.CodeNotFound {
			t.Errorf("code = %v, want NotFound", got)
		}
	})

	t.Run("linked-node read failure is Internal", func(t *testing.T) {
		store := newFakeKGPreviewStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		agentID := uuid.New()
		store.agents[agentID] = storage.Agent{ID: agentID, CampaignID: store.campaign.ID}
		store.linkedErr = errAny
		_, err := kgPreviewClient(t, store).GetAgentFactPreview(context.Background(),
			connect.NewRequest(&managementv1.GetAgentFactPreviewRequest{AgentId: agentID.String()}))
		if got := connect.CodeOf(err); got != connect.CodeInternal {
			t.Errorf("code = %v, want Internal", got)
		}
	})

	t.Run("facts read failure is Internal", func(t *testing.T) {
		store := newFakeKGPreviewStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		agentID := uuid.New()
		store.agents[agentID] = storage.Agent{ID: agentID, CampaignID: store.campaign.ID}
		store.linked[agentID] = storage.KGNode{ID: uuid.New()}
		store.factsErr = errAny
		_, err := kgPreviewClient(t, store).GetAgentFactPreview(context.Background(),
			connect.NewRequest(&managementv1.GetAgentFactPreviewRequest{AgentId: agentID.String()}))
		if got := connect.CodeOf(err); got != connect.CodeInternal {
			t.Errorf("code = %v, want Internal", got)
		}
	})
}
