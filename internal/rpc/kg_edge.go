package rpc

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Knowledge Graph Edge handlers (#132, ADR-0008 v1.0 + amendment) on
// CampaignServer: create/delete typed directional Edges, list a Node's incident
// Edges split by direction, and link/unlink an NPC Node's "voiced by" Agent. The
// active campaign is resolved server-side (single operator, ADR-0039).

// CreateEdge adds a typed Edge between two same-campaign Nodes. An UNSPECIFIED
// edge_type or an unparsable endpoint id is CodeInvalidArgument; an invalid
// (type, from, to) combination or a self-edge is CodeInvalidArgument; a duplicate
// is CodeAlreadyExists; a missing OR cross-campaign endpoint is CodeNotFound.
func (s *CampaignServer) CreateEdge(
	ctx context.Context,
	req *connect.Request[managementv1.CreateEdgeRequest],
) (*connect.Response[managementv1.CreateEdgeResponse], error) {
	m := req.Msg
	from, err := uuid.Parse(m.GetFromNodeId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid from node id"))
	}
	to, err := uuid.Parse(m.GetToNodeId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid to node id"))
	}
	edgeType, ok := toStorageEdgeType(m.GetEdgeType())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("edge type must be specified"))
	}

	c, err := s.activeCampaign(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("CreateEdge: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	created, err := s.store.CreateEdge(ctx, storage.NewKGEdge{
		CampaignID: c.ID, FromNodeID: from, ToNodeID: to, Type: edgeType,
	})
	switch {
	case err == nil:
		return connect.NewResponse(&managementv1.CreateEdgeResponse{Edge: toProtoEdge(created)}), nil
	case errors.Is(err, storage.ErrInvalidEdge):
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid edge"))
	case errors.Is(err, storage.ErrConflict):
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("edge already exists"))
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("edge endpoint not found"))
	default:
		slog.Default().Error("CreateEdge: store create failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
}

