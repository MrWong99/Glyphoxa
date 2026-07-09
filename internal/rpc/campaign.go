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
// campaign read, the roster reads and agent CRUD writes (#71), and the campaign
// management reads/writes (#264). *storage.Store satisfies it, so handlers can be
// driven by a fake in unit tests and the real store in integration tests.
type campaignStore interface {
	// GetActiveCampaignForUser + GetActiveCampaign are the profile-first resolution
	// (durable /glyphoxa use selection → most-recent fallback) the header + CRUD +
	// KG reads scope through instead of the plain most-recent read (#222).
	GetActiveCampaignForUser(ctx context.Context, discordUserID string) (storage.Campaign, error)
	GetActiveCampaign(ctx context.Context) (storage.Campaign, error)
	// GetCampaign loads a campaign by id: the roster/mute panel resolves the LIVE
	// Voice Session's campaign first (#222), so it fetches that specific row rather
	// than the profile default. UpdateAgent also uses it to read the owning
	// campaign's language for a first-save voice default (#224).
	GetCampaign(ctx context.Context, id uuid.UUID) (storage.Campaign, error)
	// ListCampaigns/CreateCampaign/UpdateCampaign/SetActiveCampaign back the
	// campaign management RPCs (#264): the name-ordered picker list, the tenant-
	// scoped create (auto-Butler fires), the opaque name/system/language write, and
	// the durable /glyphoxa use selection shared with the slash-command surface.
	ListCampaigns(ctx context.Context) ([]storage.Campaign, error)
	CreateCampaign(ctx context.Context, c storage.NewCampaign) (uuid.UUID, error)
	UpdateCampaign(ctx context.Context, c storage.CampaignUpdate) (storage.Campaign, error)
	SetActiveCampaign(ctx context.Context, discordUserID string, campaignID uuid.UUID) error
	// The campaign archive lifecycle (#269): ListAllCampaigns is the archive-
	// inclusive list the include_archived flag routes to; Archive/Unarchive/Delete
	// are the lifecycle writes. Delete cascades in one statement; Archive clears any
	// durable selection pointing at the campaign.
	ListAllCampaigns(ctx context.Context) ([]storage.Campaign, error)
	ArchiveCampaign(ctx context.Context, id uuid.UUID) (storage.Campaign, error)
	UnarchiveCampaign(ctx context.Context, id uuid.UUID) (storage.Campaign, error)
	DeleteCampaign(ctx context.Context, id uuid.UUID) error
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
	// Player Character (PC) CRUD (#276, E4): the campaign-scoped roster read plus
	// create/update (incl. Discord-User rebind)/delete. All resolve the active
	// campaign server-side like the Agent CRUD, so another campaign's Characters
	// are never returned nor mutable.
	ListCharacters(ctx context.Context, campaignID uuid.UUID) ([]storage.Character, error)
	CreateCharacter(ctx context.Context, c storage.NewCharacter) (uuid.UUID, error)
	UpdateCharacter(ctx context.Context, c storage.CharacterUpdate) (storage.Character, error)
	DeleteCharacter(ctx context.Context, id uuid.UUID) error
}

// CampaignServer implements managementv1connect.CampaignServiceHandler over a
// campaignStore. tools is the built-in Tool catalog the grant editor lists
// (ADR-0028) — the available-Tools source for ListToolGrants and the registry
// UpdateToolGrant validates tool_name against (#117).
type CampaignServer struct {
	store campaignStore
	tools *tool.Registry
	// liveCampaign reports the live Voice Session's campaign id, if any. Nil until
	// SetSessions wires it; the roster/mute panel resolves through it so it scopes
	// to the campaign actually voicing, not a durable selection changed mid-session
	// (#222). Set once at boot before serving, so no lock is needed.
	liveCampaign func() (uuid.UUID, bool)
	// memberLister lists the Discord Users currently in the operator's voice
	// channel for the Players panel picker (#279). Nil until SetMemberLister wires
	// it (a keyless / bot-offline deployment leaves it nil); the handler then
	// returns an empty list so the UI falls back to free-text snowflake entry. Set
	// once at boot before serving, so no lock is needed.
	memberLister voiceMemberLister
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

// SetSessions wires the live Voice Session source the Active Campaign resolution
// consults (#222): while a session is live, EVERY CampaignService surface (header,
// roster/mute panel, campaign CRUD, KG wiki) scopes to that session's campaign, so
// the screen's reads and writes agree even if the durable selection was changed
// mid-session. Called once at boot, after the session manager exists and before
// the server serves, so no lock is needed — mirrors VoiceServer.SetSessions.
func (s *CampaignServer) SetSessions(src activeSessionSource) {
	s.liveCampaign = func() (uuid.UUID, bool) {
		vs, active := src.Snapshot()
		return vs.CampaignID, active
	}
}

// activeCampaign resolves the campaign every CampaignService handler scopes to,
// via the one shared resolveActiveCampaign policy (live Voice Session → durable
// /glyphoxa use selection → most-recent fallback, #222). Reads and writes on the
// same screen therefore always name the same campaign.
func (s *CampaignServer) activeCampaign(ctx context.Context) (storage.Campaign, error) {
	return resolveActiveCampaign(ctx, s.liveCampaign, s.store)
}

// GetActiveCampaign resolves the operator's active campaign and maps it onto the
// wire type. The Campaign is resolved live-first (the live Voice Session's
// campaign → durable /glyphoxa use selection → most-recent fallback), so the
// Session-screen header names the same campaign the roster, transcript, and Start
// do (#222). A storage.ErrNotFound (no campaign exists) becomes CodeNotFound; any
// other failure becomes CodeInternal.
func (s *CampaignServer) GetActiveCampaign(
	ctx context.Context,
	_ *connect.Request[managementv1.GetActiveCampaignRequest],
) (*connect.Response[managementv1.GetActiveCampaignResponse], error) {
	c, err := s.activeCampaign(ctx)
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
	pb := &managementv1.Campaign{
		Id:        c.ID.String(),
		TenantId:  c.TenantID.String(),
		Name:      c.Name,
		System:    c.System,
		Language:  c.Language,
		CreatedAt: timestamppb.New(c.CreatedAt),
		UpdatedAt: timestamppb.New(c.UpdatedAt),
	}
	// archived_at is left unset (nil) for an active campaign so the wire "unset =
	// active" contract holds; set only when the campaign is archived (#269).
	if c.ArchivedAt != nil {
		pb.ArchivedAt = timestamppb.New(*c.ArchivedAt)
	}
	return pb
}

// Handler builds the Connect HTTP handler for CampaignService and returns the
// path on which to mount it together with the handler. Callers (the web tier)
// mount it on a mux without importing the generated managementv1connect package
// directly.
func (s *CampaignServer) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return managementv1connect.NewCampaignServiceHandler(s, opts...)
}
