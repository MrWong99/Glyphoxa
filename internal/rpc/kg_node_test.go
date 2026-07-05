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
