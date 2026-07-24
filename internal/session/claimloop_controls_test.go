package session_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// controlLoopHarness wires the cross-pod control-dispatch tests (#503): a
// Manager over a real bus (SayAs publishes are observable) driven by a claim
// loop with millisecond cadences, one live intent claimed.
type controlLoopHarness struct {
	mstore *fakeStore
	istore *fakeIntentStore
	mgr    *session.Manager
	loop   *session.ClaimLoop
	bus    *voiceevent.Bus
	intent *storage.VoiceSessionIntent
	tenant uuid.UUID
}

func newControlLoopHarness(t *testing.T) *controlLoopHarness {
	t.Helper()
	mstore := newFakeStore()
	mgr, bus := muteManager(t, mstore)
	t.Cleanup(mgr.Shutdown)
	istore := newFakeIntentStore()
	loop := session.NewClaimLoop(istore, mgr, "worker-test", slog.New(slog.DiscardHandler),
		session.ClaimLoopConfig{Poll: time.Millisecond, Heartbeat: time.Millisecond, Expiry: 30 * time.Second})

	tenantID := uuid.New()
	intent := istore.add(tenantID, uuid.New())
	loop.TickForTest(context.Background())
	waitFor(t, time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentLive })
	return &controlLoopHarness{mstore: mstore, istore: istore, mgr: mgr, loop: loop, bus: bus, intent: intent, tenant: tenantID}
}

// TestClaimLoop_DispatchesMuteControl covers sequence (5): a pending mute_agent
// control is executed against the hosting worker's Manager on a heartbeat tick
// and its row finishes 'done' carrying the resulting muted-id set.
func TestClaimLoop_DispatchesMuteControl(t *testing.T) {
	h := newControlLoopHarness(t)
	agents := seedAgents(h.mstore, 2)

	row := h.istore.addControl(storage.VoiceSessionControl{
		IntentID: h.intent.ID,
		TenantID: h.tenant,
		Kind:     storage.VoiceControlMuteAgent,
		AgentID:  agents[0].ID.String(),
		Muted:    true,
	})
	waitFor(t, 2*time.Second, func() bool { return h.istore.getControl(row.ID).Status == storage.VoiceControlDone })
	got := h.istore.getControl(row.ID)
	if len(got.ResultIDs) != 1 || got.ResultIDs[0] != agents[0].ID.String() {
		t.Fatalf("done control result_ids = %v, want the muted agent id", got.ResultIDs)
	}
	if !h.mgr.Muted(agents[0].ID.String()) {
		t.Fatal("Manager does not report the agent muted after dispatch")
	}
	h.istore.requestStop(h.intent.ID)
	h.loop.DrainForTest()
}

// TestClaimLoop_DispatchesSaysInOrderAndEncodesFailure covers sequence (6): two
// queued says publish in (created_at, id) order, and a Manager refusal (foreign
// agent) fails the row with a DECODED-able last_error.
func TestClaimLoop_DispatchesSaysInOrderAndEncodesFailure(t *testing.T) {
	h := newControlLoopHarness(t)
	agents := seedAgents(h.mstore, 1)

	var mu sync.Mutex
	var spoken []string
	unsub := h.bus.Subscribe(func(e voiceevent.Event) {
		if sr, ok := e.(voiceevent.SpeakRequested); ok {
			mu.Lock()
			spoken = append(spoken, sr.Text)
			mu.Unlock()
		}
	})
	defer unsub()

	first := h.istore.addControl(storage.VoiceSessionControl{
		IntentID: h.intent.ID, TenantID: h.tenant,
		Kind: storage.VoiceControlSay, AgentID: agents[0].ID.String(), SayText: "line one",
	})
	second := h.istore.addControl(storage.VoiceSessionControl{
		IntentID: h.intent.ID, TenantID: h.tenant,
		Kind: storage.VoiceControlSay, AgentID: agents[0].ID.String(), SayText: "line two",
	})
	bad := h.istore.addControl(storage.VoiceSessionControl{
		IntentID: h.intent.ID, TenantID: h.tenant,
		Kind: storage.VoiceControlSay, AgentID: uuid.NewString(), SayText: "never",
	})

	waitFor(t, 2*time.Second, func() bool {
		return h.istore.getControl(first.ID).Status == storage.VoiceControlDone &&
			h.istore.getControl(second.ID).Status == storage.VoiceControlDone &&
			h.istore.getControl(bad.ID).Status == storage.VoiceControlFailed
	})

	mu.Lock()
	order := append([]string(nil), spoken...)
	mu.Unlock()
	if len(order) != 2 || order[0] != "line one" || order[1] != "line two" {
		t.Fatalf("say publish order = %v, want [line one, line two]", order)
	}

	sentinel, ok := session.DecodeControlFailure(h.istore.getControl(bad.ID).LastError)
	if !ok || !errors.Is(sentinel, session.ErrAgentNotInCampaign) {
		t.Fatalf("failed say last_error = %q, want an encoded ErrAgentNotInCampaign",
			h.istore.getControl(bad.ID).LastError)
	}
	h.istore.requestStop(h.intent.ID)
	h.loop.DrainForTest()
}

