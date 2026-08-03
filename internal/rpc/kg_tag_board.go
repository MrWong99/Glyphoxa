package rpc

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// kgOrganize is the tags-and-boards feature module (#543): free-form GM labels
// alongside the closed 7-type vocabulary, and saved session prep boards.
//
// Everything here is GM ORGANIZATION ONLY. Nothing in this module is read by
// prompt assembly, and nothing it writes affects AgentNodeFacts — which is what
// keeps the fact budget untouched and stops tags quietly becoming a second,
// unvalidated type system inside NPC context.
type kgOrganize struct {
	store  kgOrganizeStore
	active *activeCampaignSource
}

// kgOrganizeStore is the narrow surface the module needs; *storage.Store
// satisfies it. Every method is campaign-scoped (#342).
type kgOrganizeStore interface {
	CampaignTags(ctx context.Context, campaignID uuid.UUID) ([]storage.TaggedNode, error)
	SetNodeTags(ctx context.Context, campaignID, nodeID uuid.UUID, tags []string) error
	NodeTags(ctx context.Context, campaignID, nodeID uuid.UUID) ([]string, error)
	RenameTag(ctx context.Context, campaignID uuid.UUID, from, to string) error

	ListBoards(ctx context.Context, campaignID uuid.UUID) ([]storage.KGBoard, error)
	CreateBoard(ctx context.Context, campaignID uuid.UUID, name string) (storage.KGBoard, error)
	SetBoardNodes(ctx context.Context, campaignID, boardID uuid.UUID, nodeIDs []uuid.UUID) error
	RenameBoard(ctx context.Context, campaignID, id uuid.UUID, name string) error
	DeleteBoard(ctx context.Context, campaignID, id uuid.UUID) error
}

