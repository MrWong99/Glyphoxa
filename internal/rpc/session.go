package rpc

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

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
	// SetAgentMute / SetAllMute toggle the live per-Agent mute set (#211), returning
	// the resulting sorted muted-id set; both fail ErrNoActiveSession when idle, and
	// SetAgentMute fails ErrAgentNotInCampaign for an agent outside the active
	// session's Campaign (validated atomically against that session).
	SetAgentMute(ctx context.Context, agentID string, muted bool) ([]string, error)
	SetAllMute(ctx context.Context, muted bool) ([]string, error)
	// MutedAgentIDs is the reload truth (AC5): the muted set while active, nil idle.
	MutedAgentIDs() []string
}

// SessionStore is the narrow storage surface SessionServer needs: the operator's
// durable Active Campaign selection (#108), the most-recently-created campaign as
// the fallback that scopes a session, the live Voice Session's campaign by id (the
// live-first resolution step, #222), the latest ended session for the idle
// last-session summary (#72), and the campaign-scoped transcript search (#120).
type SessionStore interface {
	GetCampaign(ctx context.Context, id uuid.UUID) (storage.Campaign, error)
	GetActiveCampaignForUser(ctx context.Context, discordUserID string) (storage.Campaign, error)
	GetActiveCampaign(ctx context.Context) (storage.Campaign, error)
	GetLatestVoiceSession(ctx context.Context, campaignID uuid.UUID) (storage.VoiceSession, error)
	SearchTranscriptLines(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.TranscriptLine, error)
}

// searchTranscriptLimit caps a transcript search result set (#120). It is a fixed
// server policy for the single-operator web tier (ADR-0039), mirroring
// searchNodesLimit; the client sends no limit.
const searchTranscriptLimit = 50

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
			Session:       toProtoVoiceSession(vs),
			Active:        true,
			MutedAgentIds: s.mgr.MutedAgentIDs(), // reload truth while live (AC5)
		}), nil
	}

	// Idle: surface the last session for the active campaign, if any. Resolve it
	// with the SAME profile-first startCampaign StartSession binds (durable
	// /glyphoxa use selection → most-recent fallback, #216/#220) so the idle summary
	// never describes a different campaign than Start would run or search would scope
	// to. A missing campaign or no prior session is the never-run state, not an error.
	campaign, err := s.startCampaign(ctx)
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

// liveCampaign reports the live Voice Session's campaign id, if any, off the
// manager Snapshot — the live-first input to resolveActiveCampaign (#222).
func (s *SessionServer) liveCampaign() (uuid.UUID, bool) {
	vs, active := s.mgr.Snapshot()
	return vs.CampaignID, active
}