// TestClaimLoop_TransientFinishDoesNotDoubleSay covers #503 FIX1: the row is
// CLAIMED pending→executing before the verb runs, so when the terminal write
// fails transiently the row stays 'executing' (never re-listed) and the say
// publishes EXACTLY ONCE — no double utterance from at-least-once dispatch.
func TestClaimLoop_TransientFinishDoesNotDoubleSay(t *testing.T) {
	h := newControlLoopHarness(t)
	agents := seedAgents(h.mstore, 1)

	var mu sync.Mutex
	var says int
	unsub := h.bus.Subscribe(func(e voiceevent.Event) {
		if _, ok := e.(voiceevent.SpeakRequested); ok {
			mu.Lock()
			says++
			mu.Unlock()
		}
	})
	defer unsub()

	// The FIRST FinishVoiceSessionControl fails transiently; the row is already
	// 'executing' (say published), so subsequent ticks never re-run it.
	h.istore.mu.Lock()
	h.istore.finishControlErrs = []error{errors.New("db blip")}
	h.istore.mu.Unlock()

	row := h.istore.addControl(storage.VoiceSessionControl{
		IntentID: h.intent.ID, TenantID: h.tenant,
		Kind: storage.VoiceControlSay, AgentID: agents[0].ID.String(), SayText: "once",
	})
	// Wait for the say to have published and the finish to have been attempted.
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return says >= 1
	})
	// Give several more heartbeat ticks a chance to (wrongly) re-dispatch.
	before := h.istore.heartbeatCount()
	waitFor(t, time.Second, func() bool { return h.istore.heartbeatCount() > before+5 })

	mu.Lock()
	got := says
	mu.Unlock()
	if got != 1 {
		t.Fatalf("say published %d times, want exactly 1 (executing fence blocks re-dispatch)", got)
	}
	if st := h.istore.getControl(row.ID).Status; st != storage.VoiceControlExecuting {
		t.Fatalf("control status after transient finish = %q, want executing (stranded for the sweep)", st)
	}
	h.istore.requestStop(h.intent.ID)
	h.loop.DrainForTest()
}

