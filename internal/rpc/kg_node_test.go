package rpc_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

func TestCreateNode_MapsAndPersists(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Name: "Lost Mine"}
	client := crudClient(t, store)

	resp, err := client.CreateNode(context.Background(),
		connect.NewRequest(&managementv1.CreateNodeRequest{
			NodeType:  managementv1.NodeType_NODE_TYPE_NOTE,
			Name:      "The sealed vault",
			Body:      "Nobody has opened it in a century.",
			GmPrivate: true,
		}))
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	node := resp.Msg.GetNode()
	if node.GetNodeType() != managementv1.NodeType_NODE_TYPE_NOTE {
		t.Errorf("node_type = %v, want NOTE", node.GetNodeType())
	}
	if node.GetName() != "The sealed vault" || node.GetBody() != "Nobody has opened it in a century." {
		t.Errorf("fields not mapped: %+v", node)
	}
	if !node.GetGmPrivate() {
		t.Error("gm_private did not round-trip")
	}
	if node.GetId() == "" || node.GetCampaignId() != store.campaign.ID.String() {
		t.Errorf("ids not mapped: %+v", node)
	}
	// The handler forwarded the Note type + gm_private to storage.
	if len(store.nodesCreated) != 1 {
		t.Fatalf("store saw %d creates, want 1", len(store.nodesCreated))
	}
	if store.nodesCreated[0].Type != storage.KGNodeNote || !store.nodesCreated[0].GMPrivate {
		t.Errorf("storage input wrong: %+v", store.nodesCreated[0])
	}
}

func TestCreateNode_TrimsName(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Name: "Lost Mine"}
	client := crudClient(t, store)

	resp, err := client.CreateNode(context.Background(),
		connect.NewRequest(&managementv1.CreateNodeRequest{
			NodeType: managementv1.NodeType_NODE_TYPE_NOTE, Name: "  Bob  ",
		}))
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	// The stored name — and the echoed one — must be trimmed, not " Bob " (which
	// would render "###  Bob " into a prompt).
	if store.nodesCreated[0].Name != "Bob" {
		t.Errorf("stored name = %q, want trimmed %q", store.nodesCreated[0].Name, "Bob")
	}
	if resp.Msg.GetNode().GetName() != "Bob" {
		t.Errorf("echoed name = %q, want trimmed", resp.Msg.GetNode().GetName())
	}
}

func TestCreateNode_UnspecifiedTypeIsInvalidArgument(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Name: "Lost Mine"}
	client := crudClient(t, store)

	_, err := client.CreateNode(context.Background(),
		connect.NewRequest(&managementv1.CreateNodeRequest{Name: "x"})) // type unset = UNSPECIFIED
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestCreateNode_EmptyNameIsInvalidArgument(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Name: "Lost Mine"}
	client := crudClient(t, store)

	_, err := client.CreateNode(context.Background(),
		connect.NewRequest(&managementv1.CreateNodeRequest{
			NodeType: managementv1.NodeType_NODE_TYPE_NOTE, Name: "   ",
		}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestCreateNode_StorageErrorIsInternal(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Name: "Lost Mine"}
	store.nodeCreateErr = errAny
	client := crudClient(t, store)

	_, err := client.CreateNode(context.Background(),
		connect.NewRequest(&managementv1.CreateNodeRequest{
			NodeType: managementv1.NodeType_NODE_TYPE_NOTE, Name: "x",
		}))
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", got)
	}
}

func TestListNodes_MapsInStorageOrder(t *testing.T) {
	t.Parallel()
	campID := uuid.New()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: campID, Name: "Lost Mine"}
	// The storage layer owns the ordering; the handler must preserve it 1:1.
	store.nodes = []storage.KGNode{
		{ID: uuid.New(), CampaignID: campID, Type: storage.KGNodeLocation, Name: "Barrow"},
		{ID: uuid.New(), CampaignID: campID, Type: storage.KGNodeNote, Name: "Rumor", Body: "hush", GMPrivate: true},
	}
	client := crudClient(t, store)

	resp, err := client.ListNodes(context.Background(),
		connect.NewRequest(&managementv1.ListNodesRequest{}))
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	nodes := resp.Msg.GetNodes()
	if len(nodes) != 2 {
		t.Fatalf("len = %d, want 2", len(nodes))
	}
	if nodes[0].GetNodeType() != managementv1.NodeType_NODE_TYPE_LOCATION || nodes[0].GetName() != "Barrow" {
		t.Errorf("nodes[0] not mapped in order: %+v", nodes[0])
	}
	if nodes[1].GetNodeType() != managementv1.NodeType_NODE_TYPE_NOTE || !nodes[1].GetGmPrivate() {
		t.Errorf("nodes[1] not mapped: %+v", nodes[1])
	}
}

