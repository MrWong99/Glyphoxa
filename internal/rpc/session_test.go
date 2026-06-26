package rpc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeSessionManager mimics the SessionManager's single-active lifecycle so the
// handler's error mapping + snapshot wiring can be unit-tested without Discord.
type fakeSessionManager struct {
	mu         sync.Mutex
	active     bool
	current    storage.VoiceSession
	startErr   error
	stopErr    error
	startCalls int
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

// fakeSessionStore serves the active campaign and the latest ended session.
type fakeSessionStore struct {
	campaign    storage.Campaign
	campaignErr error
	latest      storage.VoiceSession
	latestErr   error
}

func (f *fakeSessionStore) GetActiveCampaign(context.Context) (storage.Campaign, error) {
	if f.campaignErr != nil {
		return storage.Campaign{}, f.campaignErr
	}
	return f.campaign, nil
}

func (f *fakeSessionStore) GetLatestVoiceSession(context.Context, uuid.UUID) (storage.VoiceSession, error) {
	if f.latestErr != nil {
		return storage.VoiceSession{}, f.latestErr
	}
	return f.latest, nil
}

func newSessionClient(t *testing.T, mgr rpc.SessionManager, store rpc.SessionStore) managementv1connect.SessionServiceClient {
	t.Helper()
	tenantID := uuid.New()
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			return next(auth.WithTenant(ctx, tenantID), req)
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
