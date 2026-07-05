package rpc

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Knowledge Graph Node handlers (#126, ADR-0008 v1.0) on CampaignServer: create
// and list the campaign's wiki Nodes. Like the agent CRUD they resolve the single
// operator's active campaign server-side (ADR-0039).

// CreateNode adds a Knowledge Graph Node to the active campaign and returns it. An
// UNSPECIFIED node_type or an empty name is CodeInvalidArgument; no campaign is
// CodeNotFound; a storage failure is CodeInternal.
func (s *CampaignServer) CreateNode(
	ctx context.Context,
	req *connect.Request[managementv1.CreateNodeRequest],
) (*connect.Response[managementv1.CreateNodeResponse], error) {
	m := req.Msg
	nodeType, ok := toStorageNodeType(m.GetNodeType())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("node type must be specified"))
	}
	if strings.TrimSpace(m.GetName()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))
	}

	c, err := s.store.GetActiveCampaign(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("CreateNode: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	created, err := s.store.CreateNode(ctx, storage.NewKGNode{
		CampaignID: c.ID,
		Type:       nodeType,
		Name:       m.GetName(),
		Body:       m.GetBody(),
		GMPrivate:  m.GetGmPrivate(),
	})
	if err != nil {
		slog.Default().Error("CreateNode: store create failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.CreateNodeResponse{Node: toProtoNode(created)}), nil
}

// ListNodes returns the active campaign's Knowledge Graph Nodes in the storage
// display order (type, then case-insensitive name). No campaign is CodeNotFound;
// a storage failure is CodeInternal.
func (s *CampaignServer) ListNodes(
	ctx context.Context,
	_ *connect.Request[managementv1.ListNodesRequest],
) (*connect.Response[managementv1.ListNodesResponse], error) {
	c, err := s.store.GetActiveCampaign(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("ListNodes: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	nodes, err := s.store.ListNodes(ctx, c.ID)
	if err != nil {
		slog.Default().Error("ListNodes: store list failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	out := make([]*managementv1.Node, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, toProtoNode(n))
	}
	return connect.NewResponse(&managementv1.ListNodesResponse{Nodes: out}), nil
}

// toProtoNode maps a storage.KGNode onto its wire representation.
func toProtoNode(n storage.KGNode) *managementv1.Node {
	return &managementv1.Node{
		Id:         n.ID.String(),
		CampaignId: n.CampaignID.String(),
		NodeType:   toProtoNodeType(n.Type),
		Name:       n.Name,
		Body:       n.Body,
		GmPrivate:  n.GMPrivate,
		CreatedAt:  timestamppb.New(n.CreatedAt),
		UpdatedAt:  timestamppb.New(n.UpdatedAt),
	}
}

// toStorageNodeType maps a wire NodeType onto the storage enum. The UNSPECIFIED
// zero value (and any unknown) returns ok=false so the handler rejects it with
// CodeInvalidArgument rather than persisting a garbage type.
func toStorageNodeType(t managementv1.NodeType) (storage.KGNodeType, bool) {
	switch t {
	case managementv1.NodeType_NODE_TYPE_CHARACTER:
		return storage.KGNodeCharacter, true
	case managementv1.NodeType_NODE_TYPE_NPC:
		return storage.KGNodeNPC, true
	case managementv1.NodeType_NODE_TYPE_LOCATION:
		return storage.KGNodeLocation, true
	case managementv1.NodeType_NODE_TYPE_FACTION:
		return storage.KGNodeFaction, true
	case managementv1.NodeType_NODE_TYPE_ITEM:
		return storage.KGNodeItem, true
	case managementv1.NodeType_NODE_TYPE_PLOT_THREAD:
		return storage.KGNodePlotThread, true
	case managementv1.NodeType_NODE_TYPE_NOTE:
		return storage.KGNodeNote, true
	default:
		return "", false
	}
}

// toProtoNodeType maps the storage enum back onto the wire NodeType. An unknown
// stored value maps to UNSPECIFIED (defensive; the DB enum keeps this exhaustive).
func toProtoNodeType(t storage.KGNodeType) managementv1.NodeType {
	switch t {
	case storage.KGNodeCharacter:
		return managementv1.NodeType_NODE_TYPE_CHARACTER
	case storage.KGNodeNPC:
		return managementv1.NodeType_NODE_TYPE_NPC
	case storage.KGNodeLocation:
		return managementv1.NodeType_NODE_TYPE_LOCATION
	case storage.KGNodeFaction:
		return managementv1.NodeType_NODE_TYPE_FACTION
	case storage.KGNodeItem:
		return managementv1.NodeType_NODE_TYPE_ITEM
	case storage.KGNodePlotThread:
		return managementv1.NodeType_NODE_TYPE_PLOT_THREAD
	case storage.KGNodeNote:
		return managementv1.NodeType_NODE_TYPE_NOTE
	default:
		return managementv1.NodeType_NODE_TYPE_UNSPECIFIED
	}
}