func TestListNodes_NoCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campErr = storage.ErrNotFound
	client := crudClient(t, store)

	_, err := client.ListNodes(context.Background(),
		connect.NewRequest(&managementv1.ListNodesRequest{}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

func TestListNodes_StorageErrorIsInternal(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Name: "Lost Mine"}
	store.nodeListErr = errAny
	client := crudClient(t, store)

	_, err := client.ListNodes(context.Background(),
		connect.NewRequest(&managementv1.ListNodesRequest{}))
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", got)
	}
}

func TestUpdateNode_MapsAndPersists(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	store := newFakeStore()
	store.nodes = []storage.KGNode{
		{ID: id, CampaignID: uuid.New(), Type: storage.KGNodeLocation, Name: "Harbor", Body: "old"},
	}
	client := crudClient(t, store)

	resp, err := client.UpdateNode(context.Background(),
		connect.NewRequest(&managementv1.UpdateNodeRequest{
			Id: id.String(), Name: "Old Harbor", Body: "new prose", GmPrivate: true,
		}))
	if err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}
	node := resp.Msg.GetNode()
	if node.GetName() != "Old Harbor" || node.GetBody() != "new prose" || !node.GetGmPrivate() {
		t.Errorf("fields not mapped: %+v", node)
	}
	// node_type is immutable: the request carries none, and the stored Location type
	// survives the update.
	if node.GetNodeType() != managementv1.NodeType_NODE_TYPE_LOCATION {
		t.Errorf("node_type = %v, want the immutable LOCATION", node.GetNodeType())
	}
	if store.nodes[0].Name != "Old Harbor" || !store.nodes[0].GMPrivate {
		t.Errorf("storage not updated: %+v", store.nodes[0])
	}
}

func TestUpdateNode_TrimsName(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	store := newFakeStore()
	store.nodes = []storage.KGNode{{ID: id, Type: storage.KGNodeNote, Name: "old"}}
	client := crudClient(t, store)

	resp, err := client.UpdateNode(context.Background(),
		connect.NewRequest(&managementv1.UpdateNodeRequest{Id: id.String(), Name: "  Alice  "}))
	if err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}
	if store.nodes[0].Name != "Alice" {
		t.Errorf("stored name = %q, want trimmed %q", store.nodes[0].Name, "Alice")
	}
	if resp.Msg.GetNode().GetName() != "Alice" {
		t.Errorf("echoed name = %q, want trimmed", resp.Msg.GetNode().GetName())
	}
}

