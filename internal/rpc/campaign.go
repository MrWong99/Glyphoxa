// Package rpc adapts the storage layer to the Connect handlers that the web
// tier serves (ADR-0039). It owns the mapping between internal storage models
// and the generated wire types in gen/glyphoxa/management/v1, and the
// translation of storage errors into Connect status codes. Handlers depend on
// narrow reader interfaces (not *storage.Store) so they unit-test keyless with
// a fake and integration-test against a real store; CampaignServer composes
// per-feature modules, each over its own 3–6-method store slice (#445).
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

// CampaignServer implements managementv1connect.CampaignServiceHandler as a
// composition of feature modules (#445): campaign management, the archive
// lifecycle, the Agent roster, Player Characters, KG Nodes/Edges, Knowledge
// Proposals, and Tool Grants. Each module declares the minimal store slice it
// consumes (all satisfied structurally by *storage.Store) and its RPC methods
// promote onto this struct, so the wire surface — one CampaignService behind
// one interceptor stack — is unchanged while a unit test fakes only the slice
// its feature reads.
type CampaignServer struct {
	// active is the ONE live-first Active-Campaign resolution every module
	// shares (#222): SetSessions wires its live closure once at boot, and every
	// surface (header, roster/mute panel, campaign CRUD, KG wiki, review queue)
	// scopes through it, so a screen's reads and writes always agree.
	active *activeCampaignSource

	*campaignManagement
	*campaignArchive
	*agentRoster
	*characterRoster
	*kgNodes
	*kgEdges
	*knowledgeProposals
	*toolGrants
}

// CampaignStores groups the per-feature store slices CampaignServer composes
// (#445). One *storage.Store satisfies every field structurally — that is what
// NewCampaignServer wires — while a unit test fills ONLY the slice its feature
// under test reads (plus Active for handlers that resolve the Active Campaign)
// and leaves the rest nil.
type CampaignStores struct {
	// Active backs the shared live-first Active-Campaign resolution (#222)
	// every scoped handler walks before touching its feature slice.
	Active activeCampaignResolver
	// Campaigns backs the campaign management surface: list/create/update and
	// the durable /glyphoxa use selection (#264).
	Campaigns campaignManagementStore
	// Archive backs the archive/hard-delete lifecycle (#269).
	Archive campaignArchiveStore
	// Agents backs the roster read and the Agent (NPC) CRUD (#71).
	Agents agentStore
	// Characters backs the Player Character CRUD (#276).
	Characters characterStore
	// KGNodes backs the Knowledge Graph Node CRUD + wiki search (#126, #131).
	KGNodes kgNodeStore
	// KGEdges backs the Knowledge Graph Edge CRUD + the voiced-by link (#132).
	KGEdges kgEdgeStore
	// Proposals backs the Knowledge Proposal review queue + similarity hint
	// (#300, ADR-0052).
	Proposals knowledgeProposalStore
	// Grants backs the Tool Grant editor (#117).
	Grants toolGrantStore
}

// NewCampaignServer wires every feature module over the one concrete store —
// the production composition (cmd/glyphoxa) and the integration tests use it.
// The available-Tools catalog is the shared built-in Registry (ADR-0028), so
// the grants a GM can toggle are exactly the Tools a Voice Session runs;
// nothing external configures it.
func NewCampaignServer(s *storage.Store) *CampaignServer {
	return NewCampaignServerWith(CampaignStores{
		Active:     s,
		Campaigns:  s,
		Archive:    s,
		Agents:     s,
		Characters: s,
		KGNodes:    s,
		KGEdges:    s,
		Proposals:  s,
		Grants:     s,
	})
}

// NewCampaignServerWith composes the feature modules over per-feature store
// slices (#445), so a unit test drives one feature over the full Connect stack
// while faking only that feature's slice. A nil slice leaves that feature's
// handlers panicking on first use — fill exactly what the test exercises.
func NewCampaignServerWith(stores CampaignStores) *CampaignServer {
	active := &activeCampaignSource{store: stores.Active}
	return &CampaignServer{
		active:             active,
		campaignManagement: &campaignManagement{store: stores.Campaigns, active: active},
		campaignArchive:    &campaignArchive{store: stores.Archive, active: active},
		agentRoster:        &agentRoster{store: stores.Agents, active: active},
		characterRoster:    &characterRoster{store: stores.Characters, active: active},
		kgNodes:            &kgNodes{store: stores.KGNodes, active: active},
		kgEdges:            &kgEdges{store: stores.KGEdges, active: active},
		knowledgeProposals: &knowledgeProposals{store: stores.Proposals, active: active},
		toolGrants:         &toolGrants{store: stores.Grants, active: active, tools: tool.BuiltinRegistry(tool.Deps{})},
	}
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
	s.active.live = func() (uuid.UUID, bool) {
		vs, active := src.Snapshot()
		return vs.CampaignID, active
	}
}

