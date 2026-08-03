package rpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
)

// kgNodes is the Knowledge Graph Node feature module (#126, ADR-0008 v1.0):
// CRUD + wiki search over the campaign's Nodes. Like the agent CRUD the handlers
// resolve the single operator's active campaign server-side (ADR-0039).
type kgNodes struct {
	store  kgNodeStore
	active *activeCampaignSource
}

// kgNodeStore is the narrow KG-Node surface the module needs (#126, #131);
// *storage.Store satisfies it. Mutations are campaign-scoped (#342), so another
// campaign's Nodes are never mutable.
type kgNodeStore interface {
	CreateNode(ctx context.Context, n storage.NewKGNode) (storage.KGNode, error)
	ListNodes(ctx context.Context, campaignID uuid.UUID) ([]storage.KGNode, error)
	UpdateNode(ctx context.Context, u storage.KGNodeUpdate) (storage.KGNode, error)
	DeleteNode(ctx context.Context, campaignID, id uuid.UUID) error
	// SearchNodes is the ranked fulltext wiki search (#131).
	SearchNodes(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.KGNode, error)
	// ReplaceNodeAspects rewrites a Node's Aspects to exactly the supplied list
	// (#542); ListNodeAspects reads them back with their assigned ids and positions.
	ReplaceNodeAspects(ctx context.Context, campaignID, nodeID uuid.UUID, aspects []storage.NewKGNodeAspect) error
	ListNodeAspects(ctx context.Context, campaignID, nodeID uuid.UUID) ([]storage.KGNodeAspect, error)
}

// toStorageAspects validates and maps the wire Aspect rows onto their storage
// form (#542). Blank rows (no key AND no value) are DROPPED rather than rejected:
// the editor keeps an empty trailing row for the next entry, and a save should not
// fail because the GM left it untouched. The caps come from pkg/kgvocab so the
// editor path and the Tool create path bound the same shape.
func toStorageAspects(in []*managementv1.NodeAspect) ([]storage.NewKGNodeAspect, error) {
	out := make([]storage.NewKGNodeAspect, 0, len(in))
	for _, a := range in {
		key := strings.TrimSpace(a.GetKey())
		value := strings.TrimSpace(a.GetValue())
		if key == "" && value == "" {
			continue
		}
		if utf8.RuneCountInString(key) > kgvocab.MaxAspectKeyRunes {
			return nil, fmt.Errorf("aspect label is too long (max %d characters)", kgvocab.MaxAspectKeyRunes)
		}
		if utf8.RuneCountInString(value) > kgvocab.MaxAspectValueRunes {
			return nil, fmt.Errorf("aspect text is too long (max %d characters)", kgvocab.MaxAspectValueRunes)
		}
		out = append(out, storage.NewKGNodeAspect{Key: key, Value: value, GMPrivate: a.GetGmPrivate()})
	}
	if len(out) > kgvocab.MaxAspectsPerNode {
		return nil, fmt.Errorf("an entry may carry at most %d aspects", kgvocab.MaxAspectsPerNode)
	}
	return out, nil
}

// saveAspects replaces a Node's Aspects and reads them back onto the node so the
// response carries the persisted ids and positions — the editor re-renders from
// the response, so returning what the client SENT would hide a dropped blank row.
func (s *kgNodes) saveAspects(ctx context.Context, campaignID uuid.UUID, n *storage.KGNode, aspects []storage.NewKGNodeAspect) error {
	if err := s.store.ReplaceNodeAspects(ctx, campaignID, n.ID, aspects); err != nil {
		return err
	}
	saved, err := s.store.ListNodeAspects(ctx, campaignID, n.ID)
	if err != nil {
		return err
	}
	n.Aspects = saved
	return nil
}

