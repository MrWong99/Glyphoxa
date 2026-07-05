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