// startCampaign resolves the campaign a web Start binds to, honoring the durable
// /glyphoxa use selection so the web Start button and the slash command agree
// (ADR-0009, #108). It is the shared resolveActiveCampaign policy (live Voice
// Session → durable /glyphoxa use selection → most-recent fallback, #222) — the
// SAME resolution the header, campaign CRUD, and KG wiki scope through. In the
// idle Start/GetSession paths the live step is a no-op (no session runs yet), so
// this resolves the durable selection then the most-recent fallback.
func (s *SessionServer) startCampaign(ctx context.Context) (storage.Campaign, error) {
	return resolveActiveCampaign(ctx, s.liveCampaign, s.store)
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

// SetAgentMute mutes or unmutes one Agent of the Active Campaign in the live
// Voice Session (#211). It refuses when no session is active
// (CodeFailedPrecondition) and rejects an agent_id that is not an Agent of the
// active session's campaign — or an unparsable id — with CodeNotFound. The
// Manager validates campaign membership atomically against the SAME session it
// writes, so a session swap can't slip a foreign agent into the new session's set.
func (s *SessionServer) SetAgentMute(
	ctx context.Context,
	req *connect.Request[managementv1.SetAgentMuteRequest],
) (*connect.Response[managementv1.SetAgentMuteResponse], error) {
	notFound := connect.NewError(connect.CodeNotFound, errors.New("no such Agent in the Active Campaign"))
	if _, err := uuid.Parse(req.Msg.GetAgentId()); err != nil {
		return nil, notFound // a non-UUID id names no Agent (session-independent)
	}

	ids, err := s.mgr.SetAgentMute(ctx, req.Msg.GetAgentId(), req.Msg.GetMuted())
	switch {
	case errors.Is(err, session.ErrNoActiveSession):
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no active voice session"))
	case errors.Is(err, session.ErrAgentNotInCampaign):
		return nil, notFound
	case err != nil:
		s.log.Error("SetAgentMute: manager mute failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.SetAgentMuteResponse{MutedAgentIds: ids}), nil
}

// SetAllMute mutes or unmutes every Agent of the Active Campaign in the live Voice
// Session (#211). It refuses when no session is active (CodeFailedPrecondition).
func (s *SessionServer) SetAllMute(
	ctx context.Context,
	req *connect.Request[managementv1.SetAllMuteRequest],
) (*connect.Response[managementv1.SetAllMuteResponse], error) {
	ids, err := s.mgr.SetAllMute(ctx, req.Msg.GetMuted())
	if errors.Is(err, session.ErrNoActiveSession) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no active voice session"))
	}
	if err != nil {
		s.log.Error("SetAllMute: manager mute failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.SetAllMuteResponse{MutedAgentIds: ids}), nil
}

// SearchTranscriptLines returns the operator's Active Campaign transcript Lines
// matching the query, ranked by relevance (#120). The Campaign is resolved
// server-side — the LIVE Voice Session's campaign first (exactly like GetSession:
// the Session screen renders that session's transcript, so search must scope to
// it, not a durable selection changed mid-session), else the same profile-first
// startCampaign StartSession uses (durable /glyphoxa use selection → most-recent
// fallback). NEVER a client-supplied id, so a search can never cross into another
// campaign's transcript (AC5). It shares ONE query path
// (storage.SearchTranscriptLines) with the `/glyphoxa search` slash command, whose
// resolveActiveCampaign is live-session-first for the same reason (AC4). An
// empty/whitespace query is CodeInvalidArgument; no Active Campaign yields an empty
// result (nothing to search, not an error); a storage failure is CodeInternal. A
// read (NO_SIDE_EFFECTS).
func (s *SessionServer) SearchTranscriptLines(
	ctx context.Context,
	req *connect.Request[managementv1.SearchTranscriptLinesRequest],
) (*connect.Response[managementv1.SearchTranscriptLinesResponse], error) {
	if strings.TrimSpace(req.Msg.GetQuery()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("query must not be empty"))
	}

	campaignID, ok, err := s.searchCampaign(ctx)
	if err != nil {
		s.log.Error("SearchTranscriptLines: resolve active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if !ok {
		// No Active Campaign: nothing to search yet (never-run state), not an error.
		return connect.NewResponse(&managementv1.SearchTranscriptLinesResponse{}), nil
	}

	lines, err := s.store.SearchTranscriptLines(ctx, campaignID, req.Msg.GetQuery(), searchTranscriptLimit)
	if err != nil {
		s.log.Error("SearchTranscriptLines: store search failed", "campaign_id", campaignID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	out := make([]*managementv1.TranscriptLineMatch, 0, len(lines))
	for _, l := range lines {
		out = append(out, toProtoTranscriptLineMatch(l))
	}
	return connect.NewResponse(&managementv1.SearchTranscriptLinesResponse{Lines: out}), nil
}

// searchCampaign resolves the campaign the web transcript search scopes to: the
// live Voice Session's campaign first (the same in-process truth GetSession uses,
// so search scopes to exactly the transcript on screen), otherwise the
// profile-first startCampaign (the operator's durable /glyphoxa use selection, else
// the most-recently-created fallback). ok is false only when neither resolves — a
// never-run state the caller answers with an empty result. A storage error other
// than ErrNotFound is returned.
func (s *SessionServer) searchCampaign(ctx context.Context) (uuid.UUID, bool, error) {
	if vs, active := s.mgr.Snapshot(); active {
		return vs.CampaignID, true, nil
	}
	campaign, err := s.startCampaign(ctx)
	if errors.Is(err, storage.ErrNotFound) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	return campaign.ID, true, nil
}

// toProtoTranscriptLineMatch maps a storage.TranscriptLine onto the wire search
// hit: the rendered fields plus the owning session + stable line id the web
// deep-links with (#120).
func toProtoTranscriptLineMatch(l storage.TranscriptLine) *managementv1.TranscriptLineMatch {
	return &managementv1.TranscriptLineMatch{
		SessionId: l.VoiceSessionID.String(),
		LineId:    l.LineID,
		Who:       l.Who,
		Tag:       l.Tag,
		Kind:      l.Kind,
		Ts:        timestamppb.New(l.TS),
		Text:      l.Text,
	}
}

// toProtoVoiceSession maps a storage.VoiceSession onto its wire view. A zero
// value (no session) maps to nil so the screen renders the never-run state;
// ended_at is set only once the session has ended, and end_reason carries the
// readable cause of an abnormal end (a fatal "failed" session, #123, or a
// boot-orphaned row, #143) — empty for a clean stop.
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
	if v.EndReason != nil {
		pb.EndReason = *v.EndReason
	}
	return pb
}
