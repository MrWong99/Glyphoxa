package rpc_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
)

// edgeClient stands up the CampaignService client with the KG-Edge slice and
// the shared Active-Campaign resolution both served by the one fakeKGEdgeStore
// (it embeds *fakeActive), the composition every test here exercises (#445).
func edgeClient(t *testing.T, store *fakeKGEdgeStore) managementv1connect.CampaignServiceClient {
	t.Helper()
	return campaignClient(t, rpc.CampaignStores{Active: store, KGEdges: store})
}

func TestCreateEdge_MapsAndPersists(t *testing.T) {
	t.Parallel()
	campID := uuid.New()
	from, to := uuid.New(), uuid.New()
	store := newFakeKGEdgeStore()
	store.campaign = storage.Campaign{ID: campID, Name: "Lost Mine"}
	client := edgeClient(t, store)

	resp, err := client.CreateEdge(context.Background(),
		connect.NewRequest(&managementv1.CreateEdgeRequest{
			FromNodeId: from.String(), ToNodeId: to.String(),
			EdgeType: managementv1.EdgeType_EDGE_TYPE_RESIDES_IN,
		}))
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}
	edge := resp.Msg.GetEdge()
	if edge.GetFromNodeId() != from.String() || edge.GetToNodeId() != to.String() {
		t.Errorf("endpoint ids not mapped: %+v", edge)
	}
	if edge.GetEdgeType() != managementv1.EdgeType_EDGE_TYPE_RESIDES_IN {
		t.Errorf("edge_type = %v, want RESIDES_IN", edge.GetEdgeType())
	}
	// The handler resolved the active campaign and forwarded the endpoints + type.
	if len(store.edgesCreated) != 1 {
		t.Fatalf("store saw %d creates, want 1", len(store.edgesCreated))
	}
	got := store.edgesCreated[0]
	if got.CampaignID != campID || got.FromNodeID != from || got.ToNodeID != to || got.Type != storage.KGEdgeResidesIn {
		t.Errorf("storage input wrong: %+v", got)
	}
}

