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
	"github.com/MrWong99/Glyphoxa/internal/recap"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// SessionManager is the in-process voice-loop control surface SessionServer
// drives (ADR-0039). *session.Manager satisfies it; tests use a fake so the
// handler is exercised without Discord.
type SessionManager interface {
	Start(ctx context.Context, tenantID, campaignID uuid.UUID) (storage.VoiceSession, error)
	Stop(ctx context.Context, tenantID uuid.UUID) (storage.VoiceSession, error)
	// Active reports tenantID's live Voice Session (S3, #488): the per-Tenant read
	// replacing the single-active Snapshot, so with N concurrent sessions this
	// operator sees only their own Tenant's session.
	Active(ctx context.Context, tenantID uuid.UUID) (storage.VoiceSession, bool, error)
	// SetAgentMute / SetAllMute toggle tenantID's live per-Agent mute set (#211,
	// #488), returning the resulting sorted muted-id set; both fail
	// ErrNoActiveSession when that Tenant has no live session. The set is the
	// Character NPCs only — the Address-Only Butler is not a mute target (voiced
	// since the ADR-0009 #299 amendment, but mute is matcher-owned and
	// Character-only), so SetAllMute skips it and SetAgentMute fails
	// ErrAgentNotInCampaign for it, just as for an agent outside the active session's
	// Campaign (all validated atomically against that session).
	SetAgentMute(ctx context.Context, tenantID uuid.UUID, agentID string, muted bool) ([]string, error)
	SetAllMute(ctx context.Context, tenantID uuid.UUID, muted bool) ([]string, error)
	// MutedAgentIDs is the reload truth (AC5): tenantID's muted set while active, nil
	// when that Tenant has no live session.
	MutedAgentIDs(tenantID uuid.UUID) []string
	// Spend snapshots tenantID's active session spend meter (#130, ADR-0046):
	// estimated USD + cap state, the reload truth for the Session screen's spend-cap
	// badge. The zero Status when that Tenant is idle or no caps are configured.
	Spend(tenantID uuid.UUID) spend.Status
}

// SessionStore is the narrow storage surface SessionServer needs: the operator's
// durable Active Campaign selection (#108), the most-recently-created campaign as
// the fallback that scopes a session, the live Voice Session's campaign by id (the
// live-first resolution step, #222), the latest ended session for the idle
// last-session summary (#72), and the campaign-scoped transcript search (#120).
type SessionStore interface {
	// The three tenant-scoped resolver reads back the shared resolveActiveCampaign
	// policy (#473): StartSession takes tenantID from ctx and the campaign from this
	// SAME scoped resolver, so the session it starts (and meters spend to) can never
	// be a foreign tenant's campaign (self-signup design §0a).
	GetCampaignInTenant(ctx context.Context, tenantID, id uuid.UUID) (storage.Campaign, error)
	GetActiveCampaignForUserInTenant(ctx context.Context, tenantID uuid.UUID, discordUserID string) (storage.Campaign, error)
	GetActiveCampaignInTenant(ctx context.Context, tenantID uuid.UUID) (storage.Campaign, error)
	GetLatestVoiceSession(ctx context.Context, campaignID uuid.UUID) (storage.VoiceSession, error)
	ListVoiceSessions(ctx context.Context, campaignID uuid.UUID, limit int) ([]storage.VoiceSession, error)
	SearchTranscriptLines(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.TranscriptLine, error)
	// GetVoiceSession loads one Voice Session by id for the GenerateRecap ownership
	// check (#274): the resolved Active Campaign must own every requested id, else
	// CodeNotFound (existence never leaked). storage.ErrNotFound for a missing id.
	GetVoiceSession(ctx context.Context, id uuid.UUID) (storage.VoiceSession, error)
}

// RecapEngine regenerates a Butler-flavoured Recap over the given Voice Sessions
// (#272/#274). *recap.Engine satisfies it; tests inject a fake. It NEVER persists
// (gate #271) and spends provider money per call, so GenerateRecap is guarded like
// a mutation (ADR-0039).
type RecapEngine interface {
	Recap(ctx context.Context, sessionIDs []uuid.UUID) (recap.Result, error)
}

