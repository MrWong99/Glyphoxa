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
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// SessionManager is the in-process voice-loop control surface SessionServer
// drives (ADR-0039). *session.Manager satisfies it; tests use a fake so the
// handler is exercised without Discord.
type SessionManager interface {
	Start(ctx context.Context, tenantID, campaignID uuid.UUID) (storage.VoiceSession, error)
	Stop(ctx context.Context) (storage.VoiceSession, error)
	Snapshot() (storage.VoiceSession, bool)
}

// SessionStore is the narrow storage surface SessionServer needs: the operator's
// durable Active Campaign selection (#108), the most-recently-created campaign as
// the fallback that scopes a session, and the latest ended session for the idle
// last-session summary (#72).
type SessionStore interface {
	GetActiveCampaignForUser(ctx context.Context, discordUserID string) (storage.Campaign, error)
	GetActiveCampaign(ctx context.Context) (storage.Campaign, error)
	GetLatestVoiceSession(ctx context.Context, campaignID uuid.UUID) (storage.VoiceSession, error)
}

// SessionServer implements the Connect SessionService over a SessionManager +
// SessionStore: Start/Stop drive the in-process loop, GetSession reports the live
// or last session.
type SessionServer struct {
	mgr   SessionManager
	store SessionStore
	log   *slog.Logger
}

var _ managementv1connect.SessionServiceHandler = (*SessionServer)(nil)

// NewSessionServer wraps the manager + store in a SessionServer.
func NewSessionServer(mgr SessionManager, store SessionStore, log *slog.Logger) *SessionServer {
	if log == nil {
		log = slog.Default()
	}
	return &SessionServer{mgr: mgr, store: store, log: log}
}

// Handler builds the Connect HTTP handler for SessionService and returns its
// mount path + handler, mirroring the other servers.
func (s *SessionServer) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return managementv1connect.NewSessionServiceHandler(s, opts...)
}

// tenant resolves the operator's tenant from the auth interceptor's context.
func (s *SessionServer) tenant(ctx context.Context) (uuid.UUID, error) {
	id, ok := auth.TenantID(ctx)
	if !ok {
		return uuid.Nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no tenant in context"))
	}
	return id, nil
}

