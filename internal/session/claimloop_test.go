package session_test

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
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
	heartbeats  int   // HeartbeatVoiceSessionIntent call count (#506 finalize-heartbeat test)
	// timeline records the order of claim-plane calls ("hb" / "finish") with
	// timestamps — the #505 drain-heartbeat tests assert beats keep landing
	// through a slow wind-down and that no beat is ordered after the finish.
	timeline []timelineEvent
	// sessionOutcome scripts GetVoiceSession — the self-exit outcome read (#483 L4).
	sessionOutcome func(uuid.UUID) (storage.VoiceSession, error)
	// controls is the requested-control queue (#503), keyed by control id.
	controls map[uuid.UUID]*storage.VoiceSessionControl
	seq      int // creation tiebreak so list order is deterministic
	// onControlCreate scripts the hosting worker's reaction to a fresh control row
	// (#503 requester-side tests): run under the lock right after the insert.
	onControlCreate func(*storage.VoiceSessionControl)
	// finishControlErrs is a queue of errors FinishVoiceSessionControl returns on
	// successive calls (a nil entry, or an exhausted queue, is a normal finish) —
	// models a transient terminal-write blip (#503 FIX1).
	finishControlErrs []error
}

// timelineEvent is one recorded claim-plane call: kind "hb" (heartbeat) or
// "finish", stamped when the call landed.
type timelineEvent struct {
	kind string
	at   time.Time
}

func newFakeIntentStore() *fakeIntentStore {
	return &fakeIntentStore{
		intents:  map[uuid.UUID]*storage.VoiceSessionIntent{},
		controls: map[uuid.UUID]*storage.VoiceSessionControl{},
	}
}

// addControl enqueues a pending control row (#503), stamping CreatedAt with a
// strictly-increasing sequence so (created_at, id) list order is deterministic.
func (f *fakeIntentStore) addControl(c storage.VoiceSessionControl) *storage.VoiceSessionControl {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	row := c
	row.ID = uuid.New()
	row.Status = storage.VoiceControlPending
	row.CreatedAt = time.Now().Add(time.Duration(f.seq) * time.Microsecond)
	f.controls[row.ID] = &row
	return &row
}

// CreateVoiceSessionControl is the requester-side write (#503, IntentControlStore).
func (f *fakeIntentStore) CreateVoiceSessionControl(_ context.Context, c storage.VoiceSessionControl) (storage.VoiceSessionControl, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	row := c
	row.ID = uuid.New()
	row.Status = storage.VoiceControlPending
	row.CreatedAt = time.Now().Add(time.Duration(f.seq) * time.Microsecond)
	f.controls[row.ID] = &row
	if f.onControlCreate != nil {
		f.onControlCreate(&row)
	}
	return row, nil
}

func (f *fakeIntentStore) GetVoiceSessionControl(_ context.Context, id uuid.UUID) (storage.VoiceSessionControl, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.controls[id]
	if !ok {
		return storage.VoiceSessionControl{}, storage.ErrNotFound
	}
	return *c, nil
}

func (f *fakeIntentStore) CancelPendingVoiceSessionControl(_ context.Context, id uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.controls[id]
	if !ok || c.Status != storage.VoiceControlPending {
		return false, nil
	}
	now := time.Now()
	c.Status = storage.VoiceControlFailed
	c.LastError = "requester timed out"
	c.EndedAt = &now
	return true, nil
}

func (f *fakeIntentStore) getControl(id uuid.UUID) storage.VoiceSessionControl {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.controls[id]
}

