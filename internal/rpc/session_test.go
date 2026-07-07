package rpc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeSessionManager mimics the SessionManager's single-active lifecycle so the
// handler's error mapping + snapshot wiring can be unit-tested without Discord.
type fakeSessionManager struct {
	mu               sync.Mutex
	active           bool
	current          storage.VoiceSession
	startErr         error
	stopErr          error
	startCalls       int
	muted            map[string]struct{}
	rosterIDs        []string     // ids SetAllMute mutes (the campaign roster the real Manager lists)
	campaignAgentIDs []string     // ids SetAgentMute accepts; others → ErrAgentNotInCampaign (Manager validates now)
	spend            spend.Status // the live meter snapshot GetSession surfaces (#130)
}

func (f *fakeSessionManager) Start(_ context.Context, _, campaignID uuid.UUID) (storage.VoiceSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	if f.startErr != nil {
		return storage.VoiceSession{}, f.startErr
	}
	if f.active {
		return storage.VoiceSession{}, session.ErrSessionActive
	}
	f.current = storage.VoiceSession{
		ID:         uuid.New(),
		CampaignID: campaignID,
		Status:     storage.VoiceSessionRunning,
		StartedAt:  time.Date(2026, 6, 27, 18, 0, 0, 0, time.UTC),
	}
	f.active = true
	return f.current, nil
}

func (f *fakeSessionManager) Stop(_ context.Context) (storage.VoiceSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopErr != nil {
		return storage.VoiceSession{}, f.stopErr
	}
	if !f.active {
		return storage.VoiceSession{}, session.ErrNoActiveSession
	}
	end := time.Date(2026, 6, 27, 19, 0, 0, 0, time.UTC)
	f.current.EndedAt = &end
	f.current.Status = storage.VoiceSessionEnded
	f.active = false
	return f.current, nil
}

func (f *fakeSessionManager) Snapshot() (storage.VoiceSession, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current, f.active
}

func (f *fakeSessionManager) SetAgentMute(_ context.Context, agentID string, muted bool) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.active {
		return nil, session.ErrNoActiveSession
	}
	if !f.inRoster(agentID) {
		return nil, session.ErrAgentNotInCampaign
	}
	if f.muted == nil {
		f.muted = map[string]struct{}{}
	}
	if muted {
		f.muted[agentID] = struct{}{}
	} else {
		delete(f.muted, agentID)
	}
	return f.mutedIDsLocked(), nil
}

func (f *fakeSessionManager) inRoster(agentID string) bool {
	for _, id := range f.campaignAgentIDs {
		if id == agentID {
			return true
		}
	}
	return false
}

func (f *fakeSessionManager) SetAllMute(_ context.Context, muted bool) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.active {
		return nil, session.ErrNoActiveSession
	}
	if muted {
		f.muted = map[string]struct{}{}
		for _, id := range f.rosterIDs {
			f.muted[id] = struct{}{}
		}
	} else {
		f.muted = map[string]struct{}{}
	}
	return f.mutedIDsLocked(), nil
}

func (f *fakeSessionManager) MutedAgentIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.active {
		return nil
	}
	return f.mutedIDsLocked()
}

func (f *fakeSessionManager) Spend() spend.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spend
}

