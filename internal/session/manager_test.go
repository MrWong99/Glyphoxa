package session_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// fakeStore is an in-memory session.Store: it serves a canned deployment config
// (guild/channel source, #72) and records the voice_sessions Create/End calls so
// the lifecycle can be asserted without Postgres.
type fakeStore struct {
	mu       sync.Mutex
	dep      storage.DeploymentConfig
	depErr   error
	sessions map[uuid.UUID]storage.VoiceSession
	created  int
	ended    int
	tick     int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		dep: storage.DeploymentConfig{
			GuildID:        "111222333",
			VoiceChannelID: "444555666",
		},
		sessions: map[uuid.UUID]storage.VoiceSession{},
	}
}

func (f *fakeStore) now() time.Time {
	f.tick++
	return time.Date(2026, 6, 27, 12, 0, f.tick, 0, time.UTC)
}

func (f *fakeStore) GetDeploymentConfig(_ context.Context, _ uuid.UUID) (storage.DeploymentConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.depErr != nil {
		return storage.DeploymentConfig{}, f.depErr
	}
	return f.dep, nil
}

func (f *fakeStore) CreateVoiceSession(_ context.Context, campaignID uuid.UUID) (storage.VoiceSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created++
	vs := storage.VoiceSession{
		ID:         uuid.New(),
		CampaignID: campaignID,
		StartedAt:  f.now(),
		Status:     storage.VoiceSessionRunning,
	}
	f.sessions[vs.ID] = vs
	return vs, nil
}

func (f *fakeStore) EndVoiceSession(_ context.Context, id uuid.UUID, lineCount int) (storage.VoiceSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended++
	vs, ok := f.sessions[id]
	if !ok {
		return storage.VoiceSession{}, storage.ErrNotFound
	}
	end := f.now()
	vs.EndedAt = &end
	vs.Status = storage.VoiceSessionEnded
	vs.LineCount = lineCount
	f.sessions[id] = vs
	return vs, nil
}

func (f *fakeStore) counts() (created, ended int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created, f.ended
}

// blockingRunner is a fake loop runner that records the cfg it ran with and the
// fact it was cancelled, then unblocks on ctx cancellation — a stand-in for the
// real wirenpc loop so no Discord/Postgres is touched.
type blockingRunner struct {
	mu        sync.Mutex
	started   chan struct{} // closed once the runner is executing
	gotCfg    wirenpc.Config
	cancelled bool
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{started: make(chan struct{})}
}

func (r *blockingRunner) run(ctx context.Context, cfg wirenpc.Config) error {
	r.mu.Lock()
	r.gotCfg = cfg
	r.mu.Unlock()
	close(r.started)
	<-ctx.Done()
	r.mu.Lock()
	r.cancelled = true
	r.mu.Unlock()
	return ctx.Err()
}

func (r *blockingRunner) cfg() wirenpc.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gotCfg
}

func (r *blockingRunner) wasCancelled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelled
}

func newManager(t *testing.T, store session.Store, run session.LoopRunner, enabled bool) *session.Manager {
	t.Helper()
	return session.NewManager(store, run, wirenpc.Config{Token: "test-token"},
		slog.New(slog.DiscardHandler), enabled)
}