func TestCreateEdge_UnspecifiedTypeIsInvalidArgument(t *testing.T) {
	t.Parallel()
	store := newFakeKGEdgeStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	client := edgeClient(t, store)

	_, err := client.CreateEdge(context.Background(),
		connect.NewRequest(&managementv1.CreateEdgeRequest{
			FromNodeId: uuid.NewString(), ToNodeId: uuid.NewString(),
		}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestCreateEdge_InvalidEndpointIdIsInvalidArgument(t *testing.T) {
	t.Parallel()
	store := newFakeKGEdgeStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	client := edgeClient(t, store)

	_, err := client.CreateEdge(context.Background(),
		connect.NewRequest(&managementv1.CreateEdgeRequest{
			FromNodeId: "not-a-uuid", ToNodeId: uuid.NewString(),
			EdgeType: managementv1.EdgeType_EDGE_TYPE_KNOWS,
		}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestCreateEdge_StorageErrorsMapToCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		storErr error
		want    connect.Code
	}{
		{"invalid combo → InvalidArgument", storage.ErrInvalidEdge, connect.CodeInvalidArgument},
		{"duplicate → AlreadyExists", storage.ErrConflict, connect.CodeAlreadyExists},
		{"missing/cross-campaign endpoint → NotFound", storage.ErrNotFound, connect.CodeNotFound},
		{"opaque failure → Internal", errAny, connect.CodeInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeKGEdgeStore()
			store.campaign = storage.Campaign{ID: uuid.New()}
			store.edgeCreateErr = tc.storErr
			client := edgeClient(t, store)

			_, err := client.CreateEdge(context.Background(),
				connect.NewRequest(&managementv1.CreateEdgeRequest{
					FromNodeId: uuid.NewString(), ToNodeId: uuid.NewString(),
					EdgeType: managementv1.EdgeType_EDGE_TYPE_KNOWS,
				}))
			if got := connect.CodeOf(err); got != tc.want {
				t.Errorf("code = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCreateEdge_NoCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeKGEdgeStore()
	store.campErr = storage.ErrNotFound
	client := edgeClient(t, store)

	_, err := client.CreateEdge(context.Background(),
		connect.NewRequest(&managementv1.CreateEdgeRequest{
			FromNodeId: uuid.NewString(), ToNodeId: uuid.NewString(),
			EdgeType: managementv1.EdgeType_EDGE_TYPE_KNOWS,
		}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

func TestDeleteEdge_Deletes(t *testing.T) {
	t.Parallel()
	store := newFakeKGEdgeStore()
	client := edgeClient(t, store)

	if _, err := client.DeleteEdge(context.Background(),
		connect.NewRequest(&managementv1.DeleteEdgeRequest{Id: uuid.NewString()})); err != nil {
		t.Fatalf("DeleteEdge: %v", err)
	}
}

func TestDeleteEdge_InvalidIdIsInvalidArgument(t *testing.T) {
	t.Parallel()
	client := edgeClient(t, newFakeKGEdgeStore())

	_, err := client.DeleteEdge(context.Background(),
		connect.NewRequest(&managementv1.DeleteEdgeRequest{Id: "nope"}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestDeleteEdge_NotFoundIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeKGEdgeStore()
	store.edgeDeleteErr = storage.ErrNotFound
	client := edgeClient(t, store)

	_, err := client.DeleteEdge(context.Background(),
		connect.NewRequest(&managementv1.DeleteEdgeRequest{Id: uuid.NewString()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

// TestDeleteEdge_ScopesToActiveCampaign is #342: the delete is scoped to the
// resolved active campaign, so another campaign's Edge is never removable.
func TestDeleteEdge_ScopesToActiveCampaign(t *testing.T) {
	t.Parallel()
	store := newFakeKGEdgeStore()
	activeID := uuid.New()
	store.campaign = storage.Campaign{ID: activeID}
	client := edgeClient(t, store)

	if _, err := client.DeleteEdge(context.Background(),
		connect.NewRequest(&managementv1.DeleteEdgeRequest{Id: uuid.NewString()})); err != nil {
		t.Fatalf("DeleteEdge: %v", err)
	}
	if store.deleteEdgeCampaign != activeID {
		t.Errorf("DeleteEdge scoped to %s, want active %s", store.deleteEdgeCampaign, activeID)
	}
}

// TestDeleteEdge_NoActiveCampaignIsNotFound is #342: without an active campaign the
// scoped delete cannot resolve an owner and returns CodeNotFound.
func TestDeleteEdge_NoActiveCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeKGEdgeStore()
	store.campErr = storage.ErrNotFound
	client := edgeClient(t, store)

	_, err := client.DeleteEdge(context.Background(),
		connect.NewRequest(&managementv1.DeleteEdgeRequest{Id: uuid.NewString()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

// TestListNodeEdges_SplitsAndJoins is the AC's incident-edge list: outgoing and
// incoming are returned as SEPARATE lists (strictly directional), each carrying
// the server-joined endpoint name + type so the UI needs no N+1.
func TestListNodeEdges_SplitsAndJoins(t *testing.T) {
	t.Parallel()
	node := uuid.New()
	barrow := uuid.New()
	cyra := uuid.New()
	store := newFakeKGEdgeStore()
	store.edgesOut = []storage.KGEdgeWithNodes{{
		KGEdge:   storage.KGEdge{ID: uuid.New(), FromNodeID: node, ToNodeID: barrow, Type: storage.KGEdgeResidesIn},
		FromName: "Aldric", FromType: storage.KGNodeCharacter,
		ToName: "Barrow", ToType: storage.KGNodeLocation,
	}}
	store.edgesIn = []storage.KGEdgeWithNodes{{
		KGEdge:   storage.KGEdge{ID: uuid.New(), FromNodeID: cyra, ToNodeID: node, Type: storage.KGEdgeKnows},
		FromName: "Cyra", FromType: storage.KGNodeCharacter,
		ToName: "Aldric", ToType: storage.KGNodeCharacter,
	}}
	client := edgeClient(t, store)

	resp, err := client.ListNodeEdges(context.Background(),
		connect.NewRequest(&managementv1.ListNodeEdgesRequest{NodeId: node.String()}))
	if err != nil {
		t.Fatalf("ListNodeEdges: %v", err)
	}
	out := resp.Msg.GetOutgoing()
	in := resp.Msg.GetIncoming()
	if len(out) != 1 || len(in) != 1 {
		t.Fatalf("split = %d out / %d in, want 1/1", len(out), len(in))
	}
	if out[0].GetToNodeName() != "Barrow" || out[0].GetToNodeType() != managementv1.NodeType_NODE_TYPE_LOCATION {
		t.Errorf("outgoing join not mapped: %+v", out[0])
	}
	if out[0].GetEdgeType() != managementv1.EdgeType_EDGE_TYPE_RESIDES_IN {
		t.Errorf("outgoing type = %v, want RESIDES_IN", out[0].GetEdgeType())
	}
	if in[0].GetFromNodeName() != "Cyra" || in[0].GetFromNodeType() != managementv1.NodeType_NODE_TYPE_CHARACTER {
		t.Errorf("incoming join not mapped: %+v", in[0])
	}
}

// TestListNodeEdges_ScopesToActiveCampaign is #356: the read resolves the active
// campaign and passes its id to the store, so the anchor Node's ownership is
// verified there — another campaign's Node is never listable through this session.
func TestListNodeEdges_ScopesToActiveCampaign(t *testing.T) {
	t.Parallel()
	store := newFakeKGEdgeStore()
	activeID := uuid.New()
	store.campaign = storage.Campaign{ID: activeID}
	client := edgeClient(t, store)

	if _, err := client.ListNodeEdges(context.Background(),
		connect.NewRequest(&managementv1.ListNodeEdgesRequest{NodeId: uuid.NewString()})); err != nil {
		t.Fatalf("ListNodeEdges: %v", err)
	}
	if store.nodeEdgesCampaign != activeID {
		t.Errorf("NodeEdges scoped to %s, want active %s", store.nodeEdgesCampaign, activeID)
	}
}

// TestListNodeEdges_CrossCampaignIsNotFound is #356: a Node in another campaign is
// invisible — the store refuses it as ErrNotFound and the handler surfaces
// CodeNotFound, never a leaked edge list or an existence oracle.
func TestListNodeEdges_CrossCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeKGEdgeStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	store.nodeEdgesErr = storage.ErrNotFound // the scoped store refuses a foreign anchor
	client := edgeClient(t, store)

	_, err := client.ListNodeEdges(context.Background(),
		connect.NewRequest(&managementv1.ListNodeEdgesRequest{NodeId: uuid.NewString()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound (cross-campaign node)", got)
	}
}

// TestListNodeEdges_NoActiveCampaignIsNotFound is #356: without an active campaign
// the read cannot resolve an owning campaign and is CodeNotFound.
func TestListNodeEdges_NoActiveCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeKGEdgeStore()
	store.campErr = storage.ErrNotFound
	client := edgeClient(t, store)

	_, err := client.ListNodeEdges(context.Background(),
		connect.NewRequest(&managementv1.ListNodeEdgesRequest{NodeId: uuid.NewString()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound (no active campaign)", got)
	}
}

func TestListNodeEdges_InvalidIdIsInvalidArgument(t *testing.T) {
	t.Parallel()
	client := edgeClient(t, newFakeKGEdgeStore())

	_, err := client.ListNodeEdges(context.Background(),
		connect.NewRequest(&managementv1.ListNodeEdgesRequest{NodeId: "bad"}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestListNodeEdges_StorageErrorIsInternal(t *testing.T) {
	t.Parallel()
	store := newFakeKGEdgeStore()
	store.nodeEdgesErr = errAny
	client := edgeClient(t, store)

	_, err := client.ListNodeEdges(context.Background(),
		connect.NewRequest(&managementv1.ListNodeEdgesRequest{NodeId: uuid.NewString()}))
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", got)
	}
}

func TestSetNodeAgent_LinksAndMapsAgentId(t *testing.T) {
	t.Parallel()
	node := uuid.New()
	agent := uuid.New()
	store := newFakeKGEdgeStore()
	store.setAgentNode = storage.KGNode{ID: node, Type: storage.KGNodeNPC, Name: "Bart"}
	client := edgeClient(t, store)

	resp, err := client.SetNodeAgent(context.Background(),
		connect.NewRequest(&managementv1.SetNodeAgentRequest{
			NodeId: node.String(), AgentId: agent.String(),
		}))
	if err != nil {
		t.Fatalf("SetNodeAgent: %v", err)
	}
	if resp.Msg.GetNode().GetAgentId() != agent.String() {
		t.Errorf("agent_id not mapped: %q", resp.Msg.GetNode().GetAgentId())
	}
	if len(store.setAgentCalls) != 1 || !store.setAgentCalls[0].agentID.Valid || store.setAgentCalls[0].agentID.UUID != agent {
		t.Errorf("store not asked to link the agent: %+v", store.setAgentCalls)
	}
}

func TestSetNodeAgent_EmptyAgentIdUnlinks(t *testing.T) {
	t.Parallel()
	node := uuid.New()
	store := newFakeKGEdgeStore()
	store.setAgentNode = storage.KGNode{ID: node, Type: storage.KGNodeNPC, Name: "Bart"}
	client := edgeClient(t, store)

	resp, err := client.SetNodeAgent(context.Background(),
		connect.NewRequest(&managementv1.SetNodeAgentRequest{NodeId: node.String(), AgentId: ""}))
	if err != nil {
		t.Fatalf("SetNodeAgent unlink: %v", err)
	}
	if resp.Msg.GetNode().GetAgentId() != "" {
		t.Errorf("agent_id should be cleared on unlink: %q", resp.Msg.GetNode().GetAgentId())
	}
	if len(store.setAgentCalls) != 1 || store.setAgentCalls[0].agentID.Valid {
		t.Errorf("unlink must pass an invalid NullUUID: %+v", store.setAgentCalls)
	}
}

func TestSetNodeAgent_InvalidIdsAreInvalidArgument(t *testing.T) {
	t.Parallel()
	cases := []struct{ node, agent string }{
		{"bad", uuid.NewString()},
		{uuid.NewString(), "bad"},
	}
	for _, tc := range cases {
		store := newFakeKGEdgeStore()
		client := edgeClient(t, store)
		_, err := client.SetNodeAgent(context.Background(),
			connect.NewRequest(&managementv1.SetNodeAgentRequest{NodeId: tc.node, AgentId: tc.agent}))
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Errorf("node=%q agent=%q code = %v, want InvalidArgument", tc.node, tc.agent, got)
		}
	}
}

func TestSetNodeAgent_StorageErrorsMapToCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		storErr error
		want    connect.Code
	}{
		{"non-NPC node → InvalidArgument", storage.ErrInvalidEdge, connect.CodeInvalidArgument},
		{"agent already linked → AlreadyExists", storage.ErrConflict, connect.CodeAlreadyExists},
		{"missing/cross-campaign → NotFound", storage.ErrNotFound, connect.CodeNotFound},
		{"opaque failure → Internal", errAny, connect.CodeInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeKGEdgeStore()
			store.setAgentErr = tc.storErr
			client := edgeClient(t, store)

			_, err := client.SetNodeAgent(context.Background(),
				connect.NewRequest(&managementv1.SetNodeAgentRequest{
					NodeId: uuid.NewString(), AgentId: uuid.NewString(),
				}))
			if got := connect.CodeOf(err); got != tc.want {
				t.Errorf("code = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSetNodeAgent_ScopesToActiveCampaign is #342: the handler resolves the active
// campaign and passes its id down, so the store matches the Node's campaign against
// it — a cross-campaign link/unlink is refused server-side.
func TestSetNodeAgent_ScopesToActiveCampaign(t *testing.T) {
	t.Parallel()
	node := uuid.New()
	agent := uuid.New()
	store := newFakeKGEdgeStore()
	activeID := uuid.New()
	store.campaign = storage.Campaign{ID: activeID}
	store.setAgentNode = storage.KGNode{ID: node, Type: storage.KGNodeNPC, Name: "Bart"}
	client := edgeClient(t, store)

	if _, err := client.SetNodeAgent(context.Background(),
		connect.NewRequest(&managementv1.SetNodeAgentRequest{NodeId: node.String(), AgentId: agent.String()})); err != nil {
		t.Fatalf("SetNodeAgent: %v", err)
	}
	if store.setAgentCampaign != activeID {
		t.Errorf("SetNodeAgent scoped to %s, want active %s", store.setAgentCampaign, activeID)
	}
}

// TestSetNodeAgent_NoActiveCampaignIsNotFound is #342: without an active campaign
// the scoped link/unlink cannot resolve an owner and returns CodeNotFound.
func TestSetNodeAgent_NoActiveCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeKGEdgeStore()
	store.campErr = storage.ErrNotFound
	client := edgeClient(t, store)

	_, err := client.SetNodeAgent(context.Background(),
		connect.NewRequest(&managementv1.SetNodeAgentRequest{NodeId: uuid.NewString(), AgentId: uuid.NewString()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

// TestCreateEdgeHonorsDurableSelection is #222 for the KG wiki edge write: a new
// Edge is created in the durable /glyphoxa use selection (D), not the most-recent
// default (N).
func TestCreateEdgeHonorsDurableSelection(t *testing.T) {
	t.Parallel()
	durable := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Durable D"}
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer N"}
	store := newFakeKGEdgeStore()
	store.forUser = durable
	store.campaign = newer
	client := campaignClientAs(t, rpc.CampaignStores{Active: store, KGEdges: store},
		storage.User{DiscordUserID: "999"}, uuid.Nil, nil)

	if _, err := client.CreateEdge(context.Background(),
		connect.NewRequest(&managementv1.CreateEdgeRequest{
			FromNodeId: uuid.New().String(),
			ToNodeId:   uuid.New().String(),
			EdgeType:   managementv1.EdgeType_EDGE_TYPE_KNOWS,
		})); err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}
	if len(store.edgesCreated) != 1 {
		t.Fatalf("created %d edges, want 1", len(store.edgesCreated))
	}
	if got := store.edgesCreated[0].CampaignID; got != durable.ID {
		t.Errorf("edge created in campaign %s, want the durable selection %s (not the newer %s)", got, durable.ID, newer.ID)
	}
}

// TestCreateEdgeHonorsLiveSession is #222 live-first for the KG wiki edge write: a
// new Edge is created in the LIVE session's campaign (L).
func TestCreateEdgeHonorsLiveSession(t *testing.T) {
	t.Parallel()
	live := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Live L"}
	durable := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Durable D"}
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer N"}
	store := newFakeKGEdgeStore()
	store.forUser = durable
	store.campaign = newer
	store.campaignsByID = map[uuid.UUID]storage.Campaign{live.ID: live}
	client := campaignClientAs(t, rpc.CampaignStores{Active: store, KGEdges: store},
		storage.User{DiscordUserID: "999"}, uuid.Nil, liveMgr(live.ID))

	if _, err := client.CreateEdge(context.Background(),
		connect.NewRequest(&managementv1.CreateEdgeRequest{
			FromNodeId: uuid.New().String(),
			ToNodeId:   uuid.New().String(),
			EdgeType:   managementv1.EdgeType_EDGE_TYPE_KNOWS,
		})); err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}
	if len(store.edgesCreated) != 1 {
		t.Fatalf("created %d edges, want 1", len(store.edgesCreated))
	}
	if got := store.edgesCreated[0].CampaignID; got != live.ID {
		t.Errorf("edge created in campaign %s, want the LIVE session campaign %s (not durable %s / newer %s)",
			got, live.ID, durable.ID, newer.ID)
	}
}

// TestUpdateEdgeDetails pins #546's editor path: a relation carries texture, the
// write is campaign-scoped, and an out-of-range feeling is refused with a
// sentence rather than a constraint violation.
func TestUpdateEdgeDetails(t *testing.T) {
	t.Parallel()
	store := newFakeKGEdgeStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	edgeID := uuid.New()
	store.edges = []storage.KGEdge{{ID: edgeID, CampaignID: store.campaign.ID, Type: storage.KGEdgeKnows}}
	client := edgeClient(t, store)

	resp, err := client.UpdateEdgeDetails(context.Background(),
		connect.NewRequest(&managementv1.UpdateEdgeDetailsRequest{
			Id: edgeID.String(), Note: "  owes money since the siege  ", Disposition: -2,
		}))
	if err != nil {
		t.Fatalf("UpdateEdgeDetails: %v", err)
	}
	got := resp.Msg.GetEdge()
	if got.GetNote() != "owes money since the siege" {
		t.Errorf("note = %q, want it trimmed and saved", got.GetNote())
	}
	if got.GetDisposition() != -2 {
		t.Errorf("disposition = %d, want -2", got.GetDisposition())
	}
	if store.edgeDetailsCampaign != store.campaign.ID {
		t.Errorf("write scoped to %s, want the active campaign", store.edgeDetailsCampaign)
	}
}

func TestUpdateEdgeDetails_Validation(t *testing.T) {
	t.Parallel()

	t.Run("bad uuid is InvalidArgument", func(t *testing.T) {
		store := newFakeKGEdgeStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		_, err := edgeClient(t, store).UpdateEdgeDetails(context.Background(),
			connect.NewRequest(&managementv1.UpdateEdgeDetailsRequest{Id: "nope"}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("code = %v, want InvalidArgument", connect.CodeOf(err))
		}
	})

	t.Run("an overlong note is refused before the write", func(t *testing.T) {
		store := newFakeKGEdgeStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		_, err := edgeClient(t, store).UpdateEdgeDetails(context.Background(),
			connect.NewRequest(&managementv1.UpdateEdgeDetailsRequest{
				Id: uuid.New().String(), Note: strings.Repeat("x", kgvocab.MaxEdgeNoteRunes+1),
			}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("code = %v, want InvalidArgument", connect.CodeOf(err))
		}
		if store.edgeDetailsCampaign != uuid.Nil {
			t.Error("the store was written despite an invalid note")
		}
	})

	t.Run("an out-of-range disposition is refused", func(t *testing.T) {
		store := newFakeKGEdgeStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		store.edgeDetailsErr = storage.ErrInvalidDisposition
		_, err := edgeClient(t, store).UpdateEdgeDetails(context.Background(),
			connect.NewRequest(&managementv1.UpdateEdgeDetailsRequest{
				Id: uuid.New().String(), Disposition: 9,
			}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("code = %v, want InvalidArgument", connect.CodeOf(err))
		}
	})

	t.Run("a missing relation is NotFound", func(t *testing.T) {
		store := newFakeKGEdgeStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		_, err := edgeClient(t, store).UpdateEdgeDetails(context.Background(),
			connect.NewRequest(&managementv1.UpdateEdgeDetailsRequest{Id: uuid.New().String()}))
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("code = %v, want NotFound", connect.CodeOf(err))
		}
	})
}