// TestClaimLoop_DrainBatchBudget covers #503 FIX2: one drain of many controls
// stops once the aggregate heartbeat budget is spent (so the heartbeat goroutine
// is never starved past Expiry), and the leftovers drain on the NEXT pass. A
// deterministic clock drives the cutoff — no wall-clock sleeps, and dispatch is
// driven directly (no live runSession goroutine racing the clock).
func TestClaimLoop_DrainBatchBudget(t *testing.T) {
	mstore := newFakeStore()
	seedAgents(mstore, 1)
	mgr, _ := muteManager(t, mstore)
	t.Cleanup(mgr.Shutdown)
	tenantID, _ := startMuteSession(t, mgr)

	istore := newFakeIntentStore()
	const heartbeat = 100 * time.Millisecond
	loop := session.NewClaimLoop(istore, mgr, "worker-test", slog.New(slog.DiscardHandler),
		session.ClaimLoopConfig{Poll: time.Millisecond, Heartbeat: heartbeat, Expiry: 30 * time.Second})

	// Each clock read advances by half the heartbeat: start(0), after control 1
	// now=hb/2 (< hb, continue), after control 2 now=hb (>= hb, stop) → 2 per pass.
	var clk int64
	loop.SetClockForTest(func() time.Time {
		n := atomic.AddInt64(&clk, 1)
		return time.Unix(0, 0).Add(time.Duration(n-1) * (heartbeat / 2))
	})

	intent := istore.add(tenantID, uuid.New())
	intentRow := istore.get(intent.ID)
	rows := make([]uuid.UUID, 5)
	for i := range rows {
		rows[i] = istore.addControl(storage.VoiceSessionControl{
			IntentID: intent.ID, TenantID: tenantID, Kind: storage.VoiceControlMuteAll, Muted: true,
		}).ID
	}

	// Pass 1: budget = 2 clock steps → exactly 2 done, 3 still pending.
	loop.DispatchControlsForTest(context.Background(), intentRow)
	if done, pending := countStatus(istore, rows, storage.VoiceControlDone), countStatus(istore, rows, storage.VoiceControlPending); done != 2 || pending != 3 {
		t.Fatalf("pass 1: done=%d pending=%d, want done=2 pending=3", done, pending)
	}

	// Reset the clock and drain the leftovers across two more passes.
	atomic.StoreInt64(&clk, 0)
	loop.DispatchControlsForTest(context.Background(), intentRow)
	atomic.StoreInt64(&clk, 0)
	loop.DispatchControlsForTest(context.Background(), intentRow)
	if done := countStatus(istore, rows, storage.VoiceControlDone); done != 5 {
		t.Fatalf("after three passes: done=%d, want all 5", done)
	}
}

func countStatus(istore *fakeIntentStore, ids []uuid.UUID, want storage.VoiceSessionControlStatus) int {
	n := 0
	for _, id := range ids {
		if istore.getControl(id).Status == want {
			n++
		}
	}
	return n
}

// TestClaimLoop_NoDispatchWhileNotLive covers sequence (7): during the Manager's
// end window (self-exit finalizing — Active false) queued controls are NOT
// dispatched; the rows stay pending for the sweep or the requester's budget.
func TestClaimLoop_NoDispatchWhileNotLive(t *testing.T) {
	mstore := newFakeStore()
	closeGate := make(chan struct{})
	mstore.closeGate = closeGate // parks CloseVoiceSession → holds the end window open
	mgr, _ := muteManager(t, mstore)
	istore := newFakeIntentStore()
	istore.sessionOutcome = func(id uuid.UUID) (storage.VoiceSession, error) {
		return mstore.session(id), nil
	}
	loop := session.NewClaimLoop(istore, mgr, "worker-test", slog.New(slog.DiscardHandler),
		session.ClaimLoopConfig{Poll: time.Millisecond, Heartbeat: time.Millisecond, Expiry: 30 * time.Second})

	tenantID := uuid.New()
	agents := seedAgents(mstore, 1)
	intent := istore.add(tenantID, uuid.New())
	loop.TickForTest(context.Background())
	waitFor(t, time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentLive })

	// The session self-exits but CloseVoiceSession parks on the gate: Active
	// false, Finalizing true — the not-live window.
	go func() { _, _ = mgr.Stop(context.Background(), tenantID) }()
	waitFor(t, time.Second, func() bool { return mgr.Finalizing(tenantID) })

	row := istore.addControl(storage.VoiceSessionControl{
		IntentID: intent.ID, TenantID: tenantID,
		Kind: storage.VoiceControlMuteAgent, AgentID: agents[0].ID.String(), Muted: true,
	})
	// Several heartbeat ticks inside the window: the control must stay pending.
	before := istore.heartbeatCount()
	waitFor(t, time.Second, func() bool { return istore.heartbeatCount() > before+3 })
	if got := istore.getControl(row.ID).Status; got != storage.VoiceControlPending {
		t.Fatalf("control status inside the end window = %q, want pending (no dispatch while not live)", got)
	}

	close(closeGate)
	waitFor(t, 2*time.Second, func() bool { return istore.get(intent.ID).Status == storage.VoiceIntentDone })
	// Still pending after the intent finished: dispatch never ran (the tick sweep
	// or the requester's budget cancel retires it).
	if got := istore.getControl(row.ID).Status; got != storage.VoiceControlPending {
		t.Fatalf("control status after self-exit = %q, want pending", got)
	}
	loop.DrainForTest()
}