func TestUpdateNode_EmptyNameIsInvalidArgument(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	store := newFakeStore()
	store.nodes = []storage.KGNode{{ID: id, Type: storage.KGNodeNote, Name: "x"}}
	client := crudClient(t, store)

	_, err := client.UpdateNode(context.Background(),
		connect.NewRequest(&managementv1.UpdateNodeRequest{Id: id.String(), Name: "   "}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestUpdateNode_InvalidIdIsInvalidArgument(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	client := crudClient(t, store)

	_, err := client.UpdateNode(context.Background(),
		connect.NewRequest(&managementv1.UpdateNodeRequest{Id: "not-a-uuid", Name: "x"}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestUpdateNode_NotFoundIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore() // no nodes seeded
	client := crudClient(t, store)

	_, err := client.UpdateNode(context.Background(),
		connect.NewRequest(&managementv1.UpdateNodeRequest{Id: uuid.NewString(), Name: "x"}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

func TestUpdateNode_StorageErrorIsInternal(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.nodeUpdateErr = errAny
	client := crudClient(t, store)

	_, err := client.UpdateNode(context.Background(),
		connect.NewRequest(&managementv1.UpdateNodeRequest{Id: uuid.NewString(), Name: "x"}))
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", got)
	}
}

func TestDeleteNode_Deletes(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	store := newFakeStore()
	store.nodes = []storage.KGNode{{ID: id, Type: storage.KGNodeNote, Name: "gone soon"}}
	client := crudClient(t, store)

	if _, err := client.DeleteNode(context.Background(),
		connect.NewRequest(&managementv1.DeleteNodeRequest{Id: id.String()})); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	if len(store.nodes) != 0 {
		t.Errorf("node not removed from store: %+v", store.nodes)
	}
}

func TestDeleteNode_InvalidIdIsInvalidArgument(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	client := crudClient(t, store)

	_, err := client.DeleteNode(context.Background(),
		connect.NewRequest(&managementv1.DeleteNodeRequest{Id: "nope"}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestDeleteNode_NotFoundIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	client := crudClient(t, store)

	_, err := client.DeleteNode(context.Background(),
		connect.NewRequest(&managementv1.DeleteNodeRequest{Id: uuid.NewString()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

func TestDeleteNode_StorageErrorIsInternal(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.nodeDeleteErr = errAny
	client := crudClient(t, store)

	_, err := client.DeleteNode(context.Background(),
		connect.NewRequest(&managementv1.DeleteNodeRequest{Id: uuid.NewString()}))
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", got)
	}
}

// TestUpdateNode_ScopesToActiveCampaign is #342: the handler resolves the active
// campaign and passes its id down, so the store's UPDATE matches (id, campaign_id)
// and a cross-campaign write is refused server-side.
func TestUpdateNode_ScopesToActiveCampaign(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	store := newFakeStore()
	activeID := uuid.New()
	store.campaign = storage.Campaign{ID: activeID}
	store.nodes = []storage.KGNode{{ID: id, Type: storage.KGNodeNote, Name: "old"}}
	client := crudClient(t, store)

	if _, err := client.UpdateNode(context.Background(),
		connect.NewRequest(&managementv1.UpdateNodeRequest{Id: id.String(), Name: "New"})); err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}
	if store.updateNodeCampaign != activeID {
		t.Errorf("UpdateNode scoped to %s, want active %s", store.updateNodeCampaign, activeID)
	}
}

// TestDeleteNode_ScopesToActiveCampaign is #342: the delete is scoped to the
// resolved active campaign, so another campaign's Node is never removable.
func TestDeleteNode_ScopesToActiveCampaign(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	store := newFakeStore()
	activeID := uuid.New()
	store.campaign = storage.Campaign{ID: activeID}
	store.nodes = []storage.KGNode{{ID: id, Type: storage.KGNodeNote, Name: "gone soon"}}
	client := crudClient(t, store)

	if _, err := client.DeleteNode(context.Background(),
		connect.NewRequest(&managementv1.DeleteNodeRequest{Id: id.String()})); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	if store.deleteNodeCampaign != activeID {
		t.Errorf("DeleteNode scoped to %s, want active %s", store.deleteNodeCampaign, activeID)
	}
}

// TestDeleteNode_NoActiveCampaignIsNotFound is #342: without an active campaign the
// scoped delete cannot resolve an owner and returns CodeNotFound.
func TestDeleteNode_NoActiveCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campErr = storage.ErrNotFound
	client := crudClient(t, store)

	_, err := client.DeleteNode(context.Background(),
		connect.NewRequest(&managementv1.DeleteNodeRequest{Id: uuid.NewString()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

func TestSearchNodes_EmptyQueryIsInvalidArgument(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Name: "Lost Mine"}
	client := crudClient(t, store)

	for _, q := range []string{"", "   "} {
		_, err := client.SearchNodes(context.Background(),
			connect.NewRequest(&managementv1.SearchNodesRequest{Query: q}))
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Errorf("SearchNodes(%q) code = %v, want InvalidArgument", q, got)
		}
	}
	// An empty query must never reach storage.
	if store.searchCalls != 0 {
		t.Errorf("store.SearchNodes called %d times for empty queries, want 0", store.searchCalls)
	}
}

func TestSearchNodes_PreservesRankOrderAndIncludesPrivate(t *testing.T) {
	t.Parallel()
	campID := uuid.New()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: campID, Name: "Lost Mine"}
	// Storage owns the relevance ranking; the handler must preserve it 1:1 and must
	// NOT drop the gm_private match (GM-facing search).
	store.searchResults = []storage.KGNode{
		{ID: uuid.New(), CampaignID: campID, Type: storage.KGNodeCharacter, Name: "Dragon of the North"},
		{ID: uuid.New(), CampaignID: campID, Type: storage.KGNodePlotThread, Name: "The dragon's true name", GMPrivate: true},
		{ID: uuid.New(), CampaignID: campID, Type: storage.KGNodeNote, Name: "Rumor", Body: "a dragon"},
	}
	client := crudClient(t, store)

	resp, err := client.SearchNodes(context.Background(),
		connect.NewRequest(&managementv1.SearchNodesRequest{Query: "dragon"}))
	if err != nil {
		t.Fatalf("SearchNodes: %v", err)
	}
	nodes := resp.Msg.GetNodes()
	if len(nodes) != 3 {
		t.Fatalf("len = %d, want 3 (rank order preserved, private included)", len(nodes))
	}
	if nodes[0].GetName() != "Dragon of the North" || nodes[2].GetName() != "Rumor" {
		t.Errorf("rank order not preserved: %q … %q", nodes[0].GetName(), nodes[2].GetName())
	}
	if !nodes[1].GetGmPrivate() {
		t.Errorf("gm_private match dropped from GM-facing search: %+v", nodes[1])
	}
	// The handler forwards the raw query and the LIMIT-50 cap to storage.
	if store.searchQuery != "dragon" {
		t.Errorf("store saw query %q, want %q", store.searchQuery, "dragon")
	}
	if store.searchLimit != 50 {
		t.Errorf("store saw limit %d, want 50", store.searchLimit)
	}
}

func TestSearchNodes_NoCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campErr = storage.ErrNotFound
	client := crudClient(t, store)

	_, err := client.SearchNodes(context.Background(),
		connect.NewRequest(&managementv1.SearchNodesRequest{Query: "x"}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

func TestSearchNodes_StorageErrorIsInternal(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Name: "Lost Mine"}
	store.nodeSearchErr = errAny
	client := crudClient(t, store)

	_, err := client.SearchNodes(context.Background(),
		connect.NewRequest(&managementv1.SearchNodesRequest{Query: "x"}))
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", got)
	}
}

// TestCreateNodeHonorsDurableSelection is #222 for the KG wiki write: a new Node
// lands in the durable /glyphoxa use selection (D), not the most-recent default
// (N) — so the wiki edited on the Campaign screen and the durable selection stay
// coherent.
func TestCreateNodeHonorsDurableSelection(t *testing.T) {
	t.Parallel()
	durable := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Durable D"}
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer N"}
	store := newFakeStore()
	store.forUser = durable
	store.campaign = newer
	client := crudClientAs(t, store, storage.User{DiscordUserID: "999"}, nil)

	if _, err := client.CreateNode(context.Background(),
		connect.NewRequest(&managementv1.CreateNodeRequest{
			NodeType: managementv1.NodeType_NODE_TYPE_NOTE, Name: "A note",
		})); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if len(store.nodesCreated) != 1 {
		t.Fatalf("created %d nodes, want 1", len(store.nodesCreated))
	}
	if got := store.nodesCreated[0].CampaignID; got != durable.ID {
		t.Errorf("node landed in campaign %s, want the durable selection %s (not the newer %s)", got, durable.ID, newer.ID)
	}
}

// TestListNodesHonorsDurableSelection is #222 for the KG wiki read: ListNodes
// scopes to the durable /glyphoxa use selection (D), not the most-recent default
// (N).
func TestListNodesHonorsDurableSelection(t *testing.T) {
	t.Parallel()
	durable := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Durable D"}
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer N"}
	store := newFakeStore()
	store.forUser = durable
	store.campaign = newer
	client := crudClientAs(t, store, storage.User{DiscordUserID: "999"}, nil)

	if _, err := client.ListNodes(context.Background(),
		connect.NewRequest(&managementv1.ListNodesRequest{})); err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if store.listNodesCampaign != durable.ID {
		t.Errorf("ListNodes scoped to %s, want the durable selection %s (not the newer %s)",
			store.listNodesCampaign, durable.ID, newer.ID)
	}
}

// TestSearchNodesHonorsDurableSelection is #222 for the KG wiki search: SearchNodes
// scopes to the durable /glyphoxa use selection (D), not the most-recent default
// (N).
func TestSearchNodesHonorsDurableSelection(t *testing.T) {
	t.Parallel()
	durable := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Durable D"}
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer N"}
	store := newFakeStore()
	store.forUser = durable
	store.campaign = newer
	client := crudClientAs(t, store, storage.User{DiscordUserID: "999"}, nil)

	if _, err := client.SearchNodes(context.Background(),
		connect.NewRequest(&managementv1.SearchNodesRequest{Query: "vault"})); err != nil {
		t.Fatalf("SearchNodes: %v", err)
	}
	if store.searchNodesCampaign != durable.ID {
		t.Errorf("SearchNodes scoped to %s, want the durable selection %s (not the newer %s)",
			store.searchNodesCampaign, durable.ID, newer.ID)
	}
}

// TestCreateNodeHonorsLiveSession is #222 live-first for the KG wiki write: a new
// Node lands in the LIVE session's campaign (L), the SAME campaign the wiki list
// shows mid-session.
func TestCreateNodeHonorsLiveSession(t *testing.T) {
	t.Parallel()
	live := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Live L"}
	durable := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Durable D"}
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer N"}
	store := newFakeStore()
	store.forUser = durable
	store.campaign = newer
	store.campaignsByID = map[uuid.UUID]storage.Campaign{live.ID: live}
	client := crudClientAs(t, store, storage.User{DiscordUserID: "999"}, liveMgr(live.ID))

	if _, err := client.CreateNode(context.Background(),
		connect.NewRequest(&managementv1.CreateNodeRequest{
			NodeType: managementv1.NodeType_NODE_TYPE_NOTE, Name: "A note",
		})); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if len(store.nodesCreated) != 1 {
		t.Fatalf("created %d nodes, want 1", len(store.nodesCreated))
	}
	if got := store.nodesCreated[0].CampaignID; got != live.ID {
		t.Errorf("node landed in campaign %s, want the LIVE session campaign %s (not durable %s / newer %s)",
			got, live.ID, durable.ID, newer.ID)
	}
}

// TestListNodesHonorsLiveSession is #222 live-first for the KG wiki read: ListNodes
// scopes to the LIVE session's campaign (L).
func TestListNodesHonorsLiveSession(t *testing.T) {
	t.Parallel()
	live := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Live L"}
	durable := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Durable D"}
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer N"}
	store := newFakeStore()
	store.forUser = durable
	store.campaign = newer
	store.campaignsByID = map[uuid.UUID]storage.Campaign{live.ID: live}
	client := crudClientAs(t, store, storage.User{DiscordUserID: "999"}, liveMgr(live.ID))

	if _, err := client.ListNodes(context.Background(),
		connect.NewRequest(&managementv1.ListNodesRequest{})); err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if store.listNodesCampaign != live.ID {
		t.Errorf("ListNodes scoped to %s, want the LIVE session campaign %s (not durable %s / newer %s)",
			store.listNodesCampaign, live.ID, durable.ID, newer.ID)
	}
}

// TestSearchNodesHonorsLiveSession is #222 live-first for the KG wiki search:
// SearchNodes scopes to the LIVE session's campaign (L).
func TestSearchNodesHonorsLiveSession(t *testing.T) {
	t.Parallel()
	live := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Live L"}
	durable := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Durable D"}
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer N"}
	store := newFakeStore()
	store.forUser = durable
	store.campaign = newer
	store.campaignsByID = map[uuid.UUID]storage.Campaign{live.ID: live}
	client := crudClientAs(t, store, storage.User{DiscordUserID: "999"}, liveMgr(live.ID))

	if _, err := client.SearchNodes(context.Background(),
		connect.NewRequest(&managementv1.SearchNodesRequest{Query: "vault"})); err != nil {
		t.Fatalf("SearchNodes: %v", err)
	}
	if store.searchNodesCampaign != live.ID {
		t.Errorf("SearchNodes scoped to %s, want the LIVE session campaign %s (not durable %s / newer %s)",
			store.searchNodesCampaign, live.ID, durable.ID, newer.ID)
	}
}
