package rpc

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// The Appearances read (#545, ADR-0008 amendment): where an entry was mentioned
// in play.
//
// Note the shape of what is NOT here. There is no write RPC — appearances are
// derived by the end-of-session job, never authored — and this module is not on
// PromptKGView, so no prompt-assembly path can reach it. An appearance is
// retrieval, not world truth; making it an Edge would have put "the party talked
// about the ogre" into an NPC's Hot Context as a fact about the ogre.

// kgAppearances is the Appearances feature module.
type kgAppearances struct {
	store  kgAppearanceStore
	active *activeCampaignSource
}

// kgAppearanceStore is the narrow read surface; *storage.Store satisfies it.
// Read-only by construction: there is no write method to forget to guard.
type kgAppearanceStore interface {
	ListNodeAppearances(ctx context.Context, campaignID, nodeID uuid.UUID, limit int) ([]storage.AppearanceHit, error)
}

// ListNodeAppearances returns an entry's most recent mentions.
//
// gm_private entries are NOT filtered: this is the operator's own retrieval over
// their own campaign (ADR-0039/0041), and hiding a GM's own secret from their own
// search would make the index lie about their world. Nothing here is player- or
// prompt-facing.
func (s *kgAppearances) ListNodeAppearances(
	ctx context.Context,
	req *connect.Request[managementv1.ListNodeAppearancesRequest],
) (*connect.Response[managementv1.ListNodeAppearancesResponse], error) {
	nodeID, err := uuid.Parse(req.Msg.GetNodeId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid entry id"))
	}
	c, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("ListNodeAppearances: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	hits, err := s.store.ListNodeAppearances(ctx, c.ID, nodeID, storage.MaxAppearances)
	if err != nil {
		slog.Default().Error("ListNodeAppearances: store read failed", "node_id", nodeID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	out := &managementv1.ListNodeAppearancesResponse{
		Appearances: make([]*managementv1.NodeAppearance, 0, len(hits)),
	}
	for _, h := range hits {
		out.Appearances = append(out.Appearances, &managementv1.NodeAppearance{
			VoiceSessionId:   h.VoiceSessionID.String(),
			LineId:           h.LineID,
			At:               timestamppb.New(h.At),
			Who:              h.Who,
			Kind:             h.Kind,
			Text:             h.Text,
			SessionStartedAt: timestamppb.New(h.SessionStartedAt),
		})
	}
	return connect.NewResponse(out), nil
}
