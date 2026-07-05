// Package rpc adapts the storage layer to the Connect handlers that the web
// tier serves (ADR-0039). It owns the mapping between internal storage models
// and the generated wire types in gen/glyphoxa/management/v1, and the
// translation of storage errors into Connect status codes. Handlers depend on
// narrow reader interfaces (not *storage.Store) so they unit-test keyless with
// a fake and integration-test against a real store.
package rpc

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// campaignStore is the narrow storage surface CampaignServer needs — the active
// campaign read plus the roster reads and the agent CRUD writes (#71).
// *storage.Store satisfies it, so handlers can be driven by a fake in unit tests
// and the real store in integration tests.
type campaignStore interface {
	GetActiveCampaign(ctx context.Context) (storage.Campaign, error)
	GetButler(ctx context.Context, campaignID uuid.UUID) (storage.Agent, error)
	CharacterAgents(ctx context.Context, campaignID uuid.UUID) ([]storage.Agent, error)
	GetAgent(ctx context.Context, id uuid.UUID) (storage.Agent, error)
	CreateAgent(ctx context.Context, a storage.NewAgent) (uuid.UUID, error)
	UpdateAgent(ctx context.Context, a storage.AgentUpdate) (storage.Agent, error)
	DeleteAgent(ctx context.Context, id uuid.UUID) error
	CreateNode(ctx context.Context, n storage.NewKGNode) (storage.KGNode, error)
	ListNodes(ctx context.Context, campaignID uuid.UUID) ([]storage.KGNode, error)
	UpdateNode(ctx context.Context, u storage.KGNodeUpdate) (storage.KGNode, error)
	DeleteNode(ctx context.Context, id uuid.UUID) error
	CreateEdge(ctx context.Context, e storage.NewKGEdge) (storage.KGEdge, error)
	DeleteEdge(ctx context.Context, id uuid.UUID) error
	NodeEdges(ctx context.Context, nodeID uuid.UUID) (outgoing, incoming []storage.KGEdgeWithNodes, err error)
	SetNodeAgent(ctx context.Context, nodeID uuid.UUID, agentID uuid.NullUUID) (storage.KGNode, error)
	SearchNodes(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.KGNode, error)
	ListToolGrants(ctx context.Context, agentID uuid.UUID) ([]storage.ToolGrant, error)
	UpsertToolGrant(ctx context.Context, g storage.NewToolGrant) error
	DeleteToolGrant(ctx context.Context, agentID uuid.UUID, toolName string) error
}

// CampaignServer implements managementv1connect.CampaignServiceHandler over a
// campaignStore. tools is the built-in Tool catalog the grant editor lists
// (ADR-0028) — the available-Tools source for ListToolGrants and the registry
// UpdateToolGrant validates tool_name against (#117).
type CampaignServer struct {
	store campaignStore
	tools *tool.Registry
}

// NewCampaignServer wraps a campaignStore (e.g. *storage.Store) in a
// CampaignServer. The available-Tools catalog is the shared built-in Registry
// (ADR-0028), so the grants a GM can toggle are exactly the Tools a Voice Session
// runs; nothing external configures it.
func NewCampaignServer(s campaignStore) *CampaignServer {
	return &CampaignServer{store: s, tools: tool.BuiltinRegistry()}
}

// compile-time assertion that CampaignServer satisfies the generated handler.
var _ managementv1connect.CampaignServiceHandler = (*CampaignServer)(nil)

// GetActiveCampaign resolves the operator's active campaign and maps it onto
// the wire type. A storage.ErrNotFound (no campaign exists) becomes
// CodeNotFound; any other failure becomes CodeInternal.
func (s *CampaignServer) GetActiveCampaign(
	ctx context.Context,
	_ *connect.Request[managementv1.GetActiveCampaignRequest],
) (*connect.Response[managementv1.GetActiveCampaignResponse], error) {
	c, err := s.store.GetActiveCampaign(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		// Log the raw cause server-side and return a generic message: the storage
		// error can wrap query/DSN detail that should not reach an RPC client.
		slog.Default().Error("GetActiveCampaign: storage read failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.GetActiveCampaignResponse{
		Campaign: toProtoCampaign(c),
	}), nil
}

// toProtoCampaign maps a storage.Campaign onto its wire representation. The
// nullable GMMemberID is intentionally not mapped — it is not part of the
// management.v1 Campaign message (SEAM #6).
func toProtoCampaign(c storage.Campaign) *managementv1.Campaign {
	return &managementv1.Campaign{
		Id:        c.ID.String(),
		TenantId:  c.TenantID.String(),
		Name:      c.Name,
		System:    c.System,
		Language:  c.Language,
		CreatedAt: timestamppb.New(c.CreatedAt),
		UpdatedAt: timestamppb.New(c.UpdatedAt),
	}
}

// Handler builds the Connect HTTP handler for CampaignService and returns the
// path on which to mount it together with the handler. Callers (the web tier)
// mount it on a mux without importing the generated managementv1connect package
// directly.
func (s *CampaignServer) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return managementv1connect.NewCampaignServiceHandler(s, opts...)
}