func (f *fakeIntentStore) ListPendingVoiceSessionControls(_ context.Context, intentID uuid.UUID) ([]storage.VoiceSessionControl, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []storage.VoiceSessionControl
	for _, c := range f.controls {
		if c.IntentID == intentID && c.Status == storage.VoiceControlPending {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out, nil
}

func (f *fakeIntentStore) StartVoiceSessionControl(_ context.Context, id uuid.UUID) (storage.VoiceSessionControl, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.controls[id]
	if !ok || c.Status != storage.VoiceControlPending {
		return storage.VoiceSessionControl{}, storage.ErrNotFound
	}
	now := time.Now()
	c.Status = storage.VoiceControlExecuting
	c.StartedAt = &now
	return *c, nil
}

func (f *fakeIntentStore) FinishVoiceSessionControl(_ context.Context, id uuid.UUID, status storage.VoiceSessionControlStatus, resultIDs []string, lastError string) (storage.VoiceSessionControl, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.finishControlErrs) > 0 {
		err := f.finishControlErrs[0]
		f.finishControlErrs = f.finishControlErrs[1:]
		if err != nil {
			return storage.VoiceSessionControl{}, err // transient blip: row stays executing
		}
	}
	c, ok := f.controls[id]
	if !ok || c.Status != storage.VoiceControlExecuting {
		return storage.VoiceSessionControl{}, storage.ErrNotFound
	}
	now := time.Now()
	c.Status = status
	c.ResultIDs = resultIDs
	c.LastError = lastError
	c.EndedAt = &now
	return *c, nil
}

func (f *fakeIntentStore) SweepOrphanedVoiceSessionControls(_ context.Context, executingStale time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, c := range f.controls {
		if c.Status != storage.VoiceControlPending && c.Status != storage.VoiceControlExecuting {
			continue
		}
		terminal := false
		if i, ok := f.intents[c.IntentID]; ok {
			switch i.Status {
			case storage.VoiceIntentDone, storage.VoiceIntentDead, storage.VoiceIntentFailed:
				terminal = true
			}
		}
		staleExec := c.Status == storage.VoiceControlExecuting && c.StartedAt != nil &&
			time.Since(*c.StartedAt) >= executingStale
		if !terminal && !staleExec {
			continue
		}
		now := time.Now()
		c.Status = storage.VoiceControlFailed
		if terminal {
			c.LastError = session.EncodeControlFailure(session.ErrNoActiveSession)
		} else {
			c.LastError = "control execution stalled on the hosting worker"
		}
		c.EndedAt = &now
		n++
	}
	return n, nil
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
	f.heartbeats++
	f.timeline = append(f.timeline, timelineEvent{kind: "hb", at: time.Now()})
	i, ok := f.intents[id]
	if !ok || i.InstanceID != instanceID ||
		(i.Status != storage.VoiceIntentClaimed && i.Status != storage.VoiceIntentLive) {
		return false, storage.ErrNotFound
	}
	now := time.Now()
	i.HeartbeatAt = &now
	return i.StopRequested, nil
}

func (f *fakeIntentStore) heartbeatCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.heartbeats
}

func (f *fakeIntentStore) FinishVoiceSessionIntent(_ context.Context, id uuid.UUID, instanceID string, status storage.VoiceSessionIntentStatus, lastError string) (storage.VoiceSessionIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.timeline = append(f.timeline, timelineEvent{kind: "finish", at: time.Now()})
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

// ReapDeadVoiceSessionIntents models the real reaper (#505): a claimed/live row
// whose heartbeat is staler than expiry flips 'dead'. reapReturns is added to
// the reported count (drives the reconcile-after-reap logging path).
func (f *fakeIntentStore) ReapDeadVoiceSessionIntents(_ context.Context, expiry time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reaped++
	var n int64
	now := time.Now()
	for _, i := range f.intents {
		if (i.Status == storage.VoiceIntentClaimed || i.Status == storage.VoiceIntentLive) &&
			i.HeartbeatAt != nil && now.Sub(*i.HeartbeatAt) > expiry {
			i.Status = storage.VoiceIntentDead
			n++
		}
	}
	return n + f.reapReturns, nil
}

func (f *fakeIntentStore) ReconcileWorkerOrphanedVoiceSessions(_ context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reconciled++
	return 0, nil
}

// GetVoiceSession answers the self-exit outcome read (#483 L4) via the scripted
// sessionOutcome hook; unset → ErrNotFound (the loop then finishes 'done').
func (f *fakeIntentStore) GetVoiceSession(_ context.Context, id uuid.UUID) (storage.VoiceSession, error) {
	f.mu.Lock()
	hook := f.sessionOutcome
	f.mu.Unlock()
	if hook == nil {
		return storage.VoiceSession{}, storage.ErrNotFound
	}
	return hook(id)
}

func (f *fakeIntentStore) timelineCopy() []timelineEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]timelineEvent(nil), f.timeline...)
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

// TestClaimLoop_ReconcileEveryTick covers review item 2 as hardened by #483 L2:
// the worker-orphan reconcile runs on EVERY tick (it is idempotent and cheap),
// not only after a reap — gating it on reap > 0 stranded a 'running' row whose
// intent finished 'done'/'failed' normally but whose CloseVoiceSession write
// failed: no reap would ever fire for that Tenant again.
func TestClaimLoop_ReconcileEveryTick(t *testing.T) {
	mstore := newFakeStore()
	mgr := newManager(t, mstore, newBlockingRunner().run, true)
	istore := newFakeIntentStore()
	loop := newClaimLoop(t, istore, mgr)

	// Even a tick with no reap reconciles.
	istore.reapReturns = 0
	loop.TickForTest(context.Background())
	if got := istore.reconcileCount(); got != 1 {
		t.Fatalf("reconcile ran %d times on a no-reap tick, want 1", got)
	}

	// A reaping tick reconciles too (once).
	istore.reapReturns = 2
	loop.TickForTest(context.Background())
	if got := istore.reconcileCount(); got != 2 {
		t.Fatalf("reconcile count after a reaping tick = %d, want 2", got)
	}
}

