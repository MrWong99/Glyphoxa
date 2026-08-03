package rpc

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// kgGraph is the whole-graph read module (#534, ADR-0008 amendment): the single
// call the Campaign screen's Graph view renders from. It is its own feature slice
// (#445) because it spans BOTH the Node and Edge tables while needing neither
// CRUD surface — a graph read is not an editing authority.
type kgGraph struct {
	store  kgGraphStore
	active *activeCampaignSource
}

// kgGraphStore is the narrow whole-campaign read surface the graph needs;
// *storage.Store satisfies it. Both reads are campaign-scoped and read-only —
// there is deliberately no write here, because the graph edits through the
// EXISTING node/edge RPCs rather than introducing a second write path.
type kgGraphStore interface {
	ListGraphNodes(ctx context.Context, campaignID uuid.UUID) ([]storage.KGGraphNode, error)
	ListEdges(ctx context.Context, campaignID uuid.UUID) ([]storage.KGEdge, error)
}

// GetKnowledgeGraph returns the active campaign's whole Knowledge Graph — every
// Node and every Edge — in one call (#534). Two indexed reads: 300 per-node
// ListNodeEdges round trips is not a plan, and a campaign is hundreds of Nodes,
// so there is no pagination and no server-side layout.
//
// It is GM-facing: gm_private Nodes are INCLUDED (the graph is the GM's map of
// their own world) and the client chooses how to draw them. Prompt assembly cannot
// reach this path — it holds only PromptKGView (#450).
//
// No campaign is CodeNotFound; a storage failure is CodeInternal.
func (s *kgGraph) GetKnowledgeGraph(
	ctx context.Context,
	_ *connect.Request[managementv1.GetKnowledgeGraphRequest],
) (*connect.Response[managementv1.GetKnowledgeGraphResponse], error) {
	c, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("GetKnowledgeGraph: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	nodes, err := s.store.ListGraphNodes(ctx, c.ID)
	if err != nil {
		slog.Default().Error("GetKnowledgeGraph: list nodes failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	edges, err := s.store.ListEdges(ctx, c.ID)
	if err != nil {
		slog.Default().Error("GetKnowledgeGraph: list edges failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	out := &managementv1.GetKnowledgeGraphResponse{
		Nodes: make([]*managementv1.GraphNode, 0, len(nodes)),
		Edges: make([]*managementv1.GraphEdge, 0, len(edges)),
	}
	for _, n := range nodes {
		gn := &managementv1.GraphNode{
			Id:          n.ID.String(),
			NodeType:    toProtoNodeType(n.Type),
			Name:        n.Name,
			GmPrivate:   n.GMPrivate,
			BodyLen:     int32(n.BodyLen),     //nolint:gosec // a body length cannot exceed int32 in practice
			AspectCount: int32(n.AspectCount), //nolint:gosec // bounded by kgvocab.MaxAspectsPerNode
		}
		if n.AgentID.Valid {
			gn.AgentId = n.AgentID.UUID.String()
		}
		out.Nodes = append(out.Nodes, gn)
	}
	for _, e := range edges {
		out.Edges = append(out.Edges, &managementv1.GraphEdge{
			Id:         e.ID.String(),
			FromNodeId: e.FromNodeID.String(),
			ToNodeId:   e.ToNodeID.String(),
			EdgeType:   toProtoEdgeType(e.Type),
		})
	}
	return connect.NewResponse(out), nil
}
