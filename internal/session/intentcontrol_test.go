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
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeControlStore is an in-memory session.IntentControlStore built on top of a
// fakeIntentStore's claim-plane semantics plus a voice_sessions map — the union a
// real *storage.Store presents to BOTH IntentControl (web tier) and ClaimLoop
// (worker), so the end-to-end test wires the two halves through one store exactly
// as production does through one DB.
type fakeControlStore struct {
	*fakeIntentStore
	mu       sync.Mutex
	sessions map[uuid.UUID]storage.VoiceSession
	// onGet, when set, runs against an intent on each GetVoiceSessionIntent — a hook
	// to simulate an external transition (e.g. a stop landing on a pending row) mid
	// Start poll. Guarded by the fakeIntentStore mutex the caller already holds.
	onGet func(*storage.VoiceSessionIntent)
}

func newFakeControlStore() *fakeControlStore {
	return &fakeControlStore{
		fakeIntentStore: newFakeIntentStore(),
		sessions:        map[uuid.UUID]storage.VoiceSession{},
	}
}

// putSession records a voice_sessions row the worker's Manager created, so
// IntentControl can load it once the intent goes live.
func (f *fakeControlStore) putSession(vs storage.VoiceSession) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[vs.ID] = vs
}

func (f *fakeControlStore) CreateVoiceSessionIntent(_ context.Context, tenantID, campaignID uuid.UUID) (storage.VoiceSessionIntent, error) {
	f.fakeIntentStore.mu.Lock()
	for _, i := range f.intents {
		if i.TenantID == tenantID &&
			(i.Status == storage.VoiceIntentPending || i.Status == storage.VoiceIntentClaimed || i.Status == storage.VoiceIntentLive) {
			f.fakeIntentStore.mu.Unlock()
			return storage.VoiceSessionIntent{}, storage.ErrIntentActive
		}
	}
	f.fakeIntentStore.mu.Unlock()
	return *f.add(tenantID, campaignID), nil
}

func (f *fakeControlStore) GetVoiceSessionIntent(_ context.Context, id uuid.UUID) (storage.VoiceSessionIntent, error) {
	f.fakeIntentStore.mu.Lock()
	defer f.fakeIntentStore.mu.Unlock()
	i, ok := f.intents[id]
	if !ok {
		return storage.VoiceSessionIntent{}, storage.ErrNotFound
	}
	if f.onGet != nil {
		f.onGet(i)
	}
	return *i, nil
}

func (f *fakeControlStore) RequestVoiceSessionStop(_ context.Context, id uuid.UUID) (storage.VoiceSessionIntent, error) {
	f.fakeIntentStore.mu.Lock()
	defer f.fakeIntentStore.mu.Unlock()
	i, ok := f.intents[id]
	if !ok {
		return storage.VoiceSessionIntent{}, storage.ErrNotFound
	}
	if i.Status == storage.VoiceIntentPending {
		now := time.Now()
		i.Status = storage.VoiceIntentDone
		i.EndedAt = &now
	}
	i.StopRequested = true
	return *i, nil
}

func (f *fakeControlStore) GetLiveVoiceSessionIntentForTenant(_ context.Context, tenantID uuid.UUID) (storage.VoiceSessionIntent, error) {
	f.fakeIntentStore.mu.Lock()
	defer f.fakeIntentStore.mu.Unlock()
	for _, i := range f.intents {
		if i.TenantID == tenantID &&
			(i.Status == storage.VoiceIntentPending || i.Status == storage.VoiceIntentClaimed || i.Status == storage.VoiceIntentLive) {
			return *i, nil
		}
	}
	return storage.VoiceSessionIntent{}, storage.ErrNotFound
}

func (f *fakeControlStore) GetVoiceSession(_ context.Context, id uuid.UUID) (storage.VoiceSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	vs, ok := f.sessions[id]
	if !ok {
		return storage.VoiceSession{}, storage.ErrNotFound
	}
	return vs, nil
}

func (f *fakeControlStore) IsCampaignLiveIntent(_ context.Context, campaignID uuid.UUID) (bool, error) {
	f.fakeIntentStore.mu.Lock()
	defer f.fakeIntentStore.mu.Unlock()
	for _, i := range f.intents {
		if i.CampaignID == campaignID &&
			(i.Status == storage.VoiceIntentPending || i.Status == storage.VoiceIntentClaimed || i.Status == storage.VoiceIntentLive) {
			return true, nil
		}
	}
	return false, nil
}