// blackholeIntentStore models a black-holed DB connection (#483 M1): every
// claim-plane call parks until its ctx is cancelled. Without a per-op timeout a
// tick would block for the kernel TCP timeout (minutes) — the zombie window where
// a live session outlives its reaped intent.
type blackholeIntentStore struct{ *fakeIntentStore }

func (b blackholeIntentStore) ReapDeadVoiceSessionIntents(ctx context.Context, _ time.Duration) (int64, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

func (b blackholeIntentStore) ReconcileWorkerOrphanedVoiceSessions(ctx context.Context) (int64, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

func (b blackholeIntentStore) ClaimVoiceSessionIntent(ctx context.Context, _ string) (storage.VoiceSessionIntent, error) {
	<-ctx.Done()
	return storage.VoiceSessionIntent{}, ctx.Err()
}

// TestClaimLoop_BlackholedStoreDoesNotPinTick covers #483 M1: with every DB call
// parked on its ctx, a tick must still return within the per-op timeouts
// (min(poll, 3s) each) instead of hanging until the caller's ctx dies.
func TestClaimLoop_BlackholedStoreDoesNotPinTick(t *testing.T) {
	mstore := newFakeStore()
	mgr := newManager(t, mstore, newBlockingRunner().run, true)
	istore := blackholeIntentStore{newFakeIntentStore()}
	loop := newClaimLoop(t, istore, mgr) // Poll 1ms → per-op timeout 1ms

	done := make(chan struct{})
	go func() {
		loop.TickForTest(context.Background()) // NO deadline on the outer ctx
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a black-holed store pinned the claim tick past its per-op timeouts")
	}
}

// TestClaimLoop_StartRefusalCarriesFailCode covers #483 M4: a typed Manager Start
// refusal (here ErrDiscordNotConfigured — the deployment config has no
// guild/channel) must land in the intent's last_error as a machine-parseable fail
// code the web tier's IntentControl can re-map to the SAME sentinel, so the RPC
// answers CodeFailedPrecondition with actionable text instead of a flattened
// CodeInternal "internal error".
func TestClaimLoop_StartRefusalCarriesFailCode(t *testing.T) {
	mstore := newFakeStore()
	mstore.dep = storage.DeploymentConfig{} // no guild/channel → ErrDiscordNotConfigured
	mgr := newManager(t, mstore, newBlockingRunner().run, true)
	istore := newFakeIntentStore()
	loop := newClaimLoop(t, istore, mgr)

	intent := istore.add(uuid.New(), uuid.New())
	loop.TickForTest(context.Background())

	got := istore.get(intent.ID)
	if got.Status != storage.VoiceIntentFailed {
		t.Fatalf("intent status = %q, want failed", got.Status)
	}
	sentinel, ok := session.DecodeStartFailure(got.LastError)
	if !ok {
		t.Fatalf("last_error = %q carries no decodable fail code", got.LastError)
	}
	if !errors.Is(sentinel, session.ErrDiscordNotConfigured) {
		t.Fatalf("decoded sentinel = %v, want ErrDiscordNotConfigured", sentinel)
	}
}

// TestClaimLoop_SelfExitWaitsOutEndWindowAndCarriesFailure covers #483 L3+L4: a
// session that ends on its own has a window where Manager.Active is already false
// (as.ended) but the active entry has not cleared (finalizers + CloseVoiceSession
// still running) — finishing the intent there lets an instant restart collide
// ErrSessionActive and misreport 'failed'. The loop must wait out that window
// (Finalizing) before finishing. And a session whose row closed 'failed' must
// finish its intent 'failed' with the row's end_reason (L4), not a clean-looking
// 'done' with an empty last_error.
func TestClaimLoop_SelfExitWaitsOutEndWindowAndCarriesFailure(t *testing.T) {
	mstore := newFakeStore()
	closeGate := make(chan struct{})
	mstore.closeGate = closeGate // parks CloseVoiceSession → holds the end window open
	runner := newFailingRunner(errors.New("gateway exploded"))
	mgr := newManager(t, mstore, runner.run, true)
	istore := newFakeIntentStore()
	istore.sessionOutcome = func(id uuid.UUID) (storage.VoiceSession, error) {
		return mstore.session(id), nil
	}
	loop := newClaimLoop(t, istore, mgr)

	tenantID := uuid.New()
	intent := istore.add(tenantID, uuid.New())
	loop.TickForTest(context.Background())
	waitFor(t, time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentLive })

	// Let the loop fail NOW; CloseVoiceSession parks on the gate, so the Manager
	// sits in its end window: Active false, entry not yet cleared.
	runner.fail()
	waitFor(t, time.Second, func() bool {
		_, live, _ := mgr.Active(context.Background(), tenantID)
		return !live
	})
	// Give the heartbeat goroutine a few ticks INSIDE the window: it must NOT
	// finish the intent while the Manager is still finalizing (L3).
	time.Sleep(20 * time.Millisecond)
	if got := istore.get(intent.ID).Status; got != storage.VoiceIntentLive {
		t.Fatalf("intent finished %q inside the Manager end window, want still live (L3)", got)
	}

	// Release the end write: the entry clears, and the intent must finish 'failed'
	// carrying the session row's end_reason (L4).
	close(closeGate)
	waitFor(t, 2*time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentFailed })
	if got := istore.get(intent.ID).LastError; !strings.Contains(got, "gateway exploded") {
		t.Fatalf("intent last_error = %q, want the session's failure reason carried over (L4)", got)
	}
	loop.DrainForTest()
}