// DeleteEdge removes a typed Edge by id. An unparsable id is CodeInvalidArgument;
// a missing id is CodeNotFound.
func (s *CampaignServer) DeleteEdge(
	ctx context.Context,
	req *connect.Request[managementv1.DeleteEdgeRequest],
) (*connect.Response[managementv1.DeleteEdgeResponse], error) {
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid edge id"))
	}
	// Resolve the active campaign and scope the delete to it (#342): the store's
	// DELETE matches (id, campaign_id), so an Edge in another campaign is never
	// removable through this session — it reads back as CodeNotFound.
	c, err := s.activeCampaign(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("DeleteEdge: get active campaign failed", "edge_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	switch err := s.store.DeleteEdge(ctx, c.ID, id); {
	case err == nil:
		return connect.NewResponse(&managementv1.DeleteEdgeResponse{}), nil
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("edge not found"))
	default:
		slog.Default().Error("DeleteEdge: store delete failed", "edge_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
}

// ListNodeEdges returns a Node's incident Edges split into outgoing and incoming
// lists, each with joined endpoint name/type. An unparsable id is
// CodeInvalidArgument; a storage failure is CodeInternal.
func (s *CampaignServer) ListNodeEdges(
	ctx context.Context,
	req *connect.Request[managementv1.ListNodeEdgesRequest],
) (*connect.Response[managementv1.ListNodeEdgesResponse], error) {
	nodeID, err := uuid.Parse(req.Msg.GetNodeId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid node id"))
	}
	outgoing, incoming, err := s.store.NodeEdges(ctx, nodeID)
	if err != nil {
		slog.Default().Error("ListNodeEdges: store read failed", "node_id", nodeID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.ListNodeEdgesResponse{
		Outgoing: toProtoEdges(outgoing),
		Incoming: toProtoEdges(incoming),
	}), nil
}

// SetNodeAgent links or unlinks a Node's "voiced by" Agent. An empty agent_id
// unlinks. An unparsable node/agent id is CodeInvalidArgument; linking a non-NPC
// Node is CodeInvalidArgument; an Agent already voicing another Node is
// CodeAlreadyExists; a missing OR cross-campaign Node/Agent is CodeNotFound.
func (s *CampaignServer) SetNodeAgent(
	ctx context.Context,
	req *connect.Request[managementv1.SetNodeAgentRequest],
) (*connect.Response[managementv1.SetNodeAgentResponse], error) {
	m := req.Msg
	nodeID, err := uuid.Parse(m.GetNodeId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid node id"))
	}

	var agentID uuid.NullUUID
	if strings.TrimSpace(m.GetAgentId()) != "" {
		parsed, err := uuid.Parse(m.GetAgentId())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid agent id"))
		}
		agentID = uuid.NullUUID{UUID: parsed, Valid: true}
	}

	// Resolve the active campaign and scope both the link and the unlink to it
	// (#342): the store matches the Node's campaign_id against the active campaign,
	// so another campaign's Node is never re-voiced nor unlinked through this
	// session — and a link's Agent must also belong to the active campaign. A
	// cross-campaign Node/Agent reads back as CodeNotFound.
	c, err := s.activeCampaign(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("SetNodeAgent: get active campaign failed", "node_id", nodeID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	updated, err := s.store.SetNodeAgent(ctx, c.ID, nodeID, agentID)
	switch {
	case err == nil:
		return connect.NewResponse(&managementv1.SetNodeAgentResponse{Node: toProtoNode(updated)}), nil
	case errors.Is(err, storage.ErrInvalidEdge):
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("agent link is only valid on an NPC node"))
	case errors.Is(err, storage.ErrConflict):
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("agent already linked to another node"))
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node or agent not found"))
	default:
		slog.Default().Error("SetNodeAgent: store update failed", "node_id", nodeID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
}

// toProtoEdges maps a slice of joined Edges onto the wire type.
func toProtoEdges(edges []storage.KGEdgeWithNodes) []*managementv1.Edge {
	out := make([]*managementv1.Edge, 0, len(edges))
	for _, e := range edges {
		out = append(out, toProtoEdgeWithNodes(e))
	}
	return out
}

// toProtoEdge maps a bare storage.KGEdge onto its wire representation (no joined
// endpoint fields — the create response's endpoints are filled by the UI's
// follow-up ListNodeEdges refetch).
func toProtoEdge(e storage.KGEdge) *managementv1.Edge {
	return &managementv1.Edge{
		Id:         e.ID.String(),
		FromNodeId: e.FromNodeID.String(),
		ToNodeId:   e.ToNodeID.String(),
		EdgeType:   toProtoEdgeType(e.Type),
		CreatedAt:  timestamppb.New(e.CreatedAt),
	}
}

// toProtoEdgeWithNodes maps a joined Edge onto the wire type, including the
// server-joined endpoint name/type fields the UI renders without an N+1.
func toProtoEdgeWithNodes(e storage.KGEdgeWithNodes) *managementv1.Edge {
	pe := toProtoEdge(e.KGEdge)
	pe.FromNodeName = e.FromName
	pe.FromNodeType = toProtoNodeType(e.FromType)
	pe.ToNodeName = e.ToName
	pe.ToNodeType = toProtoNodeType(e.ToType)
	return pe
}

// toStorageEdgeType maps a wire EdgeType onto the storage enum. UNSPECIFIED (and
// any unknown) returns ok=false so the handler rejects it with CodeInvalidArgument.
func toStorageEdgeType(t managementv1.EdgeType) (storage.KGEdgeType, bool) {
	switch t {
	case managementv1.EdgeType_EDGE_TYPE_RESIDES_IN:
		return storage.KGEdgeResidesIn, true
	case managementv1.EdgeType_EDGE_TYPE_MEMBER_OF:
		return storage.KGEdgeMemberOf, true
	case managementv1.EdgeType_EDGE_TYPE_OWNS:
		return storage.KGEdgeOwns, true
	case managementv1.EdgeType_EDGE_TYPE_KNOWS:
		return storage.KGEdgeKnows, true
	case managementv1.EdgeType_EDGE_TYPE_ENEMY_OF:
		return storage.KGEdgeEnemyOf, true
	case managementv1.EdgeType_EDGE_TYPE_ALLY_OF:
		return storage.KGEdgeAllyOf, true
	case managementv1.EdgeType_EDGE_TYPE_PARENT_OF:
		return storage.KGEdgeParentOf, true
	case managementv1.EdgeType_EDGE_TYPE_PARTICIPATED_IN:
		return storage.KGEdgeParticipatedIn, true
	case managementv1.EdgeType_EDGE_TYPE_MENTIONED_IN:
		return storage.KGEdgeMentionedIn, true
	default:
		return "", false
	}
}

// toProtoEdgeType maps the storage enum back onto the wire EdgeType. An unknown
// stored value maps to UNSPECIFIED (defensive; the DB enum keeps this exhaustive).
func toProtoEdgeType(t storage.KGEdgeType) managementv1.EdgeType {
	switch t {
	case storage.KGEdgeResidesIn:
		return managementv1.EdgeType_EDGE_TYPE_RESIDES_IN
	case storage.KGEdgeMemberOf:
		return managementv1.EdgeType_EDGE_TYPE_MEMBER_OF
	case storage.KGEdgeOwns:
		return managementv1.EdgeType_EDGE_TYPE_OWNS
	case storage.KGEdgeKnows:
		return managementv1.EdgeType_EDGE_TYPE_KNOWS
	case storage.KGEdgeEnemyOf:
		return managementv1.EdgeType_EDGE_TYPE_ENEMY_OF
	case storage.KGEdgeAllyOf:
		return managementv1.EdgeType_EDGE_TYPE_ALLY_OF
	case storage.KGEdgeParentOf:
		return managementv1.EdgeType_EDGE_TYPE_PARENT_OF
	case storage.KGEdgeParticipatedIn:
		return managementv1.EdgeType_EDGE_TYPE_PARTICIPATED_IN
	case storage.KGEdgeMentionedIn:
		return managementv1.EdgeType_EDGE_TYPE_MENTIONED_IN
	default:
		return managementv1.EdgeType_EDGE_TYPE_UNSPECIFIED
	}
}