func (f *fakeSessionManager) mutedIDsLocked() []string {
	if len(f.muted) == 0 {
		return nil
	}
	ids := make([]string, 0, len(f.muted))
	for id := range f.muted {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// fakeSessionStore serves the durable per-operator selection, the implicit active
// campaign, the latest ended session, and the campaign-scoped transcript search
// (#120), recording the campaign + query the handler resolved so the scope
// precedence can be asserted.
type fakeSessionStore struct {
	forUser        storage.Campaign // the operator's /glyphoxa use selection (#108)
	forUserErr     error            // set to storage.ErrNotFound to force the fallback
	campaign       storage.Campaign
	campaignErr    error
	latest         storage.VoiceSession
	latestErr      error
	latestCampaign uuid.UUID // the campaign id the idle GetSession resolved (#220)

	searchLines    []storage.TranscriptLine
	searchErr      error
	searchCampaign uuid.UUID // the campaign id the handler passed to search
	searchQuery    string
	searchCalls    int
}

func (f *fakeSessionStore) GetActiveCampaignForUser(context.Context, string) (storage.Campaign, error) {
	if f.forUserErr != nil {
		return storage.Campaign{}, f.forUserErr
	}
	if f.forUser.ID == uuid.Nil {
		return storage.Campaign{}, storage.ErrNotFound
	}
	return f.forUser, nil
}

func (f *fakeSessionStore) GetActiveCampaign(context.Context) (storage.Campaign, error) {
	if f.campaignErr != nil {
		return storage.Campaign{}, f.campaignErr
	}
	return f.campaign, nil
}

// GetCampaign is the live-first resolution's per-id load (#222). The SessionServer
// idle/Start paths never reach it with a live session (GetSession returns the live
// Snapshot directly; Start is guarded single-active), so a simple pass-through of
// the implicit campaign satisfies the interface.
func (f *fakeSessionStore) GetCampaign(context.Context, uuid.UUID) (storage.Campaign, error) {
	if f.campaignErr != nil {
		return storage.Campaign{}, f.campaignErr
	}
	return f.campaign, nil
}

func (f *fakeSessionStore) GetLatestVoiceSession(_ context.Context, campaignID uuid.UUID) (storage.VoiceSession, error) {
	f.latestCampaign = campaignID
	if f.latestErr != nil {
		return storage.VoiceSession{}, f.latestErr
	}
	return f.latest, nil
}

func (f *fakeSessionStore) SearchTranscriptLines(_ context.Context, campaignID uuid.UUID, query string, _ int) ([]storage.TranscriptLine, error) {
	f.searchCalls++
	f.searchCampaign = campaignID
	f.searchQuery = query
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.searchLines, nil
}

func newSessionClient(t *testing.T, mgr rpc.SessionManager, store rpc.SessionStore) managementv1connect.SessionServiceClient {
	return newSessionClientAs(t, mgr, store, storage.User{})
}

// newSessionClientAs is newSessionClient plus an injected authenticated operator,
// so StartSession's durable-selection lookup (#108) sees a Discord identity. A
// zero user injects only the tenant (the legacy no-user path).
func newSessionClientAs(t *testing.T, mgr rpc.SessionManager, store rpc.SessionStore, user storage.User) managementv1connect.SessionServiceClient {
	t.Helper()
	tenantID := uuid.New()
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			ctx = auth.WithTenant(ctx, tenantID)
			if user.DiscordUserID != "" {
				ctx = auth.WithUser(ctx, user)
			}
			return next(ctx, req)
		}
	})
	mux := http.NewServeMux()
	mux.Handle(rpc.NewSessionServer(mgr, store, nil).Handler(connect.WithInterceptors(inject)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return managementv1connect.NewSessionServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())
}

func activeStore() *fakeSessionStore {
	return &fakeSessionStore{
		campaign:  storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Sunless Citadel"},
		latestErr: storage.ErrNotFound,
	}
}

// TestSessionStartStopReflectsSnapshot is AC4's server half: GetSession reports
// idle, StartSession flips it to active/running, GetSession reflects Live, and
// StopSession returns the ended session with an ended_at.
func TestSessionStartStopReflectsSnapshot(t *testing.T) {
	t.Parallel()
	mgr := &fakeSessionManager{}
	client := newSessionClient(t, mgr, activeStore())
	ctx := context.Background()

	// Idle: no session has ever run.
	get, err := client.GetSession(ctx, connect.NewRequest(&managementv1.GetSessionRequest{}))
	if err != nil {
		t.Fatalf("GetSession idle: %v", err)
	}
	if get.Msg.GetActive() || get.Msg.GetSession() != nil {
		t.Errorf("idle GetSession = active %v session %v, want inactive/nil", get.Msg.GetActive(), get.Msg.GetSession())
	}

	// Start → running + active.
	start, err := client.StartSession(ctx, connect.NewRequest(&managementv1.StartSessionRequest{}))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if start.Msg.GetSession().GetStatus() != "running" {
		t.Errorf("started status = %q, want running", start.Msg.GetSession().GetStatus())
	}

	// GetSession now reflects Live.
	live, err := client.GetSession(ctx, connect.NewRequest(&managementv1.GetSessionRequest{}))
	if err != nil {
		t.Fatalf("GetSession live: %v", err)
	}
	if !live.Msg.GetActive() || live.Msg.GetSession().GetId() != start.Msg.GetSession().GetId() {
		t.Errorf("live GetSession = %+v, want active session %s", live.Msg, start.Msg.GetSession().GetId())
	}

	// Stop → ended with ended_at.
	stop, err := client.StopSession(ctx, connect.NewRequest(&managementv1.StopSessionRequest{}))
	if err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if stop.Msg.GetSession().GetStatus() != "ended" || stop.Msg.GetSession().GetEndedAt() == nil {
		t.Errorf("stopped session = %+v, want ended with ended_at", stop.Msg.GetSession())
	}
}

// activeMgr returns a fake manager already in an active session for campaignID,
// whose Campaign roster (SetAgentMute membership check) is agentIDs.
func activeMgr(t *testing.T, campaignID uuid.UUID, agentIDs ...string) *fakeSessionManager {
	t.Helper()
	mgr := &fakeSessionManager{campaignAgentIDs: agentIDs}
	if _, err := mgr.Start(context.Background(), uuid.New(), campaignID); err != nil {
		t.Fatalf("activate fake manager: %v", err)
	}
	return mgr
}