// TestClaimLoop_HeartbeatsThroughSlowFinalize covers the #506 re-review of the
// #483 L3 fix: a self-exited session whose finalizers run slowly must keep
// heartbeating through the Manager end window — the first cut `continue`d past
// the heartbeat, so a slow finalize crossed Expiry and another worker's reaper
// mislabeled a clean self-exit 'dead'. Assert the heartbeat keeps landing while
// the session finalizes (its claim stays fresh) and the intent stays live until
// the window clears, only THEN finishing 'done'.
func TestClaimLoop_HeartbeatsThroughSlowFinalize(t *testing.T) {
	mstore := newFakeStore()
	closeGate := make(chan struct{})
	mstore.closeGate = closeGate // hold the end window open
	runner := newBlockingRunner()
	mgr := newManager(t, mstore, runner.run, true)
	istore := newFakeIntentStore()
	istore.sessionOutcome = func(id uuid.UUID) (storage.VoiceSession, error) {
		return mstore.session(id), nil
	}
	// Fast heartbeat so several beats fall inside the finalize window.
	loop := session.NewClaimLoop(istore, mgr, "worker-test", slog.New(slog.DiscardHandler),
		session.ClaimLoopConfig{Poll: time.Millisecond, Heartbeat: time.Millisecond, Expiry: 30 * time.Second})

	tenantID := uuid.New()
	intent := istore.add(tenantID, uuid.New())
	loop.TickForTest(context.Background())
	<-runner.started
	waitFor(t, time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentLive })

	// The session self-exits (Stop cancels the runner) but CloseVoiceSession parks
	// on the gate, so the Manager sits in its end window: Active false, Finalizing
	// true, active entry not yet cleared.
	go func() { _, _ = mgr.Stop(context.Background(), tenantID) }()
	waitFor(t, time.Second, func() bool { return mgr.Finalizing(tenantID) })

	// While finalizing, the heartbeat must keep landing (the claim stays fresh so
	// no reaper mislabels it) and the intent must stay live (not finished yet).
	before := istore.heartbeatCount()
	waitFor(t, time.Second, func() bool { return istore.heartbeatCount() > before+2 })
	if got := istore.get(intent.ID).Status; got != storage.VoiceIntentLive {
		t.Fatalf("intent finished %q during the finalize window, want still live (heartbeating)", got)
	}

	// Release the end write: the window clears and the intent finishes 'done'.
	close(closeGate)
	waitFor(t, 2*time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentDone })
	loop.DrainForTest()
}