// SetSpeakerInvalidator wires the live speaker resolver's campaign invalidation
// (#281). Called once at boot before serving, so no lock is needed; nil-safe at the
// call sites (invalidateSpeakers). A nil resolver leaves the hook off.
func (s *CampaignServer) SetSpeakerInvalidator(inv SpeakerInvalidator) {
	s.speakerInv = inv
}

// SetEmbedder wires the embeddings provider the ListSimilarKnowledge vector hint
// uses (#300). Called once at boot before serving; nil leaves the hint on the
// fulltext fallback only.
func (s *CampaignServer) SetEmbedder(e Embedder) {
	s.embedder = e
}

// SetHighlightClipSweeper wires the highlight-clip blob sweep the campaign hard
// delete runs (#308). Called once at boot before serving; nil leaves the sweep
// off (the highlight rows still cascade, only their blobs would linger).
func (s *CampaignServer) SetHighlightClipSweeper(sw HighlightClipSweeper) {
	s.clips = sw
}

// campaignManagement is the campaign lifecycle feature module (#264, #222): the
// Session-screen header's Active-Campaign read, the campaign list/create/update
// management surface, the durable /glyphoxa use selection, and the Campaign
// Language catalog (#268 — a pure registry read).
type campaignManagement struct {
	store  campaignManagementStore
	active *activeCampaignSource
}

// campaignManagementStore is the narrow campaign-management surface the module
// needs (#264); *storage.Store satisfies it, so the handlers unit-test keyless
// with a fake.
type campaignManagementStore interface {
	// ListCampaignsInTenant is the tenant-scoped name-ordered ACTIVE-only picker
	// list; ListAllCampaignsInTenant is the archive-inclusive variant the
	// include_archived flag routes to (#269). Both scope to auth.TenantID(ctx) so the
	// picker shows only the caller's own campaigns (#473).
	ListCampaignsInTenant(ctx context.Context, tenantID uuid.UUID) ([]storage.Campaign, error)
	ListAllCampaignsInTenant(ctx context.Context, tenantID uuid.UUID) ([]storage.Campaign, error)
	// CreateCampaign is the tenant-scoped create (the ADR-0009 auto-Butler trigger
	// fires on the insert); GetCampaignInTenant backs its read-back and
	// SetActiveCampaign's pre-write validation, both scoped so a cross-tenant id
	// resolves to NotFound (#473).
	CreateCampaign(ctx context.Context, c storage.NewCampaign) (uuid.UUID, error)
	GetCampaignInTenant(ctx context.Context, tenantID, id uuid.UUID) (storage.Campaign, error)
	UpdateCampaign(ctx context.Context, c storage.CampaignUpdate) (storage.Campaign, error)
	// SetActiveCampaign records the durable /glyphoxa use selection shared with the
	// slash-command surface (migration 00014).
	SetActiveCampaign(ctx context.Context, discordUserID string, campaignID uuid.UUID) error
}

// GetActiveCampaign resolves the operator's active campaign and maps it onto the
// wire type. The Campaign is resolved live-first (the live Voice Session's
// campaign → durable /glyphoxa use selection → most-recent fallback), so the
// Session-screen header names the same campaign the roster, transcript, and Start
// do (#222). A storage.ErrNotFound (no campaign exists) becomes CodeNotFound; any
// other failure becomes CodeInternal.
func (s *campaignManagement) GetActiveCampaign(
	ctx context.Context,
	_ *connect.Request[managementv1.GetActiveCampaignRequest],
) (*connect.Response[managementv1.GetActiveCampaignResponse], error) {
	c, err := s.active.resolve(ctx)
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
		TapeArmed: c.TapeArmed,
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