// TestSetAgentMute_Success mutes an Agent of the Active Campaign and returns the
// muted-id set, then unmutes it (#211).
func TestSetAgentMute_Success(t *testing.T) {
	t.Parallel()
	campaign := storage.Campaign{ID: uuid.New()}
	agent := storage.Agent{ID: uuid.New(), CampaignID: campaign.ID, Name: "Bart"}
	mgr := activeMgr(t, campaign.ID, agent.ID.String())
	store := &fakeSessionStore{campaign: campaign, latestErr: storage.ErrNotFound}
	client := newSessionClient(t, mgr, store)

	resp, err := client.SetAgentMute(context.Background(),
		connect.NewRequest(&managementv1.SetAgentMuteRequest{AgentId: agent.ID.String(), Muted: true}))
	if err != nil {
		t.Fatalf("SetAgentMute: %v", err)
	}
	if got := resp.Msg.GetMutedAgentIds(); len(got) != 1 || got[0] != agent.ID.String() {
		t.Fatalf("muted ids = %v, want [%s]", got, agent.ID)
	}

	resp, err = client.SetAgentMute(context.Background(),
		connect.NewRequest(&managementv1.SetAgentMuteRequest{AgentId: agent.ID.String(), Muted: false}))
	if err != nil {
		t.Fatalf("unmute: %v", err)
	}
	if got := resp.Msg.GetMutedAgentIds(); len(got) != 0 {
		t.Fatalf("muted ids after unmute = %v, want empty", got)
	}
}