// TestClaimLoop_DrainHeartbeatDuringSigterm covers #505 AC1/AC3: a SIGTERM drain
// whose CloseVoiceSession runs slowly (the slow-finalizer window, parked on the
// gate) must KEEP heartbeating while endSession blocks in Manager.Stop — before
// #505 the drain window (up to the Manager's end budgets) went unheartbeated, so
// a clean wind-down could cross Expiry and be reaped 'dead' mid-drain. Assert
// beats keep landing during the Stop window, the intent stays live (not
// terminal) until the finalizer releases, and only THEN finishes 'done'.
func TestClaimLoop_DrainHeartbeatDuringSigterm(t *testing.T) {
	mstore := newFakeStore()
	closeGate := make(chan struct{})
	mstore.closeGate = closeGate // parks CloseVoiceSession → the slow wind-down
	runner := newBlockingRunner()
	mgr := newManager(t, mstore, runner.run, true)
	istore := newFakeIntentStore()
	loop := session.NewClaimLoop(istore, mgr, "worker-test", slog.New(slog.DiscardHandler),
		session.ClaimLoopConfig{Poll: time.Millisecond, Heartbeat: time.Millisecond, Expiry: 30 * time.Second})

	tenantID := uuid.New()
	intent := istore.add(tenantID, uuid.New())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { loop.Run(ctx); close(done) }()
	<-runner.started
	waitFor(t, time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentLive })

	// SIGTERM: runSession's ctx.Done branch enters endSession → Manager.Stop
	// parks on the close gate. Beats must continue through that window.
	cancel()
	waitFor(t, time.Second, func() bool { return mgr.Finalizing(tenantID) })
	before := istore.heartbeatCount()
	waitFor(t, time.Second, func() bool { return istore.heartbeatCount() > before+2 })
	if got := istore.get(intent.ID).Status; got != storage.VoiceIntentLive {
		t.Fatalf("intent status = %q during the drain window, want still live (heartbeating)", got)
	}

	// Release the slow finalizer: the wind-down completes and the intent
	// finishes 'done' — a clean drain within budget is never 'dead'.
	close(closeGate)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not drain after the finalizer released")
	}
	if got := istore.get(intent.ID).Status; got != storage.VoiceIntentDone {
		t.Fatalf("intent status after drain = %q, want done", got)
	}
}

// TestClaimLoop_DrainHeartbeatDuringStopRequested covers #505's second call
// site: a stop_requested wind-down has the SAME unheartbeated window as the
// SIGTERM drain (endSession blocks in Manager.Stop through the finalizers), so
// beats must keep landing there too, and the intent finishes 'done' only after
// the slow finalizer releases.
func TestClaimLoop_DrainHeartbeatDuringStopRequested(t *testing.T) {
	mstore := newFakeStore()
	closeGate := make(chan struct{})
	mstore.closeGate = closeGate
	runner := newBlockingRunner()
	mgr := newManager(t, mstore, runner.run, true)
	istore := newFakeIntentStore()
	loop := session.NewClaimLoop(istore, mgr, "worker-test", slog.New(slog.DiscardHandler),
		session.ClaimLoopConfig{Poll: time.Millisecond, Heartbeat: time.Millisecond, Expiry: 30 * time.Second})

	tenantID := uuid.New()
	intent := istore.add(tenantID, uuid.New())
	loop.TickForTest(context.Background())
	<-runner.started
	waitFor(t, time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentLive })

	// stop_requested → runSession's ticker branch enters endSession →
	// Manager.Stop parks on the close gate. Beats must continue.
	istore.requestStop(intent.ID)
	waitFor(t, time.Second, func() bool { return mgr.Finalizing(tenantID) })
	before := istore.heartbeatCount()
	waitFor(t, time.Second, func() bool { return istore.heartbeatCount() > before+2 })
	if got := istore.get(intent.ID).Status; got != storage.VoiceIntentLive {
		t.Fatalf("intent status = %q during the stop wind-down, want still live (heartbeating)", got)
	}

	close(closeGate)
	waitFor(t, 2*time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentDone })
	loop.DrainForTest()
}

// TestClaimLoop_DrainHeartbeatSupersededStopsBeating covers #505's reaped-anyway
// arm (ADR-0006 superseded-mid-drain): if the claim is reaped DURING the drain
// (a multi-beat DB outage crossed Expiry), the drain-beat goroutine stops
// stamping — it never re-claims and never calls mgr.Stop again — while the
// wind-down itself completes; the finish is fenced NotFound and swallowed, the
// row stays 'dead', and the goroutine exits (DrainForTest returns — no leak).
func TestClaimLoop_DrainHeartbeatSupersededStopsBeating(t *testing.T) {
	mstore := newFakeStore()
	closeGate := make(chan struct{})
	mstore.closeGate = closeGate
	runner := newBlockingRunner()
	mgr := newManager(t, mstore, runner.run, true)
	istore := newFakeIntentStore()
	loop := session.NewClaimLoop(istore, mgr, "worker-test", slog.New(slog.DiscardHandler),
		session.ClaimLoopConfig{Poll: time.Millisecond, Heartbeat: time.Millisecond, Expiry: 30 * time.Second})

	tenantID := uuid.New()
	intent := istore.add(tenantID, uuid.New())
	loop.TickForTest(context.Background())
	<-runner.started
	waitFor(t, time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentLive })

	// Enter the drain (stop_requested) and let it park on the close gate.
	istore.requestStop(intent.ID)
	waitFor(t, time.Second, func() bool { return mgr.Finalizing(tenantID) })

	// The reaper wins mid-drain: the next drain beat NotFounds and stops beating.
	istore.markDead(intent.ID)
	waitFor(t, time.Second, func() bool {
		before := istore.heartbeatCount()
		time.Sleep(10 * time.Millisecond)
		return istore.heartbeatCount() == before
	})

	// The wind-down still completes; the finish is swallowed (row stays 'dead')
	// and every goroutine exits.
	close(closeGate)
	loop.DrainForTest()
	if got := istore.get(intent.ID).Status; got != storage.VoiceIntentDead {
		t.Fatalf("intent status = %q, want dead (superseded-mid-drain never resurrected)", got)
	}
}

