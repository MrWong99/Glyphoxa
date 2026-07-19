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
	for _, i := range f.fakeIntentStore.intents {
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
	i, ok := f.fakeIntentStore.intents[id]
	if !ok {
		return storage.VoiceSessionIntent{}, storage.ErrNotFound
	}
	return *i, nil
}

func (f *fakeControlStore) RequestVoiceSessionStop(_ context.Context, id uuid.UUID) (storage.VoiceSessionIntent, error) {
	f.fakeIntentStore.mu.Lock()
	defer f.fakeIntentStore.mu.Unlock()
	i, ok := f.fakeIntentStore.intents[id]
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
	for _, i := range f.fakeIntentStore.intents {
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
	for _, i := range f.fakeIntentStore.intents {
		if i.CampaignID == campaignID &&
			(i.Status == storage.VoiceIntentPending || i.Status == storage.VoiceIntentClaimed || i.Status == storage.VoiceIntentLive) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeControlStore) AnyLiveVoiceSessionIntent(_ context.Context) (bool, error) {
	f.fakeIntentStore.mu.Lock()
	defer f.fakeIntentStore.mu.Unlock()
	for _, i := range f.fakeIntentStore.intents {
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
