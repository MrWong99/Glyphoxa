package rpc_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

func kgGraphClient(t *testing.T, store *fakeKGGraphStore) managementv1connect.CampaignServiceClient {
	t.Helper()
	return campaignClient(t, rpc.CampaignStores{Active: store, KGGraph: store})
}

// TestGetKnowledgeGraph_ReturnsWholeCampaignInOneCall pins #534's core AC: every
// Node and Edge for the ACTIVE campaign, in one call, campaign-scoped, with no
// prose in the payload.
func TestGetKnowledgeGraph_ReturnsWholeCampaignInOneCall(t *testing.T) {
	t.Parallel()
	store := newFakeKGGraphStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Name: "Saltmarsh"}
	bart, town := uuid.New(), uuid.New()
	agent := uuid.New()
	store.graphNodes = []storage.KGGraphNode{
		{
			ID: bart, Type: storage.KGNodeNPC, Name: "Bart",
			AgentID: uuid.NullUUID{UUID: agent, Valid: true}, BodyLen: 42, AspectCount: 3,
		},
		{ID: town, Type: storage.KGNodeLocation, Name: "Saltmarsh", GMPrivate: true},
	}
	store.edges = []storage.KGEdge{
		{ID: uuid.New(), FromNodeID: bart, ToNodeID: town, Type: storage.KGEdgeResidesIn},
	}
	client := kgGraphClient(t, store)

	resp, err := client.GetKnowledgeGraph(context.Background(),
		connect.NewRequest(&managementv1.GetKnowledgeGraphRequest{}))
	if err != nil {
		t.Fatalf("GetKnowledgeGraph: %v", err)
	}
	msg := resp.Msg
	if len(msg.GetNodes()) != 2 || len(msg.GetEdges()) != 1 {
		t.Fatalf("got %d nodes / %d edges, want 2 / 1", len(msg.GetNodes()), len(msg.GetEdges()))
	}

	got := msg.GetNodes()[0]
	if got.GetName() != "Bart" || got.GetNodeType() != managementv1.NodeType_NODE_TYPE_NPC {
		t.Errorf("node[0] = %+v, want the mapped NPC", got)
	}
	if got.GetAgentId() != agent.String() {
		t.Errorf("agent link not mapped: %q", got.GetAgentId())
	}
	if got.GetBodyLen() != 42 || got.GetAspectCount() != 3 {
		t.Errorf("content signal = body_len %d / aspect_count %d, want 42 / 3",
			got.GetBodyLen(), got.GetAspectCount())
	}

	// gm_private nodes ARE included: the graph is the GM's map of their own world,
	// and the client decides how to draw them. This is the opposite of the
	// prompt-facing seam, and the distinction is deliberate.
	if !msg.GetNodes()[1].GetGmPrivate() {
		t.Error("gm_private node was dropped; the GM-facing graph must include it")
	}

	e := msg.GetEdges()[0]
	if e.GetFromNodeId() != bart.String() || e.GetToNodeId() != town.String() ||
		e.GetEdgeType() != managementv1.EdgeType_EDGE_TYPE_RESIDES_IN {
		t.Errorf("edge = %+v, want the mapped resides_in", e)
	}

	// One read per side, scoped to the active campaign — not 300 per-node round trips.
	if store.graphNodeCalls != 1 || store.edgeCalls != 1 {
		t.Errorf("made %d node / %d edge reads, want exactly 1 each", store.graphNodeCalls, store.edgeCalls)
	}
	if store.graphCampaign != store.campaign.ID || store.edgeCampaign != store.campaign.ID {
		t.Errorf("reads scoped to %s / %s, want the active campaign %s",
			store.graphCampaign, store.edgeCampaign, store.campaign.ID)
	}
}

func TestGetKnowledgeGraph_ErrorMapping(t *testing.T) {
	t.Parallel()

	t.Run("no campaign is NotFound", func(t *testing.T) {
		store := newFakeKGGraphStore()
		store.campErr = storage.ErrNotFound
		_, err := kgGraphClient(t, store).GetKnowledgeGraph(context.Background(),
			connect.NewRequest(&managementv1.GetKnowledgeGraphRequest{}))
		if got := connect.CodeOf(err); got != connect.CodeNotFound {
			t.Errorf("code = %v, want NotFound", got)
		}
	})

	t.Run("node read failure is Internal", func(t *testing.T) {
		store := newFakeKGGraphStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		store.graphNodeErr = errAny
		_, err := kgGraphClient(t, store).GetKnowledgeGraph(context.Background(),
			connect.NewRequest(&managementv1.GetKnowledgeGraphRequest{}))
		if got := connect.CodeOf(err); got != connect.CodeInternal {
			t.Errorf("code = %v, want Internal", got)
		}
	})

	t.Run("edge read failure is Internal", func(t *testing.T) {
		store := newFakeKGGraphStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		store.edgeErr = errAny
		_, err := kgGraphClient(t, store).GetKnowledgeGraph(context.Background(),
			connect.NewRequest(&managementv1.GetKnowledgeGraphRequest{}))
		if got := connect.CodeOf(err); got != connect.CodeInternal {
			t.Errorf("code = %v, want Internal", got)
		}
	})
}

// TestGetKnowledgeGraph_EmptyCampaign pins that an empty world is an empty
// payload, not an error — a brand-new campaign opens the Graph tab too.
func TestGetKnowledgeGraph_EmptyCampaign(t *testing.T) {
	t.Parallel()
	store := newFakeKGGraphStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	resp, err := kgGraphClient(t, store).GetKnowledgeGraph(context.Background(),
		connect.NewRequest(&managementv1.GetKnowledgeGraphRequest{}))
	if err != nil {
		t.Fatalf("GetKnowledgeGraph: %v", err)
	}
	if len(resp.Msg.GetNodes()) != 0 || len(resp.Msg.GetEdges()) != 0 {
		t.Errorf("empty campaign returned %+v", resp.Msg)
	}
}
