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
	// SimilarNodePairs backs the GM-initiated probable-duplicate check (#536);
	// CountUnembeddedNodesInCampaign says how much of the wiki that check could not
	// see yet.
	SimilarNodePairs(ctx context.Context, campaignID uuid.UUID, minSimilarity float64, limit int) ([]storage.KGNodePair, error)
	CountUnembeddedNodesInCampaign(ctx context.Context, campaignID uuid.UUID) (int, error)
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
	c, err := s.active.campaignFor(ctx, "GetKnowledgeGraph")
	if err != nil {
		return nil, err
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
			Id:                n.ID.String(),
			NodeType:          toProtoNodeType(n.Type),
			Name:              n.Name,
			GmPrivate:         n.GMPrivate,
			BodyLen:           int32(n.BodyLen),           //nolint:gosec // a body length cannot exceed int32 in practice
			AspectCount:       int32(n.AspectCount),       //nolint:gosec // bounded by kgvocab.MaxAspectsPerNode
			PublicAspectCount: int32(n.PublicAspectCount), //nolint:gosec // bounded by kgvocab.MaxAspectsPerNode
		}
		if n.AgentID.Valid {
			gn.AgentId = n.AgentID.UUID.String()
		}
		out.Nodes = append(out.Nodes, gn)
	}
	for _, e := range edges {
		out.Edges = append(out.Edges, &managementv1.GraphEdge{
			Id:          e.ID.String(),
			FromNodeId:  e.FromNodeID.String(),
			ToNodeId:    e.ToNodeID.String(),
			EdgeType:    toProtoEdgeType(e.Type),
			Disposition: int32(e.Disposition), //nolint:gosec // CHECK-constrained to -2..+2
			Note:        e.Note,
		})
	}
	return connect.NewResponse(out), nil
}

// Duplicate-check policy for the single-operator web tier (ADR-0039): the client
// sends no threshold and no limit.
const (
	// duplicateSimilarityFloor is deliberately high. A hint the GM must read and act
	// on is only useful if it is usually right; a low floor turns the panel into a
	// wall of coincidences and trains the GM to ignore it.
	duplicateSimilarityFloor = 0.92
	// duplicatePairLimit caps the list. Past this many, the wiki has a systemic
	// problem the panel cannot express one row at a time.
	duplicatePairLimit = 25
)

// FindDuplicateEntries returns the active campaign's closest Node pairs by
// embedding similarity — the world health panel's probable-duplicate check (#536).
//
// It is GM-INITIATED by design: an exact pairwise scan is cheap at campaign scale
// but has no business firing on every Knowledge-tab render, and the ADR-0011
// embedding path is not something to sweep speculatively.
//
// Per ADR-0052 this is a HINT and nothing more. Nothing in this path merges,
// rewrites or deletes anything: similarity is not a semantic judgment, and a wrong
// merge corrupts canon invisibly.
func (s *kgGraph) FindDuplicateEntries(
	ctx context.Context,
	_ *connect.Request[managementv1.FindDuplicateEntriesRequest],
) (*connect.Response[managementv1.FindDuplicateEntriesResponse], error) {
	c, err := s.active.campaignFor(ctx, "FindDuplicateEntries")
	if err != nil {
		return nil, err
	}

	pairs, err := s.store.SimilarNodePairs(ctx, c.ID, duplicateSimilarityFloor, duplicatePairLimit)
	if err != nil {
		slog.Default().Error("FindDuplicateEntries: pair scan failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	// Nodes still awaiting an embedding are invisible to the scan. Reporting the
	// count keeps "no duplicates found" from implying a clean bill of health the
	// check could not actually give.
	unembedded, err := s.store.CountUnembeddedNodesInCampaign(ctx, c.ID)
	if err != nil {
		slog.Default().Error("FindDuplicateEntries: unembedded count failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	out := &managementv1.FindDuplicateEntriesResponse{
		Pairs:      make([]*managementv1.DuplicateEntryPair, 0, len(pairs)),
		Unembedded: int32(unembedded), //nolint:gosec // bounded by campaign size
	}
	for _, p := range pairs {
		out.Pairs = append(out.Pairs, &managementv1.DuplicateEntryPair{
			AId: p.AID.String(), AName: p.AName,
			BId: p.BID.String(), BName: p.BName,
			Similarity: p.Similarity,
		})
	}
	return connect.NewResponse(out), nil
}