// errSplitMode is the static client message for a live-session control invoked on
// the web tier of a split (-mode web + -mode voice) deployment (#491): the live
// state lives in the worker, so mute/say/replay/spend degrade with
// CodeFailedPrecondition rather than lie (ADR-0057 consequence).
var errSplitMode = errors.New("not available in a split deployment (the voice worker holds the live session)")

// errControlPending is the static client message for a relayed live control
// (#503) the hosting worker did not confirm within the control budget.
var errControlPending = errors.New("the voice worker has not confirmed the control yet; try again shortly")

// searchTranscriptLimit caps a transcript search result set (#120). It is a fixed
// server policy for the single-operator web tier (ADR-0039), mirroring
// searchNodesLimit; the client sends no limit.
const searchTranscriptLimit = 50

// listSessionsLimit caps the past-session picker's result set (#270). Like
// searchTranscriptLimit it is a fixed server policy for the single-operator web
// tier (ADR-0039); the client sends no limit.
const listSessionsLimit = 50

// maxRecapSessions caps how many Voice Sessions one GenerateRecap may span (#274).
// The web recaps a single session; the cap is a spend guardrail (each session is a
// money-spending LLM call, gate #271), set well above any realistic pick.
const maxRecapSessions = 20

// SessionServer implements the Connect SessionService over a SessionManager +
// SessionStore: Start/Stop drive the in-process loop, GetSession reports the live
// or last session.
type SessionServer struct {
	mgr      SessionManager
	store    SessionStore
	recapper RecapEngine
	log      *slog.Logger

	// highlights + blobs back the Session Highlights RPCs (#308); wired via
	// SetHighlights after construction so the many existing call sites keep their
	// signature. Nil (unwired, e.g. keyless tests) makes the Highlight RPCs report
	// CodeUnimplemented rather than panic.
	highlights HighlightStore
	blobs      highlightBlobs
	// enqueue schedules the image-enrichment job on promotion (#311, ADR-0049);
	// nil disables enrichment (the promote itself still succeeds).
	enqueue HighlightEnqueuer

	// sharer / replayer / shareStore back the Highlight Discord-delivery RPCs (#310);
	// wired via SetSharing after construction. Nil (unwired) makes ShareHighlight /
	// ListShareChannels report CodeUnimplemented rather than panic.
	sharer     HighlightSharer
	replayer   HighlightReplayer
	shareStore ShareChannelStore
}

var _ managementv1connect.SessionServiceHandler = (*SessionServer)(nil)

