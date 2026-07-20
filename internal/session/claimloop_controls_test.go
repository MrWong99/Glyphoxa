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
	if got.Status != storage.VoiceControlFailed || got.LastError != "session ended" {
		t.Fatalf("orphaned control after tick = %+v, want failed 'session ended'", got)
	}
}
