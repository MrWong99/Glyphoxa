package session_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/idleclose"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// idleGuardManager builds a Manager wired to an Idle Close watchdog running
// policy, with the watchdog's goroutine scoped to the test.
//
// The timing is shrunk rather than faked — the SetEndTimeoutForTest /
// ClaimLoopConfig{Poll: time.Millisecond} idiom this package already uses for its
// other timer-driven behaviour. What the watchdog decides at which instant is
// pinned deterministically in internal/idleclose's own white-box tests, against a
// hand-advanced clock; what these tests pin is the WIRING — that a breach reaches
// the voice_sessions row through the Manager's policy-stop path, and that the
// enrollment is created and released at the right moments.
func idleGuardManager(t *testing.T, store session.Store, run session.LoopRunner, policy idleclose.Policy) (*session.Manager, *idleclose.Guard) {
	t.Helper()
	guard := idleclose.New(policy, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go guard.Run(ctx)
	return newManagerDeps(t, store, run, true, session.Deps{IdleGuard: guard}), guard
}

// TestIdleClose_EndsSessionCleanly is the acceptance case for the whole feature
// (ADR-0061), and it deliberately mirrors TestHardCap_EndsSessionCleanly beat for
// beat: an Idle Close is the same KIND of event as a hard spend cap — a
// deliberate policy stop — so it must land the same way.
//
// The production incident this exists for: a Voice Session with the Rollover Tape
// armed ran for a full day in a channel nobody ever joined, holding a Discord
// voice connection, a tape ring per consented Speaker, a 5-second consent poller
// and a claim-plane slot the whole time. Nothing in the system could end it.
//
// The row must close 'ended' and NOT 'failed' (ADR-0043: 'failed' is for faults,
// and this session did exactly what it was configured to do), it must carry the
// greppable end_reason, the run ctx must be cancelled, and the concurrency slot
// must be freed so the Tenant can start again immediately.
func TestIdleClose_EndsSessionCleanly(t *testing.T) {
	store := newFakeStore()
	runner := newReRunner()
	mgr, guard := idleGuardManager(t, store, runner.run, idleclose.Policy{
		Window: time.Millisecond,
		Sweep:  time.Millisecond,
	})

	vs, err := mgr.Start(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started

	// Nothing ever marks activity, so the watchdog closes the session on one of its
	// next sweeps; the trip cancels the run ctx on a goroutine, like the hard cap.
	waitInactive(t, mgr)

	closed := store.session(vs.ID)
	if closed.Status != storage.VoiceSessionEnded {
		t.Fatalf("idle-closed row status = %q, want %q: a policy stop is not a fault (ADR-0043)",
			closed.Status, storage.VoiceSessionEnded)
	}
	if closed.EndReason == nil || *closed.EndReason != idleclose.ReasonIdle {
		t.Fatalf("idle-closed end_reason = %v, want %q", closed.EndReason, idleclose.ReasonIdle)
	}
	if !runner.wasCancelled() {
		t.Fatal("an Idle Close must cancel the run ctx; without it the voice loop keeps holding the Discord connection it was closed to release")
	}
	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("second Start after an Idle Close must succeed (the concurrency slot is freed), got %v", err)
	}
	mgr.Shutdown()
	if n := guard.Enrolled(); n != 0 {
		t.Errorf("watchdog still tracks %d session(s) after every loop returned, want 0: a stranded enrollment keeps the close callback — and the session it captured — alive forever", n)
	}
}

// TestIdleClose_WiresTheActivitySignalsOntoTheSessionConfig pins the two seams the
// voice loop consumes. Without them the watchdog would close every session on
// schedule regardless of what the table was doing — the feature would be a timer,
// not an idle detector — and no unit test of internal/idleclose could catch it,
// because there the marks are called by hand.
func TestIdleClose_WiresTheActivitySignalsOntoTheSessionConfig(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	// A window long enough that the session survives the assertions below.
	mgr, _ := idleGuardManager(t, store, runner.run, idleclose.Policy{
		Window: time.Hour,
		Sweep:  time.Millisecond,
	})
	t.Cleanup(mgr.Shutdown)

	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started

	cfg := runner.cfg()
	if cfg.Activity == nil {
		t.Error("cfg.Activity is nil with a Guard wired: the inbound audio loop would mark nothing and every session would age out on schedule")
	}
	if cfg.ConnectCycle == nil {
		t.Error("cfg.ConnectCycle is nil with a Guard wired: reconnect churn would never be counted")
	}
	// They must be callable from the loop's goroutines without further setup.
	cfg.Activity()
	cfg.ConnectCycle()
}

// TestIdleClose_UnwiredGuardLeavesTheLoopUntouched is the feature-off path
// (ADR-0046's "no caps ⇒ no meter, no tee, no gate" discipline applied here): a
// deployment with no watchdog must produce a session config byte-identical to the
// pre-Idle-Close one, so the audio loop adds no tap at all.
func TestIdleClose_UnwiredGuardLeavesTheLoopUntouched(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	mgr := newManager(t, store, runner.run, true) // Deps.IdleGuard nil
	t.Cleanup(mgr.Shutdown)

	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started

	cfg := runner.cfg()
	if cfg.Activity != nil {
		t.Error("cfg.Activity is set with no Guard wired: the feature-off path must add no tap to the audio loop")
	}
	if cfg.ConnectCycle != nil {
		t.Error("cfg.ConnectCycle is set with no Guard wired")
	}
}

// TestIdleClose_DisabledPolicyEnrollsNothing: an operator who switched Idle Close
// off gets a Guard that exists but tracks nothing, and sessions that run exactly
// as they did before.
func TestIdleClose_DisabledPolicyEnrollsNothing(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	mgr, guard := idleGuardManager(t, store, runner.run, idleclose.Policy{}) // every check off
	t.Cleanup(mgr.Shutdown)

	if _, err := mgr.Start(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started

	if n := guard.Enrolled(); n != 0 {
		t.Fatalf("a disabled Policy enrolled %d session(s), want 0", n)
	}
	if runner.cfg().Activity != nil {
		t.Error("a disabled Policy still wired cfg.Activity")
	}
	// Give the (returned-immediately) Run goroutine every chance to close something.
	time.Sleep(20 * time.Millisecond)
	if !mgr.AnyLive() {
		t.Fatal("a disabled Policy ended the session")
	}
}

// TestIdleClose_ChurnEndsSessionWithItsOwnReason pins the second trigger end to
// end: a session that keeps reconnecting leaks a little more of the per-cycle
// world every time — a disgo audio-sender goroutine, an http.Transport per
// provider adapter with no idle timeout, a Silero session per Speaker Lane — so
// past its ceiling it is closed, and the row says churn rather than idleness so an
// operator reads the actual diagnosis.
func TestIdleClose_ChurnEndsSessionWithItsOwnReason(t *testing.T) {
	store := newFakeStore()
	runner := newReRunner()
	mgr, _ := idleGuardManager(t, store, runner.run, idleclose.Policy{
		Window:    time.Hour, // long: only churn can fire
		Sweep:     time.Millisecond,
		MaxCycles: 2,
	})

	vs, err := mgr.Start(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started

	cycle := runner.cfg().ConnectCycle
	for range 3 { // one past the ceiling
		cycle()
	}
	waitInactive(t, mgr)

	closed := store.session(vs.ID)
	if closed.Status != storage.VoiceSessionEnded {
		t.Fatalf("churn-closed row status = %q, want %q", closed.Status, storage.VoiceSessionEnded)
	}
	if closed.EndReason == nil || *closed.EndReason != idleclose.ReasonChurn {
		t.Fatalf("churn-closed end_reason = %v, want %q", closed.EndReason, idleclose.ReasonChurn)
	}
	mgr.Shutdown()
}

// TestIdleClose_StoppedSessionIsNotClosedTwice: a GM pressing End while the
// watchdog was about to fire must not leave a phantom idle end_reason on a row
// that closed for a different cause. The Manager's policy-stop guard
// (m.active[...] != as || as.ended) is what makes that safe; this pins it from
// the outside.
func TestIdleClose_StoppedSessionIsNotClosedTwice(t *testing.T) {
	store := newFakeStore()
	runner := newBlockingRunner()
	mgr, guard := idleGuardManager(t, store, runner.run, idleclose.Policy{
		Window: time.Hour,
		Sweep:  time.Millisecond,
	})
	t.Cleanup(mgr.Shutdown)

	tenant := uuid.New()
	vs, err := mgr.Start(context.Background(), tenant, uuid.New())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started

	if _, err := mgr.Stop(context.Background(), tenant); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// The enrollment is gone the moment the loop returned, so no later sweep can
	// reach this session at all.
	if n := guard.Enrolled(); n != 0 {
		t.Fatalf("watchdog still tracks %d session(s) after a GM Stop, want 0", n)
	}
	closed := store.session(vs.ID)
	if closed.EndReason != nil {
		t.Fatalf("a GM-stopped row carries end_reason %q, want NULL: a clean Stop has no policy reason", *closed.EndReason)
	}
}
