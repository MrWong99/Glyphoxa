package session_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeIntentStore is an in-memory session.IntentStore: it models the claim-plane
// semantics (pending → claimed → live → terminal, instance fencing, reap) so the
// ClaimLoop is exercised without Postgres.
type fakeIntentStore struct {
	mu          sync.Mutex
	intents     map[uuid.UUID]*storage.VoiceSessionIntent
	reaped      int
	reapReturns int64 // how many rows ReapDead reports this tick (drives reconcile-after-reap)
	reconciled  int   // ReconcileWorkerOrphanedVoiceSessions call count
}

func newFakeIntentStore() *fakeIntentStore {
	return &fakeIntentStore{intents: map[uuid.UUID]*storage.VoiceSessionIntent{}}
}

func (f *fakeIntentStore) add(tenantID, campaignID uuid.UUID) *storage.VoiceSessionIntent {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := &storage.VoiceSessionIntent{
		ID:         uuid.New(),
		TenantID:   tenantID,
		CampaignID: campaignID,
		Status:     storage.VoiceIntentPending,
		CreatedAt:  time.Now(),
	}
	f.intents[i.ID] = i
	return i
}

func (f *fakeIntentStore) get(id uuid.UUID) storage.VoiceSessionIntent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.intents[id]
}

func (f *fakeIntentStore) requestStop(id uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.intents[id].StopRequested = true
}

func (f *fakeIntentStore) markDead(id uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.intents[id].Status = storage.VoiceIntentDead
}

func (f *fakeIntentStore) ClaimVoiceSessionIntent(_ context.Context, instanceID string) (storage.VoiceSessionIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var oldest *storage.VoiceSessionIntent
	for _, i := range f.intents {
		if i.Status != storage.VoiceIntentPending {
			continue
		}
		if oldest == nil || i.CreatedAt.Before(oldest.CreatedAt) {
			oldest = i
		}
	}
	if oldest == nil {
		return storage.VoiceSessionIntent{}, storage.ErrNotFound
	}
	now := time.Now()
	oldest.Status = storage.VoiceIntentClaimed
	oldest.InstanceID = instanceID
	oldest.ClaimedAt = &now
	oldest.HeartbeatAt = &now
	return *oldest, nil
}

func (f *fakeIntentStore) MarkVoiceSessionIntentLive(_ context.Context, id uuid.UUID, instanceID string, vsID uuid.UUID) (storage.VoiceSessionIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.intents[id]
	if !ok || i.InstanceID != instanceID || i.Status != storage.VoiceIntentClaimed {
		return storage.VoiceSessionIntent{}, storage.ErrNotFound
	}
	i.Status = storage.VoiceIntentLive
	i.VoiceSessionID = uuid.NullUUID{UUID: vsID, Valid: true}
	return *i, nil
}

func (f *fakeIntentStore) HeartbeatVoiceSessionIntent(_ context.Context, id uuid.UUID, instanceID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.intents[id]
	if !ok || i.InstanceID != instanceID ||
		(i.Status != storage.VoiceIntentClaimed && i.Status != storage.VoiceIntentLive) {
		return false, storage.ErrNotFound
	}
	now := time.Now()
	i.HeartbeatAt = &now
	return i.StopRequested, nil
}

func (f *fakeIntentStore) FinishVoiceSessionIntent(_ context.Context, id uuid.UUID, instanceID string, status storage.VoiceSessionIntentStatus, lastError string) (storage.VoiceSessionIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.intents[id]
	if !ok || i.InstanceID != instanceID ||
		(i.Status != storage.VoiceIntentClaimed && i.Status != storage.VoiceIntentLive) {
		return storage.VoiceSessionIntent{}, storage.ErrNotFound
	}
	now := time.Now()
	i.Status = status
	i.LastError = lastError
	i.EndedAt = &now
	return *i, nil
}

func (f *fakeIntentStore) ReapDeadVoiceSessionIntents(_ context.Context, _ time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reaped++
	return f.reapReturns, nil
}

func (f *fakeIntentStore) ReconcileWorkerOrphanedVoiceSessions(_ context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reconciled++
	return 0, nil
}

func (f *fakeIntentStore) reconcileCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reconciled
}

func newClaimLoop(t *testing.T, store session.IntentStore, mgr *session.Manager) *session.ClaimLoop {
	t.Helper()
	return session.NewClaimLoop(store, mgr, "worker-test", slog.New(slog.DiscardHandler),
		session.ClaimLoopConfig{Poll: time.Millisecond, Heartbeat: 2 * time.Millisecond, Expiry: 30 * time.Second})
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within", d)
}