// TestClaimLoop_TickSweepsOrphanedControls covers sequence (8): a tick fails the
// pending controls of a TERMINAL intent ('session ended'), leaving a live
// intent's queue alone.
func TestClaimLoop_TickSweepsOrphanedControls(t *testing.T) {
	mstore := newFakeStore()
	mgr := newManager(t, mstore, newBlockingRunner().run, true)
	istore := newFakeIntentStore()
	loop := newClaimLoop(t, istore, mgr)

	dead := istore.add(uuid.New(), uuid.New())
	istore.markDead(dead.ID)
	orphan := istore.addControl(storage.VoiceSessionControl{
		IntentID: dead.ID, TenantID: dead.TenantID,
		Kind: storage.VoiceControlMuteAll, Muted: true,
	})

	loop.TickForTest(context.Background())
	got := istore.getControl(orphan.ID)
	if got.Status != storage.VoiceControlFailed {
		t.Fatalf("orphaned control after tick = %+v, want failed", got)
	}
	if sentinel, ok := session.DecodeControlFailure(got.LastError); !ok || !errors.Is(sentinel, session.ErrNoActiveSession) {
		t.Fatalf("orphaned control last_error = %q, want encoded no_active_session (#503 FIX3)", got.LastError)
	}
}

// TestClaimLoop_DispatchesDirectControl covers the ADR-0059 relay: a pending
// 'direct' control sets the directive on the hosting worker's Manager (the
// Replier-visible state), finishes 'done', and a follow-up clearing row removes
// it — the (created_at, id) drain order landing set-then-clear in request order.
func TestClaimLoop_DispatchesDirectControl(t *testing.T) {
	h := newControlLoopHarness(t)
	agents := seedAgents(h.mstore, 1)
	id := agents[0].ID.String()

	set := h.istore.addControl(storage.VoiceSessionControl{
		IntentID: h.intent.ID, TenantID: h.tenant,
		Kind: storage.VoiceControlDirect, AgentID: id, SayText: "Bart lies about the key.", DirectTurns: 2,
	})
	waitFor(t, 2*time.Second, func() bool { return h.istore.getControl(set.ID).Status == storage.VoiceControlDone })
	if got := h.mgr.Directive(context.Background(), id, false); got != "Bart lies about the key." {
		t.Fatalf("Directive after relay = %q, want the relayed note", got)
	}

	clear := h.istore.addControl(storage.VoiceSessionControl{
		IntentID: h.intent.ID, TenantID: h.tenant,
		Kind: storage.VoiceControlDirect, AgentID: id, SayText: "",
	})
	waitFor(t, 2*time.Second, func() bool { return h.istore.getControl(clear.ID).Status == storage.VoiceControlDone })
	if got := h.mgr.Directive(context.Background(), id, false); got != "" {
		t.Fatalf("Directive after relayed clear = %q, want empty", got)
	}

	// A foreign agent fails the row with a decodable cause (the say precedent).
	bad := h.istore.addControl(storage.VoiceSessionControl{
		IntentID: h.intent.ID, TenantID: h.tenant,
		Kind: storage.VoiceControlDirect, AgentID: uuid.NewString(), SayText: "never",
	})
	waitFor(t, 2*time.Second, func() bool { return h.istore.getControl(bad.ID).Status == storage.VoiceControlFailed })
	if sentinel, ok := session.DecodeControlFailure(h.istore.getControl(bad.ID).LastError); !ok || !errors.Is(sentinel, session.ErrAgentNotInCampaign) {
		t.Fatalf("failed direct last_error = %q, want encoded ErrAgentNotInCampaign", h.istore.getControl(bad.ID).LastError)
	}

	h.istore.requestStop(h.intent.ID)
	h.loop.DrainForTest()
}