// TestClaimLoop_ReaperNeverKillsCleanDrain covers #505 AC1+AC2 against the REAL
// reaper semantics (the fake reaps rows with heartbeats staler than Expiry): a
// clean wind-down that is SLOWER than Expiry (gate held >> Expiry) but keeps
// drain-beating is never marked 'dead' by concurrent reap/reconcile ticks — the
// intent stays live (non-terminal) the whole time CloseVoiceSession is
// in-flight, so neither ReconcileWorkerOrphanedVoiceSessions arm (both require
// a terminal/absent intent) can race the Close; then it finishes 'done'.
func TestClaimLoop_ReaperNeverKillsCleanDrain(t *testing.T) {
	mstore := newFakeStore()
	closeGate := make(chan struct{})
	mstore.closeGate = closeGate
	runner := newBlockingRunner()
	mgr := newManager(t, mstore, runner.run, true)
	istore := newFakeIntentStore()
	// Scaled intervals: drain beats every 2ms, Expiry 20ms, gate held ~100ms —
	// several Expiry windows pass while the wind-down is in-flight.
	loop := session.NewClaimLoop(istore, mgr, "worker-test", slog.New(slog.DiscardHandler),
		session.ClaimLoopConfig{Poll: time.Millisecond, Heartbeat: 2 * time.Millisecond, Expiry: 20 * time.Millisecond})

	tenantID := uuid.New()
	intent := istore.add(tenantID, uuid.New())
	loop.TickForTest(context.Background())
	<-runner.started
	waitFor(t, time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentLive })

	// Enter the drain and park on the gate; meanwhile keep reap+reconcile ticks
	// running concurrently (another worker's reaper never sleeps).
	istore.requestStop(intent.ID)
	waitFor(t, time.Second, func() bool { return mgr.Finalizing(tenantID) })
	reapStop := make(chan struct{})
	reapDone := make(chan struct{})
	go func() {
		defer close(reapDone)
		for {
			select {
			case <-reapStop:
				return
			default:
				loop.TickForTest(context.Background())
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()

	// Hold the drain open for ~5x Expiry: the intent must stay live throughout —
	// never 'dead' (AC1), never terminal while Close is in-flight (AC2).
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := istore.get(intent.ID).Status; got != storage.VoiceIntentLive {
			t.Fatalf("intent status = %q mid-drain under a live reaper, want live", got)
		}
		time.Sleep(5 * time.Millisecond)
	}

	close(closeGate)
	waitFor(t, 2*time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentDone })
	close(reapStop)
	<-reapDone
	loop.DrainForTest()
}

// TestClaimLoop_DrainBeatsEndBeforeFinish covers #505's ordering contract:
// endSession waits the drain-beat goroutine out (stopBeat) BEFORE writing the
// terminal state, so the recorded call timeline never shows a heartbeat ordered
// after the finish — a late stray beat would be fenced NotFound on a real
// store, but the loop must not rely on that fence.
func TestClaimLoop_DrainBeatsEndBeforeFinish(t *testing.T) {
	mstore := newFakeStore()
	closeGate := make(chan struct{})
	mstore.closeGate = closeGate
	runner := newBlockingRunner()
	mgr := newManager(t, mstore, runner.run, true)
	istore := newFakeIntentStore()
	loop := session.NewClaimLoop(istore, mgr, "worker-test", slog.New(slog.DiscardHandler),
		session.ClaimLoopConfig{Poll: time.Millisecond, Heartbeat: time.Millisecond, Expiry: 30 * time.Second})

	tenantID := uuid.New()
	intent := istore.add(tenantID, uuid.New())
	loop.TickForTest(context.Background())
	<-runner.started
	waitFor(t, time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentLive })

	istore.requestStop(intent.ID)
	waitFor(t, time.Second, func() bool { return mgr.Finalizing(tenantID) })
	before := istore.heartbeatCount()
	waitFor(t, time.Second, func() bool { return istore.heartbeatCount() > before+2 })
	close(closeGate)
	waitFor(t, 2*time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentDone })
	loop.DrainForTest()

	events := istore.timelineCopy()
	finishAt := -1
	for i, e := range events {
		if e.kind == "finish" {
			finishAt = i
			break
		}
	}
	if finishAt < 0 {
		t.Fatal("no finish recorded in the call timeline")
	}
	for _, e := range events[finishAt+1:] {
		if e.kind == "hb" {
			t.Fatal("a heartbeat was ordered AFTER the finish — drain beats must end before the terminal write")
		}
	}
}