// reapExpiry is the age threshold the test uses; a heartbeat older than now minus
// this is stale. Tests set an intent's heartbeat via markStaleHeartbeat.
func (f *fakeControlStore) ReapVoiceSessionIntentIfExpired(_ context.Context, id uuid.UUID, expiry time.Duration) (bool, error) {
	f.intents_mu().Lock()
	defer f.intents_mu().Unlock()
	i, ok := f.intents[id]
	if !ok || (i.Status != storage.VoiceIntentClaimed && i.Status != storage.VoiceIntentLive) {
		return false, nil
	}
	if i.HeartbeatAt == nil || time.Since(*i.HeartbeatAt) < expiry {
		return false, nil
	}
	now := time.Now()
	i.Status = storage.VoiceIntentDead
	i.LastError = "worker heartbeat expired"
	i.EndedAt = &now
	return true, nil
}

// markStaleHeartbeat ages an intent's heartbeat so ReapVoiceSessionIntentIfExpired
// treats it as a dead worker's row.
func (f *fakeControlStore) markStaleHeartbeat(id uuid.UUID) {
	f.intents_mu().Lock()
	defer f.intents_mu().Unlock()
	old := time.Now().Add(-time.Hour)
	f.intents[id].HeartbeatAt = &old
}

// intents_mu exposes the embedded fakeIntentStore mutex for the control-store
// methods that touch the shared intent map.
func (f *fakeControlStore) intents_mu() *sync.Mutex { return &f.fakeIntentStore.mu }

func (f *fakeControlStore) AnyLiveVoiceSessionIntent(_ context.Context) (bool, error) {
	f.fakeIntentStore.mu.Lock()
	defer f.fakeIntentStore.mu.Unlock()
	for _, i := range f.intents {
		switch i.Status {
		case storage.VoiceIntentPending, storage.VoiceIntentClaimed, storage.VoiceIntentLive:
			return true, nil
		}
	}
	return false, nil
}

func newIntentControl(t *testing.T, store session.IntentControlStore) *session.IntentControl {
	t.Helper()
	return session.NewIntentControl(store, slog.New(slog.DiscardHandler),
		session.IntentControlConfig{Poll: time.Millisecond, StartBudget: 100 * time.Millisecond, StopBudget: time.Second})
}

// TestIntentControl_StartTimesOutPending covers sequence (10) timeout leg: no
// worker claims the intent, so Start returns ErrIntentPending (→ CodeUnavailable).
func TestIntentControl_StartTimesOutPending(t *testing.T) {
	store := newFakeControlStore()
	ctl := newIntentControl(t, store)
	_, err := ctl.Start(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, session.ErrIntentPending) {
		t.Fatalf("Start err = %v, want ErrIntentPending", err)
	}
}

// TestIntentControl_StartDuplicateActive covers the per-tenant guard: a second
// Start while an intent is non-terminal maps to ErrSessionActive.
func TestIntentControl_StartDuplicateActive(t *testing.T) {
	store := newFakeControlStore()
	ctl := newIntentControl(t, store)
	tenantID := uuid.New()
	store.add(tenantID, uuid.New()) // a standing pending intent for the tenant
	_, err := ctl.Start(context.Background(), tenantID, uuid.New())
	if !errors.Is(err, session.ErrSessionActive) {
		t.Fatalf("duplicate Start err = %v, want ErrSessionActive", err)
	}
}

// TestIntentControl_ActiveOnlyWhenLive asserts Active reports false for a
// pending/claimed intent and true (with the row) once live.
func TestIntentControl_ActiveOnlyWhenLive(t *testing.T) {
	store := newFakeControlStore()
	ctl := newIntentControl(t, store)
	tenantID, campaignID := uuid.New(), uuid.New()
	intent := store.add(tenantID, campaignID)

	if _, active, _ := ctl.Active(context.Background(), tenantID); active {
		t.Fatal("Active true for a pending intent")
	}

	// Drive it live with a bound voice_sessions row (as the worker would).
	claimed, _ := store.ClaimVoiceSessionIntent(context.Background(), "worker-1")
	vs := storage.VoiceSession{ID: uuid.New(), CampaignID: campaignID, Status: storage.VoiceSessionRunning}
	store.putSession(vs)
	if _, err := store.MarkVoiceSessionIntentLive(context.Background(), claimed.ID, "worker-1", vs.ID); err != nil {
		t.Fatalf("mark live: %v", err)
	}
	got, active, err := ctl.Active(context.Background(), tenantID)
	if err != nil || !active || got.ID != vs.ID {
		t.Fatalf("Active = (%s,%v,%v), want live row %s", got.ID, active, err, vs.ID)
	}
	_ = intent
}