// TestSetAgentMute_IdleFailedPrecondition maps the no-active-session refusal to
// FailedPrecondition (AC4).
func TestSetAgentMute_IdleFailedPrecondition(t *testing.T) {
	t.Parallel()
	client := newSessionClient(t, &fakeSessionManager{}, activeStore())
	_, err := client.SetAgentMute(context.Background(),
		connect.NewRequest(&managementv1.SetAgentMuteRequest{AgentId: uuid.NewString(), Muted: true}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("idle SetAgentMute code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
}

// TestSetAgentMute_UnknownAgentNotFound maps an agent_id that is not an Agent of
// the Active Campaign — or an unparsable id — to CodeNotFound.
func TestSetAgentMute_UnknownAgentNotFound(t *testing.T) {
	t.Parallel()
	campaign := storage.Campaign{ID: uuid.New()}
	inRoster := storage.Agent{ID: uuid.New(), CampaignID: campaign.ID}
	mgr := activeMgr(t, campaign.ID, inRoster.ID.String())
	store := &fakeSessionStore{campaign: campaign, latestErr: storage.ErrNotFound}
	client := newSessionClient(t, mgr, store)

	// A valid UUID that is not in the roster.
	_, err := client.SetAgentMute(context.Background(),
		connect.NewRequest(&managementv1.SetAgentMuteRequest{AgentId: uuid.NewString(), Muted: true}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("foreign-agent code = %v, want NotFound", connect.CodeOf(err))
	}
	// A non-UUID agent_id.
	_, err = client.SetAgentMute(context.Background(),
		connect.NewRequest(&managementv1.SetAgentMuteRequest{AgentId: "not-a-uuid", Muted: true}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("non-uuid code = %v, want NotFound", connect.CodeOf(err))
	}
}

// TestSetAllMute_Success mutes then unmutes every Agent of the Active Campaign.
func TestSetAllMute_Success(t *testing.T) {
	t.Parallel()
	campaign := storage.Campaign{ID: uuid.New()}
	mgr := activeMgr(t, campaign.ID)
	mgr.rosterIDs = []string{"aaa", "bbb"}
	client := newSessionClient(t, mgr, &fakeSessionStore{campaign: campaign, latestErr: storage.ErrNotFound})

	resp, err := client.SetAllMute(context.Background(),
		connect.NewRequest(&managementv1.SetAllMuteRequest{Muted: true}))
	if err != nil {
		t.Fatalf("SetAllMute: %v", err)
	}
	if got := resp.Msg.GetMutedAgentIds(); len(got) != 2 {
		t.Fatalf("muted ids after mute-all = %v, want 2", got)
	}

	resp, err = client.SetAllMute(context.Background(),
		connect.NewRequest(&managementv1.SetAllMuteRequest{Muted: false}))
	if err != nil {
		t.Fatalf("SetAllMute unmute: %v", err)
	}
	if got := resp.Msg.GetMutedAgentIds(); len(got) != 0 {
		t.Fatalf("muted ids after unmute-all = %v, want empty", got)
	}
}

// TestSetAllMute_IdleFailedPrecondition maps the no-active-session refusal.
func TestSetAllMute_IdleFailedPrecondition(t *testing.T) {
	t.Parallel()
	client := newSessionClient(t, &fakeSessionManager{}, activeStore())
	_, err := client.SetAllMute(context.Background(),
		connect.NewRequest(&managementv1.SetAllMuteRequest{Muted: true}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("idle SetAllMute code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
}

// TestGetSession_CarriesMutedAgentIds pins AC5's reload truth: GetSession carries
// the muted-Agent id set while active, and none when idle.
func TestGetSession_CarriesMutedAgentIds(t *testing.T) {
	t.Parallel()
	campaign := storage.Campaign{ID: uuid.New()}
	agent := storage.Agent{ID: uuid.New(), CampaignID: campaign.ID}
	mgr := activeMgr(t, campaign.ID, agent.ID.String())
	store := &fakeSessionStore{campaign: campaign, latestErr: storage.ErrNotFound}
	client := newSessionClient(t, mgr, store)

	if _, err := client.SetAgentMute(context.Background(),
		connect.NewRequest(&managementv1.SetAgentMuteRequest{AgentId: agent.ID.String(), Muted: true})); err != nil {
		t.Fatalf("mute: %v", err)
	}
	resp, err := client.GetSession(context.Background(), connect.NewRequest(&managementv1.GetSessionRequest{}))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got := resp.Msg.GetMutedAgentIds(); len(got) != 1 || got[0] != agent.ID.String() {
		t.Fatalf("live GetSession muted ids = %v, want [%s]", got, agent.ID)
	}

	// Idle: no muted ids.
	idle := newSessionClient(t, &fakeSessionManager{}, activeStore())
	resp, err = idle.GetSession(context.Background(), connect.NewRequest(&managementv1.GetSessionRequest{}))
	if err != nil {
		t.Fatalf("GetSession idle: %v", err)
	}
	if got := resp.Msg.GetMutedAgentIds(); len(got) != 0 {
		t.Fatalf("idle GetSession muted ids = %v, want empty", got)
	}
}

// TestSessionMute_CSRFGuardsMutationNotRead pins the mutation guard (#211): with
// the CSRF interceptor mounted and no double-submit token, the state-changing
// SetAgentMute/SetAllMute are rejected PermissionDenied, while the
// side-effect-free GetSession (NO_SIDE_EFFECTS) is exempt and reaches the handler.
// It fails if someone later mis-marks a mute RPC NO_SIDE_EFFECTS.
func TestSessionMute_CSRFGuardsMutationNotRead(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.Handle(rpc.NewSessionServer(&fakeSessionManager{}, activeStore(), nil).Handler(
		connect.WithInterceptors(auth.NewCSRFInterceptor()),
	))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := managementv1connect.NewSessionServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())
	ctx := context.Background()

	_, agentErr := client.SetAgentMute(ctx, connect.NewRequest(&managementv1.SetAgentMuteRequest{AgentId: uuid.NewString(), Muted: true}))
	if got := connect.CodeOf(agentErr); got != connect.CodePermissionDenied {
		t.Errorf("SetAgentMute code = %v, want PermissionDenied (CSRF-guarded mutation)", got)
	}
	_, allErr := client.SetAllMute(ctx, connect.NewRequest(&managementv1.SetAllMuteRequest{Muted: true}))
	if got := connect.CodeOf(allErr); got != connect.CodePermissionDenied {
		t.Errorf("SetAllMute code = %v, want PermissionDenied (CSRF-guarded mutation)", got)
	}
	// The read is exempt — no token still reaches the handler.
	if _, err := client.GetSession(ctx, connect.NewRequest(&managementv1.GetSessionRequest{})); connect.CodeOf(err) == connect.CodePermissionDenied {
		t.Error("GetSession must be CSRF-exempt (NO_SIDE_EFFECTS read)")
	}
}

// TestSessionStartAlreadyActive maps the single-active guard to CodeAlreadyExists.
func TestSessionStartAlreadyActive(t *testing.T) {
	t.Parallel()
	mgr := &fakeSessionManager{startErr: session.ErrSessionActive}
	client := newSessionClient(t, mgr, activeStore())

	_, err := client.StartSession(context.Background(), connect.NewRequest(&managementv1.StartSessionRequest{}))
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Errorf("already-active code = %v, want AlreadyExists", connect.CodeOf(err))
	}
}

// TestSessionStartDiscordUnconfigured maps the precondition to FailedPrecondition.
func TestSessionStartDiscordUnconfigured(t *testing.T) {
	t.Parallel()
	mgr := &fakeSessionManager{startErr: session.ErrDiscordNotConfigured}
	client := newSessionClient(t, mgr, activeStore())

	_, err := client.StartSession(context.Background(), connect.NewRequest(&managementv1.StartSessionRequest{}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("unconfigured code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
}

// TestSessionStartTokenMissing maps the #87 no-token precondition to
// FailedPrecondition (mirrors the guild/channel-unconfigured mapping).
func TestSessionStartTokenMissing(t *testing.T) {
	t.Parallel()
	mgr := &fakeSessionManager{startErr: session.ErrDiscordTokenMissing}
	client := newSessionClient(t, mgr, activeStore())

	_, err := client.StartSession(context.Background(), connect.NewRequest(&managementv1.StartSessionRequest{}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("token-missing code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
}

// TestSessionStartTokenUndecryptable maps the #87 undecryptable-token
// precondition to FailedPrecondition (the boot-without-$GLYPHOXA_SECRET misconfig
// must be actionable, not an opaque Internal).
func TestSessionStartTokenUndecryptable(t *testing.T) {
	t.Parallel()
	mgr := &fakeSessionManager{startErr: session.ErrDiscordTokenUndecryptable}
	client := newSessionClient(t, mgr, activeStore())

	_, err := client.StartSession(context.Background(), connect.NewRequest(&managementv1.StartSessionRequest{}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("token-undecryptable code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
}

// TestSessionStartManagerClosed is #157: a Start refused by the manager's
// terminal closed state (process shutting down) surfaces CodeUnavailable, not an
// opaque Internal — the client should retry against the restarted process.
func TestSessionStartManagerClosed(t *testing.T) {
	t.Parallel()
	mgr := &fakeSessionManager{startErr: session.ErrManagerClosed}
	client := newSessionClient(t, mgr, activeStore())

	_, err := client.StartSession(context.Background(), connect.NewRequest(&managementv1.StartSessionRequest{}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("manager-closed code = %v, want Unavailable", connect.CodeOf(err))
	}
}

// TestSessionStartNoCampaign fails with FailedPrecondition when there is no
// active campaign to run a session for.
func TestSessionStartNoCampaign(t *testing.T) {
	t.Parallel()
	mgr := &fakeSessionManager{}
	store := &fakeSessionStore{campaignErr: storage.ErrNotFound}
	client := newSessionClient(t, mgr, store)

	_, err := client.StartSession(context.Background(), connect.NewRequest(&managementv1.StartSessionRequest{}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("no-campaign code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
	if mgr.startCalls != 0 {
		t.Errorf("manager.Start called %d times, want 0 when no campaign", mgr.startCalls)
	}
}

// TestSessionStopNoActive maps ErrNoActiveSession to FailedPrecondition.
func TestSessionStopNoActive(t *testing.T) {
	t.Parallel()
	mgr := &fakeSessionManager{stopErr: session.ErrNoActiveSession}
	client := newSessionClient(t, mgr, activeStore())

	_, err := client.StopSession(context.Background(), connect.NewRequest(&managementv1.StopSessionRequest{}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("stop-no-active code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
}

// TestSessionGetIdleReturnsLastSession returns the most recent ended session when
// idle (the screen's last-session summary), with active=false.
func TestSessionGetIdleReturnsLastSession(t *testing.T) {
	t.Parallel()
	end := time.Date(2026, 6, 27, 17, 0, 0, 0, time.UTC)
	store := &fakeSessionStore{
		campaign: storage.Campaign{ID: uuid.New(), Name: "Sunless Citadel"},
		latest: storage.VoiceSession{
			ID: uuid.New(), Status: storage.VoiceSessionEnded, EndedAt: &end, LineCount: 12,
		},
	}
	client := newSessionClient(t, &fakeSessionManager{}, store)

	resp, err := client.GetSession(context.Background(), connect.NewRequest(&managementv1.GetSessionRequest{}))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if resp.Msg.GetActive() {
		t.Error("active = true, want false when idle")
	}
	got := resp.Msg.GetSession()
	if got == nil || got.GetStatus() != "ended" || got.GetLineCount() != 12 {
		t.Errorf("idle session = %+v, want ended with 12 lines", got)
	}
}

// TestSessionGetIdleReturnsFailedSessionReason is #123 (AC1/AC3 reload truth): a
// session that ended in a fatal gateway rejection is surfaced idle as status
// "failed" with its readable end_reason, so a page reload after a fatal start shows
// why. Proves toProtoVoiceSession maps EndReason and GetSession's idle path carries it.
func TestSessionGetIdleReturnsFailedSessionReason(t *testing.T) {
	t.Parallel()
	end := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	reason := "invalid_bot_token: wirenpc: open gateway: websocket: close 4004: Authentication failed"
	store := &fakeSessionStore{
		campaign: storage.Campaign{ID: uuid.New(), Name: "Sunless Citadel"},
		latest: storage.VoiceSession{
			ID: uuid.New(), Status: storage.VoiceSessionFailed, EndedAt: &end, EndReason: &reason,
		},
	}
	client := newSessionClient(t, &fakeSessionManager{}, store)

	resp, err := client.GetSession(context.Background(), connect.NewRequest(&managementv1.GetSessionRequest{}))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if resp.Msg.GetActive() {
		t.Error("active = true, want false when idle")
	}
	got := resp.Msg.GetSession()
	if got == nil || got.GetStatus() != "failed" {
		t.Fatalf("idle session = %+v, want failed", got)
	}
	if got.GetEndReason() != reason {
		t.Errorf("end_reason = %q, want %q", got.GetEndReason(), reason)
	}
}

// TestSessionStartHonorsDurableSelection is #108 web parity: with the operator's
// /glyphoxa use selection set (campaign A) AND a newer implicit default (campaign
// B), the web StartSession binds A — so the Session screen and the slash command
// agree on the campaign.
func TestSessionStartHonorsDurableSelection(t *testing.T) {
	t.Parallel()
	selected := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Selected"}
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer"}
	store := &fakeSessionStore{forUser: selected, campaign: newer, latestErr: storage.ErrNotFound}
	client := newSessionClientAs(t, &fakeSessionManager{}, store, storage.User{DiscordUserID: "999"})

	start, err := client.StartSession(context.Background(), connect.NewRequest(&managementv1.StartSessionRequest{}))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if start.Msg.GetSession().GetCampaignId() != selected.ID.String() {
		t.Errorf("bound campaign = %s, want the durable selection %s", start.Msg.GetSession().GetCampaignId(), selected.ID)
	}
}

// TestSessionStartFallsBackWithoutSelection pins the existing web behavior: an
// operator with no /glyphoxa use selection falls back to the most-recently-created
// campaign (GetActiveCampaign).
func TestSessionStartFallsBackWithoutSelection(t *testing.T) {
	t.Parallel()
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer"}
	store := &fakeSessionStore{forUserErr: storage.ErrNotFound, campaign: newer, latestErr: storage.ErrNotFound}
	client := newSessionClientAs(t, &fakeSessionManager{}, store, storage.User{DiscordUserID: "999"})

	start, err := client.StartSession(context.Background(), connect.NewRequest(&managementv1.StartSessionRequest{}))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if start.Msg.GetSession().GetCampaignId() != newer.ID.String() {
		t.Errorf("bound campaign = %s, want the fallback %s", start.Msg.GetSession().GetCampaignId(), newer.ID)
	}
}

// TestSessionGetIdleHonorsDurableSelection is #220: with the operator's /glyphoxa
// use selection set (campaign A) AND a newer implicit default (campaign B) and no
// live session, the idle GetSession last-session summary resolves campaign A — the
// SAME profile-first startCampaign StartSession binds — so the Session screen never
// describes campaign B while Start would run A. Repro: /use A, newer B exists, no
// session running → idle summary must scope to A (GetLatestVoiceSession(A)).
func TestSessionGetIdleHonorsDurableSelection(t *testing.T) {
	t.Parallel()
	selected := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Selected A"}
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer B"}
	store := &fakeSessionStore{forUser: selected, campaign: newer, latestErr: storage.ErrNotFound}
	client := newSessionClientAs(t, &fakeSessionManager{}, store, storage.User{DiscordUserID: "999"})

	if _, err := client.GetSession(context.Background(), connect.NewRequest(&managementv1.GetSessionRequest{})); err != nil {
		t.Fatalf("GetSession idle: %v", err)
	}
	if store.latestCampaign != selected.ID {
		t.Errorf("idle summary scoped to %s, want the durable selection %s (not the newer default %s)",
			store.latestCampaign, selected.ID, newer.ID)
	}
}

// TestSessionGetIdleFallsBackWithoutSelection pins the fallback half of #220: an
// operator with no /glyphoxa use selection falls back to the most-recently-created
// campaign (GetActiveCampaign) for the idle summary — mirroring StartSession, so the
// deleted-campaign SET NULL path (selection → ErrNotFound) still surfaces a summary.
func TestSessionGetIdleFallsBackWithoutSelection(t *testing.T) {
	t.Parallel()
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer B"}
	store := &fakeSessionStore{forUserErr: storage.ErrNotFound, campaign: newer, latestErr: storage.ErrNotFound}
	client := newSessionClientAs(t, &fakeSessionManager{}, store, storage.User{DiscordUserID: "999"})

	if _, err := client.GetSession(context.Background(), connect.NewRequest(&managementv1.GetSessionRequest{})); err != nil {
		t.Fatalf("GetSession idle: %v", err)
	}
	if store.latestCampaign != newer.ID {
		t.Errorf("idle summary scoped to %s, want the fallback %s", store.latestCampaign, newer.ID)
	}
}

// TestSearchTranscriptIdleScopesToActiveCampaign is #120 AC1 + AC5 (server, idle):
// with no live session, the search resolves the operator's Active Campaign via
// GetActiveCampaign, passes THAT campaign id to the one storage search method, and
// maps the ranked rows to the wire hits (speaker/tag/kind/ts/text + session/line
// id for deep-linking).
func TestSearchTranscriptIdleScopesToActiveCampaign(t *testing.T) {
	t.Parallel()
	campaignID := uuid.New()
	sessionID := uuid.New()
	store := &fakeSessionStore{
		campaign: storage.Campaign{ID: campaignID, Name: "Sunless Citadel"},
		searchLines: []storage.TranscriptLine{
			{VoiceSessionID: sessionID, CampaignID: campaignID, LineID: "a:t1", Seq: 2, Who: "Bart", Tag: "NPC", Kind: "npc", TS: time.Date(2026, 6, 27, 18, 0, 2, 0, time.UTC), Text: "Well met, traveller."},
		},
	}
	client := newSessionClient(t, &fakeSessionManager{}, store)

	resp, err := client.SearchTranscriptLines(context.Background(),
		connect.NewRequest(&managementv1.SearchTranscriptLinesRequest{Query: "dragon"}))
	if err != nil {
		t.Fatalf("SearchTranscriptLines: %v", err)
	}
	if store.searchCampaign != campaignID {
		t.Errorf("searched campaign = %s, want the Active Campaign %s (AC5 scope)", store.searchCampaign, campaignID)
	}
	if store.searchQuery != "dragon" {
		t.Errorf("searched query = %q, want %q (raw query passed through to the shared path)", store.searchQuery, "dragon")
	}
	lines := resp.Msg.GetLines()
	if len(lines) != 1 {
		t.Fatalf("got %d hits, want 1", len(lines))
	}
	m := lines[0]
	if m.GetSessionId() != sessionID.String() || m.GetLineId() != "a:t1" || m.GetWho() != "Bart" ||
		m.GetTag() != "NPC" || m.GetKind() != "npc" || m.GetText() != "Well met, traveller." {
		t.Errorf("hit = %+v, want the mapped Bart NPC line with its session + line id", m)
	}
}

// TestSearchTranscriptPrefersLiveSessionCampaign is #120's live-session precedence
// (restored after the #108 alignment dropped it): while a Voice Session is live the
// web search scopes to the LIVE session's campaign — exactly like GetSession, which
// renders that session's transcript — so it never diverges from what is on screen,
// even if the durable /glyphoxa use selection was changed mid-session (AC5). Repro:
// /use B → start (live B) → /use A must still search B, not A.
func TestSearchTranscriptPrefersLiveSessionCampaign(t *testing.T) {
	t.Parallel()
	liveCampaign := uuid.New()
	durable := storage.Campaign{ID: uuid.New(), Name: "Durable A"} // a since-changed /glyphoxa use selection
	legacy := storage.Campaign{ID: uuid.New(), Name: "Most recent"}
	mgr := &fakeSessionManager{
		active:  true,
		current: storage.VoiceSession{ID: uuid.New(), CampaignID: liveCampaign, Status: storage.VoiceSessionRunning},
	}
	// Both the durable selection and the legacy default differ from the live
	// session; the live session must win over both (Snapshot before startCampaign).
	store := &fakeSessionStore{forUser: durable, campaign: legacy, latestErr: storage.ErrNotFound}
	client := newSessionClientAs(t, mgr, store, storage.User{DiscordUserID: "999"})

	if _, err := client.SearchTranscriptLines(context.Background(),
		connect.NewRequest(&managementv1.SearchTranscriptLinesRequest{Query: "dragon"})); err != nil {
		t.Fatalf("SearchTranscriptLines: %v", err)
	}
	if store.searchCampaign != liveCampaign {
		t.Errorf("searched campaign = %s, want the LIVE session's %s (not the durable %s or legacy %s)",
			store.searchCampaign, liveCampaign, durable.ID, legacy.ID)
	}
}

// TestSearchTranscriptHonorsDurableSelection is #120 aligned to #108: with NO live
// session the web search resolves the campaign with the SAME profile-first
// startCampaign path as StartSession — the logged-in operator's durable /glyphoxa
// use selection outranks the most-recently-created default, so the web search box
// and the Start button always agree on which campaign is searched (AC5).
func TestSearchTranscriptHonorsDurableSelection(t *testing.T) {
	t.Parallel()
	selected := storage.Campaign{ID: uuid.New(), Name: "Selected"}
	newer := storage.Campaign{ID: uuid.New(), Name: "Newer"}
	store := &fakeSessionStore{forUser: selected, campaign: newer, latestErr: storage.ErrNotFound}
	client := newSessionClientAs(t, &fakeSessionManager{}, store, storage.User{DiscordUserID: "999"})

	if _, err := client.SearchTranscriptLines(context.Background(),
		connect.NewRequest(&managementv1.SearchTranscriptLinesRequest{Query: "dragon"})); err != nil {
		t.Fatalf("SearchTranscriptLines: %v", err)
	}
	if store.searchCampaign != selected.ID {
		t.Errorf("searched campaign = %s, want the durable selection %s (profile-first, not the fallback %s)",
			store.searchCampaign, selected.ID, newer.ID)
	}
}

// TestSearchTranscriptEmptyQueryIsInvalidArgument mirrors SearchNodes: an
// empty/whitespace query is CodeInvalidArgument and never reaches storage.
func TestSearchTranscriptEmptyQueryIsInvalidArgument(t *testing.T) {
	t.Parallel()
	store := &fakeSessionStore{campaign: storage.Campaign{ID: uuid.New()}}
	client := newSessionClient(t, &fakeSessionManager{}, store)

	for _, q := range []string{"", "   "} {
		_, err := client.SearchTranscriptLines(context.Background(),
			connect.NewRequest(&managementv1.SearchTranscriptLinesRequest{Query: q}))
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Errorf("SearchTranscriptLines(%q) code = %v, want InvalidArgument", q, got)
		}
	}
	if store.searchCalls != 0 {
		t.Errorf("store.SearchTranscriptLines called %d times for empty queries, want 0", store.searchCalls)
	}
}

// TestSearchTranscriptNoCampaignReturnsEmpty: with no live session and no Active
// Campaign (never-run state), the search returns an empty result gracefully — not
// an error — and never reaches storage.
func TestSearchTranscriptNoCampaignReturnsEmpty(t *testing.T) {
	t.Parallel()
	store := &fakeSessionStore{campaignErr: storage.ErrNotFound}
	client := newSessionClient(t, &fakeSessionManager{}, store)

	resp, err := client.SearchTranscriptLines(context.Background(),
		connect.NewRequest(&managementv1.SearchTranscriptLinesRequest{Query: "dragon"}))
	if err != nil {
		t.Fatalf("SearchTranscriptLines with no campaign = %v, want nil (graceful empty)", err)
	}
	if len(resp.Msg.GetLines()) != 0 {
		t.Errorf("got %d hits, want 0 when there is no Active Campaign", len(resp.Msg.GetLines()))
	}
	if store.searchCalls != 0 {
		t.Errorf("store.SearchTranscriptLines called %d times with no campaign, want 0", store.searchCalls)
	}
}

// TestSearchTranscriptAuthGatesLikeSiblings (auth half): the whole SessionService
// is auth-gated (ADR-0016) — with no valid session BOTH the SearchTranscriptLines
// read and the StartSession mutation are Unauthenticated. The read being
// side-effect-free exempts it from CSRF, never from auth.
func TestSearchTranscriptAuthGatesLikeSiblings(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.Handle(rpc.NewSessionServer(&fakeSessionManager{}, activeStore(), nil).Handler(
		connect.WithInterceptors(auth.NewAuthInterceptor(denyAuth{})),
	))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := managementv1connect.NewSessionServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())
	ctx := context.Background()

	_, searchErr := client.SearchTranscriptLines(ctx, connect.NewRequest(&managementv1.SearchTranscriptLinesRequest{Query: "dragon"}))
	_, startErr := client.StartSession(ctx, connect.NewRequest(&managementv1.StartSessionRequest{}))
	for name, err := range map[string]error{"SearchTranscriptLines": searchErr, "StartSession(sibling)": startErr} {
		if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
			t.Errorf("%s code = %v, want Unauthenticated (whole API is auth-gated)", name, got)
		}
	}
}

// TestSearchTranscriptCSRFExemptAsRead (CSRF half): with the CSRF interceptor
// mounted and no double-submit token, the state-changing StartSession is
// PermissionDenied while the side-effect-free SearchTranscriptLines (NO_SIDE_EFFECTS)
// is exempt and reaches the handler.
func TestSearchTranscriptCSRFExemptAsRead(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.Handle(rpc.NewSessionServer(&fakeSessionManager{}, activeStore(), nil).Handler(
		connect.WithInterceptors(auth.NewCSRFInterceptor()),
	))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := managementv1connect.NewSessionServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())
	ctx := context.Background()

	// The mutation is CSRF-guarded — no token → PermissionDenied.
	_, startErr := client.StartSession(ctx, connect.NewRequest(&managementv1.StartSessionRequest{}))
	if got := connect.CodeOf(startErr); got != connect.CodePermissionDenied {
		t.Errorf("StartSession code = %v, want PermissionDenied (CSRF-guarded mutation)", got)
	}
	// The read is exempt — no token still reaches the handler.
	if _, err := client.SearchTranscriptLines(ctx, connect.NewRequest(&managementv1.SearchTranscriptLinesRequest{Query: "dragon"})); connect.CodeOf(err) == connect.CodePermissionDenied {
		t.Error("SearchTranscriptLines must be CSRF-exempt (NO_SIDE_EFFECTS read)")
	}
}