// CreateNode adds a Knowledge Graph Node to the active campaign and returns it. An
// UNSPECIFIED node_type or an empty name is CodeInvalidArgument; no campaign is
// CodeNotFound; a storage failure is CodeInternal.
func (s *kgNodes) CreateNode(
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

	c, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("CreateNode: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	aspects, err := toStorageAspects(m.GetAspects())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	created, err := s.store.CreateNode(ctx, storage.NewKGNode{
		CampaignID: c.ID,
		Type:       nodeType,
		Name:       strings.TrimSpace(m.GetName()),
		Body:       m.GetBody(),
		GMPrivate:  m.GetGmPrivate(),
	})
	if err != nil {
		slog.Default().Error("CreateNode: store create failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	// Aspects are written after the Node exists (they reference it). A failure here
	// leaves a created Node with no aspects rather than rolling back: the entry is
	// real and the GM re-saves its aspects, which beats losing the entry entirely.
	if len(aspects) > 0 {
		if err := s.saveAspects(ctx, c.ID, &created, aspects); err != nil {
			slog.Default().Error("CreateNode: store aspects failed", "node_id", created.ID, "err", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
	}
	return connect.NewResponse(&managementv1.CreateNodeResponse{Node: toProtoNode(created)}), nil
}

// ListNodes returns the active campaign's Knowledge Graph Nodes in the storage
// display order (type, then case-insensitive name). No campaign is CodeNotFound;
// a storage failure is CodeInternal.
func (s *kgNodes) ListNodes(
	ctx context.Context,
	_ *connect.Request[managementv1.ListNodesRequest],
) (*connect.Response[managementv1.ListNodesResponse], error) {
	c, err := s.active.resolve(ctx)
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

// UpdateNode saves a Node's editor fields (name/body/gm_private) and returns the
// updated Node. node_type is immutable, so it is never sent nor changed. An empty
// name or an unparsable id is CodeInvalidArgument; a missing id is CodeNotFound.
func (s *kgNodes) UpdateNode(
	ctx context.Context,
	req *connect.Request[managementv1.UpdateNodeRequest],
) (*connect.Response[managementv1.UpdateNodeResponse], error) {
	m := req.Msg
	id, err := uuid.Parse(m.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid node id"))
	}
	if strings.TrimSpace(m.GetName()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))
	}

	// Resolve the active campaign and scope the write to it (#342): the store's
	// UPDATE matches (id, campaign_id), so a Node in another campaign is never
	// mutable through this session — it reads back as CodeNotFound.
	c, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("UpdateNode: get active campaign failed", "node_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	aspects, err := toStorageAspects(m.GetAspects())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	updated, err := s.store.UpdateNode(ctx, storage.KGNodeUpdate{
		ID:         id,
		CampaignID: c.ID,
		Name:       strings.TrimSpace(m.GetName()),
		Body:       m.GetBody(),
		GMPrivate:  m.GetGmPrivate(),
	})
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
		}
		slog.Default().Error("UpdateNode: store update failed", "node_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	// The aspect list is REPLACE-in-full (#542), so it runs on every save — including
	// an empty list, which is how the GM clears the last aspect. Skipping the call
	// when the list is empty would make "delete my last aspect" silently impossible.
	if err := s.saveAspects(ctx, c.ID, &updated, aspects); err != nil {
		slog.Default().Error("UpdateNode: store aspects failed", "node_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.UpdateNodeResponse{Node: toProtoNode(updated)}), nil
}

// DeleteNode removes a Node by id. An unparsable id is CodeInvalidArgument; a
// missing id is CodeNotFound; a storage failure is CodeInternal.
func (s *kgNodes) DeleteNode(
	ctx context.Context,
	req *connect.Request[managementv1.DeleteNodeRequest],
) (*connect.Response[managementv1.DeleteNodeResponse], error) {
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid node id"))
	}

	// Resolve the active campaign and scope the delete to it (#342): the store's
	// DELETE matches (id, campaign_id), so a Node in another campaign is never
	// removable through this session — it reads back as CodeNotFound.
	c, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("DeleteNode: get active campaign failed", "node_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	switch err := s.store.DeleteNode(ctx, c.ID, id); {
	case err == nil:
		return connect.NewResponse(&managementv1.DeleteNodeResponse{}), nil
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	default:
		slog.Default().Error("DeleteNode: store delete failed", "node_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
}

// searchNodesLimit caps a wiki search result set (#131). It is a fixed server
// policy for the single-operator web tier (ADR-0039); the client sends no limit.
const searchNodesLimit = 50

// SearchNodes returns the active campaign's Knowledge Graph Nodes whose name or
// body match the query, ranked by relevance (#131, ADR-0008 v1.0). gm_private
// Nodes are INCLUDED (GM-facing search). An empty/whitespace query is
// CodeInvalidArgument; no campaign is CodeNotFound; a storage failure is
// CodeInternal.
func (s *kgNodes) SearchNodes(
	ctx context.Context,
	req *connect.Request[managementv1.SearchNodesRequest],
) (*connect.Response[managementv1.SearchNodesResponse], error) {
	if strings.TrimSpace(req.Msg.GetQuery()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("query must not be empty"))
	}

	c, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("SearchNodes: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	nodes, err := s.store.SearchNodes(ctx, c.ID, req.Msg.GetQuery(), searchNodesLimit)
	if err != nil {
		slog.Default().Error("SearchNodes: store search failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	out := make([]*managementv1.Node, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, toProtoNode(n))
	}
	return connect.NewResponse(&managementv1.SearchNodesResponse{Nodes: out}), nil
}

// toProtoNode maps a storage.KGNode onto its wire representation. agent_id carries
// the NPC-Node ↔ Agent link (#132) when set, else empty.
func toProtoNode(n storage.KGNode) *managementv1.Node {
	pn := &managementv1.Node{
		Id:         n.ID.String(),
		CampaignId: n.CampaignID.String(),
		NodeType:   toProtoNodeType(n.Type),
		Name:       n.Name,
		Body:       n.Body,
		GmPrivate:  n.GMPrivate,
		CreatedAt:  timestamppb.New(n.CreatedAt),
		UpdatedAt:  timestamppb.New(n.UpdatedAt),
	}
	if n.AgentID.Valid {
		pn.AgentId = n.AgentID.UUID.String()
	}
	for _, a := range n.Aspects {
		pn.Aspects = append(pn.Aspects, &managementv1.NodeAspect{
			Id:        a.ID.String(),
			Key:       a.Key,
			Value:     a.Value,
			GmPrivate: a.GMPrivate,
		})
	}
	return pn
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