// TestIntentControl_EndToEndWithClaimLoop covers sequence (10) end-to-end: the
// web tier's IntentControl.Start and a worker's in-process ClaimLoop wired
// through ONE store — Start writes the intent, the loop claims + starts + marks
// live, Start returns the live row; then Stop flags it, the loop winds down, Stop
// returns the closed row.
func TestIntentControl_EndToEndWithClaimLoop(t *testing.T) {
	store := newFakeControlStore()

	// The worker half: a Manager over a fake voice-loop, and a ClaimLoop that
	// records the voice_sessions rows the Manager creates into the shared store so
	// IntentControl can load them (mirroring one *storage.Store serving both).
	mstore := &recordingStore{fakeStore: newFakeStore(), control: store}
	runner := newBlockingRunner()
	mgr := newManager(t, mstore, runner.run, true)
	loop := newClaimLoop(t, store, mgr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopDone := make(chan struct{})
	go func() { loop.Run(ctx); close(loopDone) }()

	ctl := session.NewIntentControl(store, slog.New(slog.DiscardHandler),
		session.IntentControlConfig{Poll: time.Millisecond, StartBudget: 3 * time.Second, StopBudget: 3 * time.Second})

	tenantID, campaignID := uuid.New(), uuid.New()
	vs, err := ctl.Start(ctx, tenantID, campaignID)
	if err != nil {
		t.Fatalf("Start end-to-end: %v", err)
	}
	if vs.CampaignID != campaignID || vs.Status != storage.VoiceSessionRunning {
		t.Fatalf("Start returned %+v, want a running row for campaign %s", vs, campaignID)
	}

	// The web tier now sees the live session.
	if _, active, _ := ctl.Active(ctx, tenantID); !active {
		t.Fatal("Active false after Start went live")
	}

	// Stop from the web tier: flag the intent, the loop winds the session down.
	closed, err := ctl.Stop(ctx, tenantID)
	if err != nil {
		t.Fatalf("Stop end-to-end: %v", err)
	}
	if closed.ID != vs.ID {
		t.Fatalf("Stop returned session %s, want %s", closed.ID, vs.ID)
	}
	if !runner.wasCancelled() {
		t.Fatal("worker loop not cancelled by the stop flag")
	}

	cancel()
	<-loopDone
}

// recordingStore adapts a fakeStore (the Manager's session.Store) so every
// voice_sessions row it creates/closes is also mirrored into the shared
// fakeControlStore, so IntentControl reads the same rows the worker's Manager
// wrote — as one *storage.Store would.
type recordingStore struct {
	*fakeStore
	control *fakeControlStore
}

func (r *recordingStore) CreateVoiceSession(ctx context.Context, campaignID uuid.UUID) (storage.VoiceSession, error) {
	vs, err := r.fakeStore.CreateVoiceSession(ctx, campaignID)
	if err == nil {
		r.control.putSession(vs)
	}
	return vs, err
}

func (r *recordingStore) CloseVoiceSession(ctx context.Context, id uuid.UUID, status storage.VoiceSessionStatus, lineCount int, endReason *string) (storage.VoiceSession, error) {
	vs, err := r.fakeStore.CloseVoiceSession(ctx, id, status, lineCount, endReason)
	if err == nil {
		r.control.putSession(vs)
	}
	return vs, err
}

// TestIntentControl_StartCancelsPendingOnTimeout covers review item 3: when no
// worker claims within the Start budget, IntentControl cancels the pending intent
// (so a retry does not 23505) and returns ErrIntentPending.
func TestIntentControl_StartCancelsPendingOnTimeout(t *testing.T) {
	store := newFakeControlStore()
	ctl := session.NewIntentControl(store, slog.New(slog.DiscardHandler),
		session.IntentControlConfig{Poll: time.Millisecond, StartBudget: 20 * time.Millisecond, StopBudget: time.Second, Expiry: 30 * time.Second})
	tenantID, campaignID := uuid.New(), uuid.New()

	_, err := ctl.Start(context.Background(), tenantID, campaignID)
	if !errors.Is(err, session.ErrIntentPending) {
		t.Fatalf("Start err = %v, want ErrIntentPending", err)
	}
	// The pending intent was cancelled to 'done', so a fresh Start does not collide.
	if _, err := ctl.Start(context.Background(), tenantID, campaignID); !errors.Is(err, session.ErrIntentPending) {
		t.Fatalf("retry Start err = %v, want ErrIntentPending (no 23505 dead-end)", err)
	}
}

// TestIntentControl_StartCancelledOutcome covers review item 7: an external stop
// landing on the still-pending row is a distinct ErrIntentCancelled, not
// ErrIntentPending.
func TestIntentControl_StartCancelledOutcome(t *testing.T) {
	store := newFakeControlStore()
	// On the first poll, flip the pending intent straight to done (an external stop).
	store.onGet = func(i *storage.VoiceSessionIntent) {
		if i.Status == storage.VoiceIntentPending {
			now := time.Now()
			i.Status = storage.VoiceIntentDone
			i.EndedAt = &now
		}
	}
	ctl := session.NewIntentControl(store, slog.New(slog.DiscardHandler),
		session.IntentControlConfig{Poll: time.Millisecond, StartBudget: time.Second, StopBudget: time.Second, Expiry: 30 * time.Second})
	_, err := ctl.Start(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, session.ErrIntentCancelled) {
		t.Fatalf("Start err = %v, want ErrIntentCancelled", err)
	}
}

// TestIntentControl_ZeroWorkerReapsStaleBlocker covers review item 4: a Start
// blocked by a dead worker's stale claimed/live intent reaps it and proceeds
// (no worker present, so it then times out) — never a permanent AlreadyExists.
func TestIntentControl_ZeroWorkerReapsStaleBlocker(t *testing.T) {
	store := newFakeControlStore()
	ctl := session.NewIntentControl(store, slog.New(slog.DiscardHandler),
		session.IntentControlConfig{Poll: time.Millisecond, StartBudget: 20 * time.Millisecond, StopBudget: time.Second, Expiry: time.Millisecond})
	tenantID, campaignID := uuid.New(), uuid.New()

	old := store.add(tenantID, campaignID)
	claimed, _ := store.ClaimVoiceSessionIntent(context.Background(), "dead-worker")
	vs := storage.VoiceSession{ID: uuid.New(), CampaignID: campaignID, Status: storage.VoiceSessionRunning}
	store.putSession(vs)
	if _, err := store.MarkVoiceSessionIntentLive(context.Background(), claimed.ID, "dead-worker", vs.ID); err != nil {
		t.Fatalf("mark live: %v", err)
	}
	store.markStaleHeartbeat(claimed.ID)

	_, err := ctl.Start(context.Background(), tenantID, campaignID)
	if !errors.Is(err, session.ErrIntentPending) {
		t.Fatalf("Start err = %v, want ErrIntentPending (reaped then queued)", err)
	}
	if got := store.get(old.ID).Status; got != storage.VoiceIntentDead {
		t.Fatalf("stale blocker status = %q, want dead (reaped)", got)
	}
}

// TestIntentControl_StopBudgetError covers review item 7: a Stop whose worker
// never confirms within the budget returns ErrStopPending, not a false success.
func TestIntentControl_StopBudgetError(t *testing.T) {
	store := newFakeControlStore()
	ctl := session.NewIntentControl(store, slog.New(slog.DiscardHandler),
		session.IntentControlConfig{Poll: time.Millisecond, StartBudget: time.Second, StopBudget: 20 * time.Millisecond, Expiry: 30 * time.Second})
	tenantID, campaignID := uuid.New(), uuid.New()

	store.add(tenantID, campaignID)
	claimed, _ := store.ClaimVoiceSessionIntent(context.Background(), "worker-1")
	vs := storage.VoiceSession{ID: uuid.New(), CampaignID: campaignID, Status: storage.VoiceSessionRunning}
	store.putSession(vs)
	if _, err := store.MarkVoiceSessionIntentLive(context.Background(), claimed.ID, "worker-1", vs.ID); err != nil {
		t.Fatalf("mark live: %v", err)
	}

	// The worker never winds down (no loop), so Stop only flags and polls to budget.
	_, err := ctl.Stop(context.Background(), tenantID)
	if !errors.Is(err, session.ErrStopPending) {
		t.Fatalf("Stop err = %v, want ErrStopPending", err)
	}
}

// TestIntentControl_MgrOnlyDegrade covers review item 6: the Manager-only live
// controls degrade with ErrSplitMode on the web tier of a split deployment.
func TestIntentControl_MgrOnlyDegrade(t *testing.T) {
	ctl := session.NewIntentControl(newFakeControlStore(), slog.New(slog.DiscardHandler), session.IntentControlConfig{})
	tenantID := uuid.New()

	if _, err := ctl.SetAgentMute(context.Background(), tenantID, uuid.NewString(), true); !errors.Is(err, session.ErrSplitMode) {
		t.Errorf("SetAgentMute err = %v, want ErrSplitMode", err)
	}
	if _, err := ctl.SetAllMute(context.Background(), tenantID, true); !errors.Is(err, session.ErrSplitMode) {
		t.Errorf("SetAllMute err = %v, want ErrSplitMode", err)
	}
	if err := ctl.ReplayHighlight(context.Background(), tenantID, "clip-key"); !errors.Is(err, session.ErrSplitMode) {
		t.Errorf("ReplayHighlight err = %v, want ErrSplitMode", err)
	}
	if ids := ctl.MutedAgentIDs(tenantID); ids != nil {
		t.Errorf("MutedAgentIDs = %v, want nil", ids)
	}
	if sp := ctl.Spend(tenantID); sp != (spend.Status{}) {
		t.Errorf("Spend = %+v, want zero", sp)
	}
}
