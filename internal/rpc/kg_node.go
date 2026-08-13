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
	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
)

// kgNodes is the Knowledge Graph Node feature module (#126, ADR-0008 v1.0):
// CRUD + wiki search over the campaign's Nodes. Like the agent CRUD the handlers
// resolve the single operator's active campaign server-side (ADR-0039).
type kgNodes struct {
	store  kgNodeStore
	active *activeCampaignSource
	// blobs releases a deleted Node's portrait bytes (#590, ADR-0048: deletion
	// goes through the seam, not FK cascade). Nil-safe: a composition without
	// the seam merely leaves reclaimable bytes behind.
	blobs blob.Store
}

// kgNodeStore is the narrow KG-Node surface the module needs (#126, #131);
// *storage.Store satisfies it. Mutations are campaign-scoped (#342), so another
// campaign's Nodes are never mutable.
type kgNodeStore interface {
	CreateNode(ctx context.Context, n storage.NewKGNode) (storage.KGNode, error)
	ListNodes(ctx context.Context, campaignID uuid.UUID) ([]storage.KGNode, error)
	UpdateNode(ctx context.Context, u storage.KGNodeUpdate) (storage.KGNode, error)
	// DeleteNode returns the deleted Node's portrait blob key ('' when it had
	// none) so the handler can release the bytes through the seam (#590).
	DeleteNode(ctx context.Context, campaignID, id uuid.UUID) (portraitBlobKey string, err error)
	// SearchNodes is the ranked fulltext wiki search (#131).
	SearchNodes(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.KGNode, error)
	// CreateNodeWithAspects and UpdateNodeWithAspects save a Node and its Aspects
	// ATOMICALLY (#542) — a half-applied save would leave the GM unable to tell
	// whether a retry duplicates the entry.
	CreateNodeWithAspects(ctx context.Context, n storage.NewKGNode, aspects []storage.NewKGNodeAspect) (storage.KGNode, error)
	UpdateNodeWithAspects(ctx context.Context, u storage.KGNodeUpdate, w storage.KGNodeAspectWrite) (storage.KGNode, error)
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
		// The row's persisted id rides along so the store updates it IN PLACE rather
		// than deleting and reinserting — which is what keeps a repeated save
		// idempotent instead of duplicating the list. An unparsable or invented id is
		// simply treated as a new row by the store.
		row := storage.NewKGNodeAspect{Key: key, Value: value, GMPrivate: a.GetGmPrivate()}
		if id, err := uuid.Parse(a.GetId()); err == nil {
			row.ID = id
		}
		out = append(out, row)
	}
	if len(out) > kgvocab.MaxAspectsPerNode {
		return nil, fmt.Errorf("an entry may carry at most %d aspects", kgvocab.MaxAspectsPerNode)
	}
	return out, nil
}

// knownAspectIDs parses the aspect ids the CLIENT had loaded. The store deletes
// only these, so an Aspect appended by a Knowledge Proposal approval while the
// editor was open survives the save instead of being silently wiped (#542).
//
// It is a SEPARATE field from the aspect rows on purpose: a row the GM deleted is
// absent from the rows and present here, which is exactly how the server learns it
// was deleted. Deriving the set from the rows would make deletion impossible.
// Unparsable ids are dropped rather than rejected — a garbage id can only fail to
// match a row, and refusing the whole save over one would cost the GM their edit.
func knownAspectIDs(in []string) []uuid.UUID {
	var out []uuid.UUID
	for _, raw := range in {
		if id, err := uuid.Parse(raw); err == nil && id != uuid.Nil {
			out = append(out, id)
		}
	}
	return out
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

	// The Node and its Aspects land in ONE transaction, so a failure leaves nothing
	// behind and a retry cannot duplicate the entry.
	created, err := s.store.CreateNodeWithAspects(ctx, storage.NewKGNode{
		CampaignID: c.ID,
		Type:       nodeType,
		Name:       strings.TrimSpace(m.GetName()),
		Body:       m.GetBody(),
		GMPrivate:  m.GetGmPrivate(),
	}, aspects)
	if err != nil {
		slog.Default().Error("CreateNode: store create failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
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

	// The editor fields and the aspect list are ONE act to the GM, so they are one
	// transaction here. The aspect write runs on every save — including an empty
	// list, which is how the GM clears the last aspect.
	updated, err := s.store.UpdateNodeWithAspects(ctx, storage.KGNodeUpdate{
		ID:         id,
		CampaignID: c.ID,
		Name:       strings.TrimSpace(m.GetName()),
		Body:       m.GetBody(),
		GMPrivate:  m.GetGmPrivate(),
	}, storage.KGNodeAspectWrite{Known: knownAspectIDs(m.GetKnownAspectIds()), Rows: aspects})
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
		}
		if errors.Is(err, storage.ErrAspectsFull) {
			// The authored list fits the cap on its own, but a fact approved while the
			// editor was open pushed the total over. Say so — silently dropping either
			// side would lose a fact the GM believes is saved.
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
				"this entry now has more than %d facts — a suggestion was approved while you were editing; remove one and save again",
				kgvocab.MaxAspectsPerNode))
		}
		slog.Default().Error("UpdateNode: store update failed", "node_id", id, "err", err)
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

	portraitKey, err := s.store.DeleteNode(ctx, c.ID, id)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	case err != nil:
		slog.Default().Error("DeleteNode: store delete failed", "node_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	// A failed blob delete leaves reclaimable bytes, not a broken entry — a
	// warning, not a failed request the GM would retry against an already-gone
	// row. An empty key names no bytes, and deleting "" is ErrInvalidKey — a
	// warning about leaked bytes that never existed (the DeleteMap posture).
	if s.blobs != nil && portraitKey != "" {
		if derr := s.blobs.Delete(ctx, portraitKey); derr != nil {
			slog.Default().Warn("DeleteNode: could not drop portrait blob", "key", portraitKey, "err", derr)
		}
	}
	return connect.NewResponse(&managementv1.DeleteNodeResponse{}), nil
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
		// The key IS the picture's existence (#590): it is minted only when bytes
		// are written, so an empty one means there is no portrait to fetch.
		HasPortrait: n.PortraitBlobKey != "",
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