// TestStartStopLifecycle is AC1: Start → status running + row written + the loop
// runs; Stop → ended_at + status ended, and the cancellation propagates to the
// loop (the runner observes ctx.Done).
func TestStartStopLifecycle(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	mgr := newManager(t, store, runner.run, true)

	tenantID, campaignID := uuid.New(), uuid.New()
	vs, err := mgr.Start(context.Background(), tenantID, campaignID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if vs.Status != storage.VoiceSessionRunning {
		t.Errorf("started status = %q, want running", vs.Status)
	}
	if created, _ := store.counts(); created != 1 {
		t.Errorf("created rows = %d, want 1", created)
	}

	// The loop is running and was handed the saved guild/channel (#72: sourced
	// from the deployment config, not CLI flags).
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("loop runner never started")
	}
	if got := runner.cfg(); got.Guild != "111222333" || got.Channel != "444555666" {
		t.Errorf("loop cfg guild/channel = %q/%q, want saved 111222333/444555666", got.Guild, got.Channel)
	}

	// Snapshot reflects the active session.
	if snap, active := mgr.Snapshot(); !active || snap.ID != vs.ID {
		t.Errorf("Snapshot = %+v active=%v, want active %s", snap, active, vs.ID)
	}

	ended, err := mgr.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if ended.Status != storage.VoiceSessionEnded || ended.EndedAt == nil {
		t.Errorf("stopped session = %+v, want ended with ended_at", ended)
	}
	if _, e := store.counts(); e != 1 {
		t.Errorf("ended rows = %d, want 1", e)
	}
	if !runner.wasCancelled() {
		t.Error("cancellation did not propagate to the loop")
	}
	if _, active := mgr.Snapshot(); active {
		t.Error("Snapshot still active after Stop")
	}
}

// TestSecondStartRejected is AC2: a second Start while one is active is rejected
// (single-active-session guard), and no second row is written.
func TestSecondStartRejected(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	mgr := newManager(t, store, runner.run, true)

	tenantID, campaignID := uuid.New(), uuid.New()
	if _, err := mgr.Start(context.Background(), tenantID, campaignID); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	t.Cleanup(func() { _, _ = mgr.Stop(context.Background()) })

	_, err := mgr.Start(context.Background(), tenantID, campaignID)
	if !errors.Is(err, session.ErrSessionActive) {
		t.Errorf("second Start = %v, want ErrSessionActive", err)
	}
	if created, _ := store.counts(); created != 1 {
		t.Errorf("created rows = %d, want 1 (second Start must not write)", created)
	}
}

// TestStartRequiresDiscordConfig is the #72 precondition: an unset guild/channel
// fails Start with ErrDiscordNotConfigured and writes no row.
func TestStartRequiresDiscordConfig(t *testing.T) {
	store := newFakeStore()
	store.dep = storage.DeploymentConfig{} // no guild/channel
	mgr := newManager(t, store, newBlockingRunner().run, true)

	_, err := mgr.Start(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, session.ErrDiscordNotConfigured) {
		t.Errorf("Start with no guild/channel = %v, want ErrDiscordNotConfigured", err)
	}
	if created, _ := store.counts(); created != 0 {
		t.Errorf("created rows = %d, want 0", created)
	}
}

// TestStopWithoutActiveSession returns ErrNoActiveSession.
func TestStopWithoutActiveSession(t *testing.T) {
	mgr := newManager(t, newFakeStore(), newBlockingRunner().run, true)
	if _, err := mgr.Stop(context.Background()); !errors.Is(err, session.ErrNoActiveSession) {
		t.Errorf("Stop with no session = %v, want ErrNoActiveSession", err)
	}
}

// TestStartDisabledMode rejects Start when voice is not enabled for the mode
// (web-only, ADR-0039: all-mode drives sessions in-process).
func TestStartDisabledMode(t *testing.T) {
	mgr := newManager(t, newFakeStore(), newBlockingRunner().run, false)
	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, session.ErrVoiceUnavailable) {
		t.Errorf("Start in disabled mode = %v, want ErrVoiceUnavailable", err)
	}
}

// TestLoopExitClearsActive asserts a loop that exits on its own (not via Stop)
// still ends the session and clears the active slot, so the guard frees up.
func TestLoopExitClearsActive(t *testing.T) {
	store := newFakeStore()
	// A runner that returns immediately (loop ended by itself).
	mgr := newManager(t, store, func(_ context.Context, _ wirenpc.Config) error { return nil }, true)

	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Wait for the self-exiting loop to End the session and clear active.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, active := mgr.Snapshot(); !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("active session not cleared after the loop exited")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ended := store.counts(); ended != 1 {
		t.Errorf("ended rows = %d, want 1 after self-exit", ended)
	}
}
