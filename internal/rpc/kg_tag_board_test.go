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

func organizeClient(t *testing.T, store *fakeOrganizeStore) managementv1connect.CampaignServiceClient {
	t.Helper()
	return campaignClient(t, rpc.CampaignStores{Active: store, Organize: store})
}

// TestTagsRoundTrip pins the tag surface: one campaign-wide read the client
// indexes both ways, replace-in-full saves, and a campaign-wide rename.
func TestTagsRoundTrip(t *testing.T) {
	t.Parallel()
	store := newFakeOrganizeStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	nodeID := uuid.New()
	client := organizeClient(t, store)
	ctx := context.Background()

	if _, err := client.SetNodeTags(ctx, connect.NewRequest(&managementv1.SetNodeTagsRequest{
		NodeId: nodeID.String(), Tags: []string{"seafaring", "act two"},
	})); err != nil {
		t.Fatalf("SetNodeTags: %v", err)
	}
	if store.tagsCampaign != store.campaign.ID {
		t.Errorf("write scoped to %s, want the active campaign", store.tagsCampaign)
	}

	resp, err := client.GetCampaignTags(ctx, connect.NewRequest(&managementv1.GetCampaignTagsRequest{}))
	if err != nil {
		t.Fatalf("GetCampaignTags: %v", err)
	}
	if len(resp.Msg.GetEntries()) != 2 {
		t.Errorf("got %d pairs, want 2", len(resp.Msg.GetEntries()))
	}

	if _, err := client.RenameTag(ctx, connect.NewRequest(&managementv1.RenameTagRequest{
		From: "act two", To: "act three",
	})); err != nil {
		t.Fatalf("RenameTag: %v", err)
	}
	if store.renamedFrom != "act two" || store.renamedTo != "act three" {
		t.Errorf("rename = %q→%q", store.renamedFrom, store.renamedTo)
	}
}

func TestTagsValidation(t *testing.T) {
	t.Parallel()

	t.Run("bad node id is InvalidArgument", func(t *testing.T) {
		store := newFakeOrganizeStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		_, err := organizeClient(t, store).SetNodeTags(context.Background(),
			connect.NewRequest(&managementv1.SetNodeTagsRequest{NodeId: "nope"}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("code = %v, want InvalidArgument", connect.CodeOf(err))
		}
	})

	t.Run("an invalid tag surfaces as InvalidArgument", func(t *testing.T) {
		store := newFakeOrganizeStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		store.tagErr = storage.ErrInvalidTag
		_, err := organizeClient(t, store).SetNodeTags(context.Background(),
			connect.NewRequest(&managementv1.SetNodeTagsRequest{NodeId: uuid.New().String()}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("code = %v, want InvalidArgument", connect.CodeOf(err))
		}
	})

	t.Run("a blank rename is refused", func(t *testing.T) {
		store := newFakeOrganizeStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		_, err := organizeClient(t, store).RenameTag(context.Background(),
			connect.NewRequest(&managementv1.RenameTagRequest{From: "  ", To: "x"}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("code = %v, want InvalidArgument", connect.CodeOf(err))
		}
	})
}

// TestBoardsRoundTrip pins that a board is a named ordered shortlist, and that
// updating it replaces its entries in the authored order.
func TestBoardsRoundTrip(t *testing.T) {
	t.Parallel()
	store := newFakeOrganizeStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	client := organizeClient(t, store)
	ctx := context.Background()

	created, err := client.CreateBoard(ctx, connect.NewRequest(&managementv1.CreateBoardRequest{
		Name: "  tonight: the harbour heist  ",
	}))
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if created.Msg.GetBoard().GetName() != "tonight: the harbour heist" {
		t.Errorf("name = %q, want it trimmed", created.Msg.GetBoard().GetName())
	}
	id := created.Msg.GetBoard().GetId()

	a, b := uuid.New().String(), uuid.New().String()
	updated, err := client.UpdateBoard(ctx, connect.NewRequest(&managementv1.UpdateBoardRequest{
		Id: id, Name: "tonight", NodeIds: []string{b, a},
	}))
	if err != nil {
		t.Fatalf("UpdateBoard: %v", err)
	}
	if got := updated.Msg.GetBoard().GetNodeIds(); len(got) != 2 || got[0] != b {
		t.Errorf("board = %v, want the authored order preserved", got)
	}

	if _, err := client.DeleteBoard(ctx, connect.NewRequest(&managementv1.DeleteBoardRequest{Id: id})); err != nil {
		t.Fatalf("DeleteBoard: %v", err)
	}
	list, err := client.ListBoards(ctx, connect.NewRequest(&managementv1.ListBoardsRequest{}))
	if err != nil {
		t.Fatalf("ListBoards: %v", err)
	}
	if len(list.Msg.GetBoards()) != 0 {
		t.Errorf("boards after delete = %v", list.Msg.GetBoards())
	}
}

func TestBoardsValidation(t *testing.T) {
	t.Parallel()
	store := newFakeOrganizeStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	client := organizeClient(t, store)

	if _, err := client.CreateBoard(context.Background(),
		connect.NewRequest(&managementv1.CreateBoardRequest{Name: "  "})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("blank name: code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	if _, err := client.UpdateBoard(context.Background(),
		connect.NewRequest(&managementv1.UpdateBoardRequest{
			Id: uuid.New().String(), Name: "x", NodeIds: []string{"not-a-uuid"},
		})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("bad entry id: code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	if _, err := client.DeleteBoard(context.Background(),
		connect.NewRequest(&managementv1.DeleteBoardRequest{Id: uuid.New().String()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("missing board: code = %v, want NotFound", connect.CodeOf(err))
	}
}