// markLiveFailStore scripts MarkVoiceSessionIntentLive to fail with a
// non-NotFound error (a DB blip), leaving the row 'claimed' with its
// heartbeat_at from claim time — the #509-review residual 1 setup.
type markLiveFailStore struct {
	*fakeIntentStore
	err error
}

func (s *markLiveFailStore) MarkVoiceSessionIntentLive(context.Context, uuid.UUID, string, uuid.UUID) (storage.VoiceSessionIntent, error) {
	return storage.VoiceSessionIntent{}, s.err
}

// TestClaimLoop_DrainHeartbeatDuringMarkLiveFailure covers #509-review residual
// 1: a mark-live failure stops the just-started session, and that Stop blocks
// through the finalizers with the row still 'claimed' and heartbeat_at from
// claim time. Un-heartbeated, a slow wind-down crosses Expiry and the reaper
// records the TRUE mark-live failure as "worker heartbeat expired" ('dead').
// The path must drain-beat like endSession does: under a live reaper and a
// wind-down held open for ~5x Expiry, the row stays 'claimed' the whole time
// and then finishes 'failed' carrying the mark-live error — never 'dead'.
func TestClaimLoop_DrainHeartbeatDuringMarkLiveFailure(t *testing.T) {
	mstore := newFakeStore()
	closeGate := make(chan struct{})
	mstore.closeGate = closeGate // parks CloseVoiceSession → the slow wind-down
	runner := newBlockingRunner()
	mgr := newManager(t, mstore, runner.run, true)
	istore := &markLiveFailStore{fakeIntentStore: newFakeIntentStore(), err: errors.New("db blip")}
	loop := session.NewClaimLoop(istore, mgr, "worker-test", slog.New(slog.DiscardHandler),
		session.ClaimLoopConfig{Poll: time.Millisecond, Heartbeat: 2 * time.Millisecond, Expiry: 20 * time.Millisecond})

	tenantID := uuid.New()
	intent := istore.add(tenantID, uuid.New())

	// The tick blocks inside startClaimed (Stop parked on the gate) — run it in a
	// goroutine, like the reaper ticks below run concurrently against it.
	tickDone := make(chan struct{})
	go func() { loop.TickForTest(context.Background()); close(tickDone) }()
	<-runner.started
	waitFor(t, time.Second, func() bool { return mgr.Finalizing(tenantID) })

	// Beats must land while Stop is parked (the row is 'claimed', which the store
	// heartbeat fence accepts).
	before := istore.heartbeatCount()
	waitFor(t, time.Second, func() bool { return istore.heartbeatCount() > before+2 })

	// Keep reap ticks running while the wind-down is held open for ~5x Expiry:
	// the row must stay 'claimed' throughout — never reaped 'dead'.
	reapStop := make(chan struct{})
	reapDone := make(chan struct{})
	go func() {
		defer close(reapDone)
		for {
			select {
			case <-reapStop:
				return
			default:
				loop.TickForTest(context.Background())
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := istore.get(intent.ID).Status; got != storage.VoiceIntentClaimed {
			t.Fatalf("intent status = %q mid-stop under a live reaper, want claimed", got)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Release the finalizer: the true failure lands on the row.
	close(closeGate)
	select {
	case <-tickDone:
	case <-time.After(2 * time.Second):
		t.Fatal("tick did not return after the finalizer released")
	}
	close(reapStop)
	<-reapDone
	got := istore.get(intent.ID)
	if got.Status != storage.VoiceIntentFailed {
		t.Fatalf("intent status = %q, want failed carrying the mark-live error", got.Status)
	}
	if !strings.Contains(got.LastError, "mark-live failed") || !strings.Contains(got.LastError, "db blip") {
		t.Fatalf("intent last_error = %q, want the true mark-live failure", got.LastError)
	}

	// Same ordering contract as endSession: no beat after the terminal write.
	events := istore.timelineCopy()
	finishAt := -1
	for i, e := range events {
		if e.kind == "finish" {
			finishAt = i
			break
		}
	}
	if finishAt < 0 {
		t.Fatal("no finish recorded in the call timeline")
	}
	for _, e := range events[finishAt+1:] {
		if e.kind == "hb" {
			t.Fatal("a heartbeat was ordered AFTER the finish — drain beats must end before the terminal write")
		}
	}
}

// stuckRunner models a session run loop wedged past ctx cancel — the ONLY
// unbounded step in Manager.Stop (the finalizers and end-write are all
// endTimeout-bounded). It ignores cancellation and unblocks only on release.
type stuckRunner struct {
	started chan struct{}
	release chan struct{}
}

func newStuckRunner() *stuckRunner {
	return &stuckRunner{started: make(chan struct{}), release: make(chan struct{})}
}

func (r *stuckRunner) run(context.Context, wirenpc.Config) error {
	close(r.started)
	<-r.release // NOT ctx.Done() — a wedged loop never observes the cancel
	return nil
}

// TestClaimLoop_DrainBeatCapLetsReaperReclaim covers #509-review residual 2
// (the risk #505 introduced): on a stop_requested wind-down whose run loop is
// wedged past ctx cancel, uncapped drain beats keep the intent 'live' FOREVER —
// the Tenant's voice plane is pinned permanently, where pre-#505 the reaper
// freed it within Expiry. The drain beats must stop after DrainBeatCap, letting
// the heartbeat go stale so the reaper reclaims the row ('dead'), and the
// eventually-unwedged finish is fenced NotFound — never a resurrection.
func TestClaimLoop_DrainBeatCapLetsReaperReclaim(t *testing.T) {
	mstore := newFakeStore()
	runner := newStuckRunner()
	mgr := newManager(t, mstore, runner.run, true)
	istore := newFakeIntentStore()
	loop := session.NewClaimLoop(istore, mgr, "worker-test", slog.New(slog.DiscardHandler),
		session.ClaimLoopConfig{Poll: time.Millisecond, Heartbeat: time.Millisecond,
			Expiry: 20 * time.Millisecond, DrainBeatCap: 40 * time.Millisecond})

	tenantID := uuid.New()
	intent := istore.add(tenantID, uuid.New())
	loop.TickForTest(context.Background())
	<-runner.started
	waitFor(t, time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentLive })

	// stop_requested → endSession → Manager.Stop blocks forever on the wedged
	// loop. Drain beats run, then MUST cease once the cap elapses.
	istore.requestStop(intent.ID)
	waitFor(t, 2*time.Second, func() bool {
		before := istore.heartbeatCount()
		time.Sleep(10 * time.Millisecond)
		return istore.heartbeatCount() == before
	})

	// With beats ceased the heartbeat goes stale and the reaper reclaims: the
	// Tenant's voice plane is freed even though this worker is still wedged.
	waitFor(t, time.Second, func() bool {
		loop.TickForTest(context.Background())
		return istore.get(intent.ID).Status == storage.VoiceIntentDead
	})

	// Unwedge: the wind-down completes, the finish NotFounds on the dead row
	// (swallowed), every goroutine exits, and the row is never resurrected.
	close(runner.release)
	loop.DrainForTest()
	if got := istore.get(intent.ID).Status; got != storage.VoiceIntentDead {
		t.Fatalf("intent status = %q after the wedged drain unblocked, want dead (no resurrection)", got)
	}
}

// TestClaimLoop_DrainBeatCapDefaults pins the cap's defaulting: unset derives
// 10x Expiry (300s at the 30s default — matching the deploy's
// terminationGracePeriodSeconds and far above every finalizer budget, so no
// legitimate slow wind-down is ever cut off); an explicit value is respected.
func TestClaimLoop_DrainBeatCapDefaults(t *testing.T) {
	mstore := newFakeStore()
	mgr := newManager(t, mstore, newBlockingRunner().run, true)
	derived := session.NewClaimLoop(newFakeIntentStore(), mgr, "worker-test", slog.New(slog.DiscardHandler),
		session.ClaimLoopConfig{Expiry: 7 * time.Second})
	if got := derived.DrainBeatCapForTest(); got != 70*time.Second {
		t.Fatalf("derived DrainBeatCap = %v, want 10x Expiry (70s)", got)
	}
	explicit := session.NewClaimLoop(newFakeIntentStore(), mgr, "worker-test", slog.New(slog.DiscardHandler),
		session.ClaimLoopConfig{Expiry: 7 * time.Second, DrainBeatCap: time.Minute})
	if got := explicit.DrainBeatCapForTest(); got != time.Minute {
		t.Fatalf("explicit DrainBeatCap = %v, want 1m", got)
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