// NewSessionServer wraps the manager + store + recap engine in a SessionServer.
func NewSessionServer(mgr SessionManager, store SessionStore, recapper RecapEngine, log *slog.Logger) *SessionServer {
	if log == nil {
		log = slog.Default()
	}
	return &SessionServer{mgr: mgr, store: store, recapper: recapper, log: log}
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
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	if vs, active, aerr := s.mgr.Active(ctx, tenantID); aerr == nil && active {
		// Spend-cap reload truth while live (#130): the badge reads the meter state +
		// estimated spend the same way muted_agent_ids reads the mute set (AC5). All
		// scoped to THIS Tenant's session (#488).
		sp := s.mgr.Spend(tenantID)
		return connect.NewResponse(&managementv1.GetSessionResponse{
			Session:           toProtoVoiceSession(vs),
			Active:            true,
			MutedAgentIds:     s.mgr.MutedAgentIDs(tenantID), // reload truth while live (AC5)
			SpendCapState:     string(sp.State),
			EstimatedSpendUsd: sp.EstimatedUSD,
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

// liveCampaign reports the CALLER's Tenant live Voice Session campaign id, if any
// — the live-first input to resolveActiveCampaign (#222, #488). It reads the
// per-Tenant [session.Manager.Active] rather than the process-wide single-active
// snapshot, so with N concurrent sessions the live-first pivot uses only this
// operator's own Tenant's session (never a foreign Tenant's).
func (s *SessionServer) liveCampaign(ctx context.Context, tenantID uuid.UUID) (uuid.UUID, bool) {
	vs, active, err := s.mgr.Active(ctx, tenantID)
	if err != nil || !active {
		return uuid.Nil, false
	}
	return vs.CampaignID, true
}

// startCampaign resolves the campaign a web Start binds to, honoring the durable
// /glyphoxa use selection so the web Start button and the slash command agree
// (ADR-0009, #108). It is the shared resolveActiveCampaign policy (live Voice
// Session → durable /glyphoxa use selection → most-recent fallback, #222) — the
// SAME resolution the header, campaign CRUD, and KG wiki scope through. In the
// idle Start/GetSession paths the live step is a no-op (no session runs yet), so
// this resolves the durable selection then the most-recent fallback.
func (s *SessionServer) startCampaign(ctx context.Context) (storage.Campaign, error) {
	tenantID, _ := auth.TenantID(ctx)
	live := func(c context.Context) (uuid.UUID, bool) { return s.liveCampaign(c, tenantID) }
	return resolveActiveCampaign(ctx, live, s.store)
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
	case errors.Is(err, session.ErrAllowanceExhausted):
		// ADR-0055 gate (b): a deliberate policy refusal with an actionable
		// message — the estimate resets with the next calendar month.
		return connect.NewError(connect.CodeResourceExhausted,
			errors.New("the plan's monthly usage allowance is exhausted; it resets next month (estimates, ADR-0046)"))
	case errors.Is(err, session.ErrSessionLimit):
		// #488: the process is at its concurrent-session cap
		// (GLYPHOXA_MAX_VOICE_SESSIONS). Distinct, user-visible, and retryable once a
		// running session ends — the ErrAllowanceExhausted ResourceExhausted precedent.
		return connect.NewError(connect.CodeResourceExhausted,
			errors.New("the server is already running the maximum number of concurrent voice sessions; try again once one ends"))
	case errors.Is(err, session.ErrManagerClosed):
		// The manager is in its terminal closed state (#157): the process is
		// shutting down. Unavailable, so the client retries the restarted process.
		return connect.NewError(connect.CodeUnavailable, errors.New("the server is shutting down; try again shortly"))
	case errors.Is(err, session.ErrIntentPending):
		// Split mode (#491): the intent was written but no -mode voice worker claimed
		// and drove it live within the Start budget, so IntentControl CANCELLED the
		// pending row before returning. Unavailable, so the operator's retry starts a
		// fresh intent rather than colliding — the worker picks it up on its next poll.
		return connect.NewError(connect.CodeUnavailable, errors.New("voice worker has not claimed the session yet; try again shortly"))
	case errors.Is(err, session.ErrIntentCancelled):
		// Split mode (#491): the queued start was stopped before any worker claimed
		// it — a distinct cancelled outcome, not a fault and not still-pending.
		return connect.NewError(connect.CodeAborted, errors.New("the voice session start was cancelled before it began"))
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
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	vs, err := s.mgr.Stop(ctx, tenantID)
	if errors.Is(err, session.ErrNoActiveSession) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no active voice session"))
	}
	if errors.Is(err, session.ErrStopPending) {
		// Split mode (#491): the worker has not confirmed the wind-down within the
		// Stop budget. Unavailable so the operator retries — never a false success
		// carrying a still-'running' row.
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("the voice worker has not confirmed the stop yet; try again shortly"))
	}
	if err != nil {
		s.log.Error("StopSession: manager stop failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.StopSessionResponse{
		Session: toProtoVoiceSession(vs),
	}), nil
}

// SetAgentMute mutes or unmutes one voiced Agent of the Active Campaign in the
// live Voice Session (#211). It refuses when no session is active
// (CodeFailedPrecondition) and rejects an agent_id that is not a voiced Agent of
// the active session's campaign — a foreign agent, an unparsable id, or the
// Address-Only Butler (never voiced, ADR-0009/ADR-0024) — with CodeNotFound. The
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
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}

	ids, err := s.mgr.SetAgentMute(ctx, tenantID, req.Msg.GetAgentId(), req.Msg.GetMuted())
	switch {
	case errors.Is(err, session.ErrSplitMode):
		return nil, connect.NewError(connect.CodeFailedPrecondition, errSplitMode)
	case errors.Is(err, session.ErrControlPending):
		// #503: split-mode mutes relay through the claim plane; an unconfirmed
		// relay is retryable, never a false success (ADR-0012).
		return nil, connect.NewError(connect.CodeUnavailable, errControlPending)
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

// SetAllMute mutes or unmutes every voiced Agent of the Active Campaign (the
// Character NPCs; the Address-Only Butler is excluded) in the live Voice Session
// (#211). It refuses when no session is active (CodeFailedPrecondition).
func (s *SessionServer) SetAllMute(
	ctx context.Context,
	req *connect.Request[managementv1.SetAllMuteRequest],
) (*connect.Response[managementv1.SetAllMuteResponse], error) {
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := s.mgr.SetAllMute(ctx, tenantID, req.Msg.GetMuted())
	if errors.Is(err, session.ErrSplitMode) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errSplitMode)
	}
	if errors.Is(err, session.ErrControlPending) {
		// #503: see SetAgentMute — relayed control unconfirmed, retry.
		return nil, connect.NewError(connect.CodeUnavailable, errControlPending)
	}
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

// ListSessions returns the operator's past Voice Sessions for the resolved Active
// Campaign, newest-first, capped at listSessionsLimit (#270). It backs the Session
// screen's past-session picker. The Campaign is resolved server-side with the SAME
// searchCampaign policy SearchTranscriptLines uses — the live Voice Session's
// campaign first (so the picker lists the session on screen and its siblings), else
// the profile-first startCampaign (durable /glyphoxa use selection → most-recent
// fallback). NEVER a client-supplied id (AC2), so it can never list another
// campaign's sessions. No Active Campaign yields an empty list (never-run state,
// not an error); a storage failure is CodeInternal. A read (NO_SIDE_EFFECTS).
func (s *SessionServer) ListSessions(
	ctx context.Context,
	_ *connect.Request[managementv1.ListSessionsRequest],
) (*connect.Response[managementv1.ListSessionsResponse], error) {
	campaignID, ok, err := s.searchCampaign(ctx)
	if err != nil {
		s.log.Error("ListSessions: resolve active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if !ok {
		// No Active Campaign: nothing to list yet (never-run state), not an error.
		return connect.NewResponse(&managementv1.ListSessionsResponse{}), nil
	}

	sessions, err := s.store.ListVoiceSessions(ctx, campaignID, listSessionsLimit)
	if err != nil {
		s.log.Error("ListSessions: store list failed", "campaign_id", campaignID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	out := make([]*managementv1.VoiceSession, 0, len(sessions))
	for _, vs := range sessions {
		out = append(out, toProtoVoiceSession(vs))
	}
	return connect.NewResponse(&managementv1.ListSessionsResponse{Sessions: out}), nil
}

// GenerateRecap regenerates a Butler-flavoured Recap over the requested Voice
// Sessions (#274, gate #271: never persisted). The Campaign is resolved
// server-side via the SAME searchCampaign policy the reads use (live Voice
// Session first, else the profile-first durable selection / most-recent fallback)
// — no Active Campaign is CodeFailedPrecondition. Every session_id MUST belong to
// that resolved Campaign: an unparsable id, a storage.ErrNotFound, or a session
// whose CampaignID differs is CodeNotFound — the SAME never-leak-existence posture
// as SetAgentMute, so a probe can't learn whether a foreign id exists. Empty
// session_ids is CodeInvalidArgument. Recapping a RUNNING session is allowed: its
// Lines already exist and the snapshot simply grows, so the recap covers the
// transcript as of the call. recap.ErrNoTranscript (nothing to summarize) and
// recap.ErrTranscriptTooLong (a retry can never succeed) are both static
// CodeFailedPrecondition; any other engine error is logged and returned as a
// static CodeInternal. An over-cap session_ids count is CodeInvalidArgument (a
// spend guardrail). State-changing (spends money): auth + CSRF guard it.
func (s *SessionServer) GenerateRecap(
	ctx context.Context,
	req *connect.Request[managementv1.GenerateRecapRequest],
) (*connect.Response[managementv1.GenerateRecapResponse], error) {
	rawIDs := req.Msg.GetSessionIds()
	if len(rawIDs) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("at least one session id is required"))
	}
	// Belt-and-braces spend guard (gate #271): the web sends one id; an unreasonable
	// count would fan out that many money-spending LLM calls. Cap it well above any
	// realistic pick.
	if len(rawIDs) > maxRecapSessions {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("too many session ids to recap at once"))
	}

	// A foreign/unknown/unparsable id names no session in the Active Campaign — one
	// static NotFound for all three, so existence is never leaked (mirrors SetAgentMute).
	notFound := connect.NewError(connect.CodeNotFound, errors.New("no such session in the Active Campaign"))

	campaignID, ok, err := s.searchCampaign(ctx)
	if err != nil {
		s.log.Error("GenerateRecap: resolve active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if !ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no active campaign"))
	}

	sessionIDs := make([]uuid.UUID, 0, len(rawIDs))
	for _, raw := range rawIDs {
		id, perr := uuid.Parse(raw)
		if perr != nil {
			return nil, notFound // a non-UUID id names no session
		}
		vs, gerr := s.store.GetVoiceSession(ctx, id)
		if errors.Is(gerr, storage.ErrNotFound) {
			return nil, notFound
		}
		if gerr != nil {
			s.log.Error("GenerateRecap: load voice session failed", "err", gerr)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
		if vs.CampaignID != campaignID {
			return nil, notFound // cross-campaign id: never leak that it exists
		}
		sessionIDs = append(sessionIDs, id)
	}

	res, err := s.recapper.Recap(ctx, sessionIDs)
	if errors.Is(err, recap.ErrNoTranscript) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no transcript to summarize"))
	}
	if errors.Is(err, recap.ErrTranscriptTooLong) {
		// Deterministic + operator-meaningful (a retry can never succeed): a precondition,
		// not an internal fault. Name the condition without echoing internals.
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("transcript too long to recap"))
	}
	if err != nil {
		s.log.Error("GenerateRecap: recap engine failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	outIDs := make([]string, len(res.SessionIDs))
	for i, id := range res.SessionIDs {
		outIDs[i] = id.String()
	}
	return connect.NewResponse(&managementv1.GenerateRecapResponse{
		Text:       res.Text,
		SessionIds: outIDs,
		Windowed:   res.Windowed,
	}), nil
}

// ResolveActiveCampaign exposes the SessionServer's read-side Active-Campaign
// resolution (searchCampaign: live Voice Session first, else the profile-first
// durable selection) for out-of-band HTTP surfaces that share the posture — the
// Highlight clip byte stream (#308), a plain net/http mount that is not a Connect
// method and so cannot reach searchCampaign directly. ok is false when neither
// resolves (never-run state).
func (s *SessionServer) ResolveActiveCampaign(ctx context.Context) (uuid.UUID, bool, error) {
	return s.searchCampaign(ctx)
}

// searchCampaign resolves the campaign the web transcript search scopes to: the
// live Voice Session's campaign first (the same in-process truth GetSession uses,
// so search scopes to exactly the transcript on screen), otherwise the
// profile-first startCampaign (the operator's durable /glyphoxa use selection, else
// the most-recently-created fallback). ok is false only when neither resolves — a
// never-run state the caller answers with an empty result. A storage error other
// than ErrNotFound is returned.
func (s *SessionServer) searchCampaign(ctx context.Context) (uuid.UUID, bool, error) {
	if tenantID, ok := auth.TenantID(ctx); ok {
		if vs, active, err := s.mgr.Active(ctx, tenantID); err == nil && active {
			return vs.CampaignID, true, nil
		}
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