// TestClaimLoop_ClaimStartLiveEndDone covers sequence (6): one tick claims a
// pending intent, starts the Manager session, marks it live, heartbeats; a
// requested stop winds it down and the intent lands 'done'.
func TestClaimLoop_ClaimStartLiveEndDone(t *testing.T) {
	mstore := newFakeStore()
	runner := newBlockingRunner()
	mgr := newManager(t, mstore, runner.run, true)
	istore := newFakeIntentStore()
	loop := newClaimLoop(t, istore, mgr)

	tenantID, campaignID := uuid.New(), uuid.New()
	intent := istore.add(tenantID, campaignID)

	loop.TickForTest(context.Background())

	// The session started (blocking runner is executing) and the intent is live.
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("manager session never started")
	}
	waitFor(t, time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentLive })
	if _, live, _ := mgr.Active(context.Background(), tenantID); !live {
		t.Fatal("manager reports no live session after claim")
	}

	// Sequence (7): a requested stop → the heartbeat goroutine stops the manager
	// session and finishes the intent 'done'.
	istore.requestStop(intent.ID)
	waitFor(t, 2*time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentDone })
	if !runner.wasCancelled() {
		t.Fatal("manager loop was not cancelled on stop_requested")
	}
	if _, live, _ := mgr.Active(context.Background(), tenantID); live {
		t.Fatal("manager still reports a live session after stop")
	}
	loop.DrainForTest()
}

// TestClaimLoop_HeartbeatSupersededStopsSession covers the reaped-claim path
// (sequence 9 worker-death sibling, in-process): when a heartbeat returns
// ErrNotFound (the reaper marked the claim dead), the loop stops its local
// session and does NOT resurrect the terminal row (ADR-0006 no takeover).
func TestClaimLoop_HeartbeatSupersededStopsSession(t *testing.T) {
	mstore := newFakeStore()
	runner := newBlockingRunner()
	mgr := newManager(t, mstore, runner.run, true)
	istore := newFakeIntentStore()
	loop := newClaimLoop(t, istore, mgr)

	tenantID, campaignID := uuid.New(), uuid.New()
	intent := istore.add(tenantID, campaignID)
	loop.TickForTest(context.Background())
	<-runner.started
	waitFor(t, time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentLive })

	// Simulate the reaper marking this claim dead: the next heartbeat is superseded.
	istore.markDead(intent.ID)
	waitFor(t, 2*time.Second, func() bool { return runner.wasCancelled() })
	if _, live, _ := mgr.Active(context.Background(), tenantID); live {
		t.Fatal("session still live after superseded heartbeat")
	}
	// The row stays 'dead' — no finish resurrected it.
	if got := istore.get(intent.ID).Status; got != storage.VoiceIntentDead {
		t.Fatalf("intent status = %q, want dead (no takeover / resurrection)", got)
	}
	loop.DrainForTest()
}

// TestClaimLoop_GracefulDrain covers sequence (5)/AC5: ctx cancellation stops
// claiming and each live session ends cleanly, its intent finished 'done'.
func TestClaimLoop_GracefulDrain(t *testing.T) {
	mstore := newFakeStore()
	runner := newBlockingRunner()
	mgr := newManager(t, mstore, runner.run, true)
	istore := newFakeIntentStore()
	loop := newClaimLoop(t, istore, mgr)

	tenantID, campaignID := uuid.New(), uuid.New()
	intent := istore.add(tenantID, campaignID)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { loop.Run(ctx); close(done) }()

	<-runner.started
	waitFor(t, time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentLive })

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not drain within the window")
	}
	if got := istore.get(intent.ID).Status; got != storage.VoiceIntentDone {
		t.Fatalf("intent status after drain = %q, want done", got)
	}
	if !runner.wasCancelled() {
		t.Fatal("session loop not cancelled on graceful shutdown")
	}
}

// TestClaimLoop_ReconcileAfterReap covers review item 2: a tick that reaps stale
// intents (reap > 0) also runs the worker-orphan reconcile, so a fast restart's
// leftover 'running' rows are closed the moment their intent expires — not only
// at the next boot. A tick with no reap does NOT reconcile (cheap).
func TestClaimLoop_ReconcileAfterReap(t *testing.T) {
	mstore := newFakeStore()
	mgr := newManager(t, mstore, newBlockingRunner().run, true)
	istore := newFakeIntentStore()
	loop := newClaimLoop(t, istore, mgr)

	// No reap this tick → no reconcile.
	istore.reapReturns = 0
	loop.TickForTest(context.Background())
	if got := istore.reconcileCount(); got != 0 {
		t.Fatalf("reconcile ran %d times with no reap, want 0", got)
	}

	// A reap → reconcile runs.
	istore.reapReturns = 2
	loop.TickForTest(context.Background())
	if got := istore.reconcileCount(); got != 1 {
		t.Fatalf("reconcile ran %d times after a reap, want 1", got)
	}
}

// TestClaimLoop_NoCapacityNoClaim asserts the loop does not claim when the
// Manager is at capacity (single-session default): a second pending intent is
// left pending while the first runs.
func TestClaimLoop_NoCapacityNoClaim(t *testing.T) {
	mstore := newFakeStore()
	runner := newBlockingRunner()
	mgr := newManager(t, mstore, runner.run, true) // MaxSessions defaults to 1
	istore := newFakeIntentStore()
	loop := newClaimLoop(t, istore, mgr)

	first := istore.add(uuid.New(), uuid.New())
	second := istore.add(uuid.New(), uuid.New())

	loop.TickForTest(context.Background())
	<-runner.started
	waitFor(t, time.Second, func() bool { return istore.get(first.ID).Status == storage.VoiceIntentLive })

	// Capacity is 1 and taken; the tick above must not have claimed the second.
	if got := istore.get(second.ID).Status; got != storage.VoiceIntentPending {
		t.Fatalf("second intent status = %q, want pending (no capacity)", got)
	}
}