// GetCampaignTags returns every (entry, tag) pair — one read the client indexes
// both ways, so tagging a hundred entries is still one round trip.
func (s *kgOrganize) GetCampaignTags(
	ctx context.Context,
	_ *connect.Request[managementv1.GetCampaignTagsRequest],
) (*connect.Response[managementv1.GetCampaignTagsResponse], error) {
	c, err := s.campaign(ctx, "GetCampaignTags")
	if err != nil {
		return nil, err
	}
	pairs, err := s.store.CampaignTags(ctx, c.ID)
	if err != nil {
		slog.Default().Error("GetCampaignTags: store read failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	out := &managementv1.GetCampaignTagsResponse{Entries: make([]*managementv1.TaggedEntry, 0, len(pairs))}
	for _, p := range pairs {
		out.Entries = append(out.Entries, &managementv1.TaggedEntry{
			NodeId: p.NodeID.String(), Tag: p.Tag,
		})
	}
	return connect.NewResponse(out), nil
}

// SetNodeTags replaces one entry's tags. Blank tags are dropped and
// case-insensitive duplicates collapse — the chip editor keeps an empty field
// around, and that convenience must not become a save error.
func (s *kgOrganize) SetNodeTags(
	ctx context.Context,
	req *connect.Request[managementv1.SetNodeTagsRequest],
) (*connect.Response[managementv1.SetNodeTagsResponse], error) {
	nodeID, err := uuid.Parse(req.Msg.GetNodeId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid entry id"))
	}
	c, err := s.campaign(ctx, "SetNodeTags")
	if err != nil {
		return nil, err
	}

	if err := s.store.SetNodeTags(ctx, c.ID, nodeID, req.Msg.GetTags()); err != nil {
		if errors.Is(err, storage.ErrInvalidTag) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		slog.Default().Error("SetNodeTags: store write failed", "node_id", nodeID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	saved, err := s.store.NodeTags(ctx, c.ID, nodeID)
	if err != nil {
		slog.Default().Error("SetNodeTags: read back failed", "node_id", nodeID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.SetNodeTagsResponse{Tags: saved}), nil
}

// RenameTag renames a tag across the whole Campaign. The AC offers exactly two
// options — rename everywhere, or do not offer it — and half-renamed vocabularies
// are the reason.
func (s *kgOrganize) RenameTag(
	ctx context.Context,
	req *connect.Request[managementv1.RenameTagRequest],
) (*connect.Response[managementv1.RenameTagResponse], error) {
	from, to := strings.TrimSpace(req.Msg.GetFrom()), strings.TrimSpace(req.Msg.GetTo())
	if from == "" || to == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("both tag names are required"))
	}
	c, err := s.campaign(ctx, "RenameTag")
	if err != nil {
		return nil, err
	}
	if err := s.store.RenameTag(ctx, c.ID, from, to); err != nil {
		if errors.Is(err, storage.ErrInvalidTag) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		slog.Default().Error("RenameTag: store write failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.RenameTagResponse{}), nil
}

// ListBoards returns the Campaign's prep boards with their entries.
func (s *kgOrganize) ListBoards(
	ctx context.Context,
	_ *connect.Request[managementv1.ListBoardsRequest],
) (*connect.Response[managementv1.ListBoardsResponse], error) {
	c, err := s.campaign(ctx, "ListBoards")
	if err != nil {
		return nil, err
	}
	boards, err := s.store.ListBoards(ctx, c.ID)
	if err != nil {
		slog.Default().Error("ListBoards: store read failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	out := &managementv1.ListBoardsResponse{Boards: make([]*managementv1.Board, 0, len(boards))}
	for _, b := range boards {
		out.Boards = append(out.Boards, toProtoBoard(b))
	}
	return connect.NewResponse(out), nil
}

// CreateBoard makes an empty prep board.
func (s *kgOrganize) CreateBoard(
	ctx context.Context,
	req *connect.Request[managementv1.CreateBoardRequest],
) (*connect.Response[managementv1.CreateBoardResponse], error) {
	name := strings.TrimSpace(req.Msg.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))
	}
	c, err := s.campaign(ctx, "CreateBoard")
	if err != nil {
		return nil, err
	}
	b, err := s.store.CreateBoard(ctx, c.ID, name)
	if err != nil {
		slog.Default().Error("CreateBoard: store write failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.CreateBoardResponse{Board: toProtoBoard(b)}), nil
}

// UpdateBoard renames a board and replaces its entries in order. Both in one call
// because they are one act to the GM: shaping tonight's shortlist.
func (s *kgOrganize) UpdateBoard(
	ctx context.Context,
	req *connect.Request[managementv1.UpdateBoardRequest],
) (*connect.Response[managementv1.UpdateBoardResponse], error) {
	m := req.Msg
	id, err := uuid.Parse(m.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid board id"))
	}
	name := strings.TrimSpace(m.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))
	}
	nodeIDs := make([]uuid.UUID, 0, len(m.GetNodeIds()))
	for _, raw := range m.GetNodeIds() {
		nid, perr := uuid.Parse(raw)
		if perr != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid entry id"))
		}
		nodeIDs = append(nodeIDs, nid)
	}

	c, err := s.campaign(ctx, "UpdateBoard")
	if err != nil {
		return nil, err
	}
	if err := s.store.RenameBoard(ctx, c.ID, id, name); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("board not found"))
		}
		slog.Default().Error("UpdateBoard: rename failed", "board_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if err := s.store.SetBoardNodes(ctx, c.ID, id, nodeIDs); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("board not found"))
		}
		slog.Default().Error("UpdateBoard: set entries failed", "board_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	boards, err := s.store.ListBoards(ctx, c.ID)
	if err != nil {
		slog.Default().Error("UpdateBoard: read back failed", "board_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	for _, b := range boards {
		if b.ID == id {
			return connect.NewResponse(&managementv1.UpdateBoardResponse{Board: toProtoBoard(b)}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, errors.New("board not found"))
}

// DeleteBoard removes a board. Its entries are untouched — a board is a
// shortlist, not ownership.
func (s *kgOrganize) DeleteBoard(
	ctx context.Context,
	req *connect.Request[managementv1.DeleteBoardRequest],
) (*connect.Response[managementv1.DeleteBoardResponse], error) {
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid board id"))
	}
	c, err := s.campaign(ctx, "DeleteBoard")
	if err != nil {
		return nil, err
	}
	switch err := s.store.DeleteBoard(ctx, c.ID, id); {
	case err == nil:
		return connect.NewResponse(&managementv1.DeleteBoardResponse{}), nil
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("board not found"))
	default:
		slog.Default().Error("DeleteBoard: store delete failed", "board_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
}

func (s *kgOrganize) campaign(ctx context.Context, op string) (storage.Campaign, error) {
	c, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return storage.Campaign{}, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error(op+": get active campaign failed", "err", err)
		return storage.Campaign{}, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return c, nil
}

func toProtoBoard(b storage.KGBoard) *managementv1.Board {
	pb := &managementv1.Board{Id: b.ID.String(), Name: b.Name}
	for _, id := range b.NodeIDs {
		pb.NodeIds = append(pb.NodeIds, id.String())
	}
	return pb
}