// GetSession returns the running session when one is live, otherwise the most
// recent ended session for the active campaign (the screen's last-session
// summary), and whether voice is active. A read (NO_SIDE_EFFECTS).
func (s *SessionServer) GetSession(
	ctx context.Context,
	_ *connect.Request[managementv1.GetSessionRequest],
) (*connect.Response[managementv1.GetSessionResponse], error) {
	if vs, active := s.mgr.Snapshot(); active {
		return connect.NewResponse(&managementv1.GetSessionResponse{
			Session: toProtoVoiceSession(vs),
			Active:  true,
		}), nil
	}

	// Idle: surface the last session for the active campaign, if any. A missing
	// campaign or no prior session is the never-run state, not an error.
	campaign, err := s.store.GetActiveCampaign(ctx)
	if errors.Is(err, storage.ErrNotFound) {
		return connect.NewResponse(&managementv1.GetSessionResponse{Active: false}), nil
	}
	if err != nil {
		s.log.Error("GetSession: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	latest, err := s.store.GetLatestVoiceSession(ctx, campaign.ID)
	if errors.Is(err, storage.ErrNotFound) {
		return connect.NewResponse(&managementv1.GetSessionResponse{Active: false}), nil
	}
	if err != nil {
		s.log.Error("GetSession: get latest voice session failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.GetSessionResponse{
		Session: toProtoVoiceSession(latest),
		Active:  false,
	}), nil
}

// StartSession launches the voice loop for the active campaign and returns the
// created running session.
func (s *SessionServer) StartSession(
	ctx context.Context,
	_ *connect.Request[managementv1.StartSessionRequest],
) (*connect.Response[managementv1.StartSessionResponse], error) {
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}

	campaign, err := s.startCampaign(ctx)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no active campaign to start a session for"))
	}
	if err != nil {
		s.log.Error("StartSession: resolve active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	vs, err := s.mgr.Start(ctx, tenantID, campaign.ID)
	if err != nil {
		return nil, s.startError(err)
	}
	return connect.NewResponse(&managementv1.StartSessionResponse{
		Session: toProtoVoiceSession(vs),
	}), nil
}

// startCampaign resolves the campaign a web Start binds to, honoring the durable
// /glyphoxa use selection so the web Start button and the slash command agree
// (ADR-0009, #108): the logged-in operator's active_campaign_id when set, else the
// most-recently-created campaign — kept as the fallback so a fresh install that
// has never run /glyphoxa use still starts. The slash surface is strict (no
// fallback) because it has the /use affordance; the web has no such hint, so it
// keeps the legacy default.
func (s *SessionServer) startCampaign(ctx context.Context) (storage.Campaign, error) {
	if u, ok := auth.CurrentUser(ctx); ok && u.DiscordUserID != "" {
		c, err := s.store.GetActiveCampaignForUser(ctx, u.DiscordUserID)
		if err == nil {
			return c, nil
		}
		if !errors.Is(err, storage.ErrNotFound) {
			return storage.Campaign{}, err
		}
		// No durable selection yet — fall back to the implicit default.
	}
	return s.store.GetActiveCampaign(ctx)
}

// startError maps a manager Start failure onto a Connect status code: the
// single-active guard is AlreadyExists, the configuration/mode preconditions are
// FailedPrecondition, anything else is a logged Internal.
func (s *SessionServer) startError(err error) error {
	switch {
	case errors.Is(err, session.ErrSessionActive):
		return connect.NewError(connect.CodeAlreadyExists, errors.New("a voice session is already active"))
	case errors.Is(err, session.ErrDiscordNotConfigured):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the Discord guild/voice channel are not configured"))
	case errors.Is(err, session.ErrDiscordTokenMissing):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("no Discord bot token is configured"))
	case errors.Is(err, session.ErrDiscordTokenUndecryptable):
		// The full decrypt detail stays in the manager/server log; the client gets a
		// static, actionable hint (no underlying error echoed).
		s.log.Error("StartSession: saved Discord bot token could not be decrypted", "err", err)
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("the saved Discord bot token could not be decrypted; ensure the server $GLYPHOXA_SECRET is set correctly (ADR-0004)"))
	case errors.Is(err, session.ErrVoiceUnavailable):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("voice is not available in this mode"))
	case errors.Is(err, session.ErrManagerClosed):
		// The manager is in its terminal closed state (#157): the process is
		// shutting down. Unavailable, so the client retries the restarted process.
		return connect.NewError(connect.CodeUnavailable, errors.New("the server is shutting down; try again shortly"))
	default:
		s.log.Error("StartSession: manager start failed", "err", err)
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
}

// StopSession cancels the active voice loop and returns the ended session.
func (s *SessionServer) StopSession(
	ctx context.Context,
	_ *connect.Request[managementv1.StopSessionRequest],
) (*connect.Response[managementv1.StopSessionResponse], error) {
	vs, err := s.mgr.Stop(ctx)
	if errors.Is(err, session.ErrNoActiveSession) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no active voice session"))
	}
	if err != nil {
		s.log.Error("StopSession: manager stop failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.StopSessionResponse{
		Session: toProtoVoiceSession(vs),
	}), nil
}

// toProtoVoiceSession maps a storage.VoiceSession onto its wire view. A zero
// value (no session) maps to nil so the screen renders the never-run state;
// ended_at is set only once the session has ended.
func toProtoVoiceSession(v storage.VoiceSession) *managementv1.VoiceSession {
	if v.ID == uuid.Nil {
		return nil
	}
	pb := &managementv1.VoiceSession{
		Id:         v.ID.String(),
		CampaignId: v.CampaignID.String(),
		Status:     string(v.Status),
		StartedAt:  timestamppb.New(v.StartedAt),
		LineCount:  uint32(v.LineCount), //nolint:gosec // line_count is a small non-negative count
	}
	if v.EndedAt != nil {
		pb.EndedAt = timestamppb.New(*v.EndedAt)
	}
	return pb
}
