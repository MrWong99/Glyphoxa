// The Idle Close tests live in `package idleclose` (white-box) so they can drive
// the unexported sweep step directly with a hand-advanced clock and an injected
// usage sampler — no wall-clock waits and no real runtime pressure (the
// determinism requirement, ADR-0019; the precedent is pkg/voice/wire's
// silence_test.go and internal/presence's elector_test.go).
package idleclose

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// base is the fixed origin every clock-driven test steps from, so a failure
// message names an absolute instant rather than "now-ish".
var base = time.Date(2026, 8, 7, 20, 0, 0, 0, time.UTC)

// testPolicy is the idle-only Policy the majority of these tests run: a 15 min
// window on a 30 s sweep, both ceilings off.
func testPolicy() Policy {
	return Policy{Window: 15 * time.Minute, Sweep: 30 * time.Second}
}

// recorder captures the reasons one enrolled Voice Session was closed with, so a
// test can assert both the reason text and that a close fired exactly once.
type recorder struct {
	mu      sync.Mutex
	reasons []string
}

func (r *recorder) close(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reasons = append(r.reasons, reason)
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reasons...)
}

// newGuard builds a Guard pinned to a manually advanced clock. The returned
// advance func moves the clock and runs exactly one sweep, which is how every
// test drives the watchdog — Run's ticker is never involved.
func newGuard(t *testing.T, p Policy) (*Guard, func(d time.Duration)) {
	t.Helper()
	var mu sync.Mutex
	clk := base
	g := New(p, slog.New(slog.DiscardHandler))
	g.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clk
	}
	// No ceilings are configured by default, so a sampler that reports nothing
	// keeps the resource guard inert without touching the real runtime.
	g.usage = func() Usage { return Usage{} }
	return g, func(d time.Duration) {
		mu.Lock()
		clk = clk.Add(d)
		now := clk
		mu.Unlock()
		g.sweep(now)
	}
}

// enroll registers a session and admits it to the sweep in one step — what every
// owner does, only split across the two calls because the owner needs the handle
// to wire its activity mark before it can declare the session live.
func enroll(g *Guard, id string, close func(string)) *Session {
	h := g.Enroll(id, close)
	h.Activate()
	return h
}

// TestGuard_ClosesSessionAfterWindowWithNoAudio is the acceptance case: a Voice
// Session that never marks a single audio frame is closed once the Idle Close
// Window elapses, and not one sweep earlier.
func TestGuard_ClosesSessionAfterWindowWithNoAudio(t *testing.T) {
	t.Parallel()
	g, advance := newGuard(t, testPolicy())
	var rec recorder
	if h := enroll(g, "s1", rec.close); h == nil {
		t.Fatal("Enroll returned nil for an enabled Policy, want a live handle")
	}

	// One tick short of the window: still inside its grace, must not close.
	advance(14 * time.Minute)
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("closed after 14m of silence with a 15m window: %v; the window is a floor, not a hint", got)
	}

	advance(time.Minute)
	got := rec.snapshot()
	if len(got) != 1 || got[0] != ReasonIdle {
		t.Fatalf("close reasons after 15m = %v, want exactly one %q — the row must carry the greppable ADR-0043 prefix", got, ReasonIdle)
	}
}

// TestGuard_MarkedAudioResetsTheWindow pins the whole point of the feature: a
// session that keeps processing audio is never closed, however long it runs.
func TestGuard_MarkedAudioResetsTheWindow(t *testing.T) {
	t.Parallel()
	g, advance := newGuard(t, testPolicy())
	var rec recorder
	h := enroll(g, "s1", rec.close)

	// Ten minutes of quiet, one frame of audio, ten more minutes: the mark moves
	// the deadline, so the cumulative 20 min never trips the 15 min window.
	advance(10 * time.Minute)
	h.Mark()
	advance(10 * time.Minute)
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("closed a session that processed audio 10m ago: %v; a Mark must restart the window", got)
	}

	// And it does trip once the audio genuinely stops for a full window.
	advance(15 * time.Minute)
	if got := rec.snapshot(); len(got) != 1 {
		t.Fatalf("close count after audio stopped for a full window = %d, want 1", len(got))
	}
}

// TestGuard_ClosesOnlyOnce guards the once-ness the spend meter gets from its own
// sync.Once (internal/spend/meter.go): the watchdog re-sweeps every 30 s while a
// closing session finishes its multi-second finalizers, and must not fire the
// callback again on each pass.
func TestGuard_ClosesOnlyOnce(t *testing.T) {
	t.Parallel()
	g, advance := newGuard(t, testPolicy())
	var rec recorder
	enroll(g, "s1", rec.close)

	advance(15 * time.Minute)
	advance(time.Minute)
	advance(time.Minute)
	if got := rec.snapshot(); len(got) != 1 {
		t.Fatalf("close fired %d times across three sweeps, want 1: %v", len(got), got)
	}
}

// TestGuard_ReleasedSessionIsNeverClosed pins the deregistration the Manager does
// when its loop returns: a released handle must not be swept, or a watchdog tick
// landing during the end window would stamp an idle end_reason onto a session
// that already stopped for another reason.
func TestGuard_ReleasedSessionIsNeverClosed(t *testing.T) {
	t.Parallel()
	g, advance := newGuard(t, testPolicy())
	var rec recorder
	h := enroll(g, "s1", rec.close)
	h.Release()

	advance(time.Hour)
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("closed a released session: %v", got)
	}
	if n := g.Enrolled(); n != 0 {
		t.Fatalf("enrolled sessions after Release = %d, want 0 — a leaked handle is a leaked map entry", n)
	}
}

// TestGuard_ReleaseIsIdempotent: the Manager releases in a defer, and a session
// already closed by the watchdog is released again on the way out.
func TestGuard_ReleaseIsIdempotent(t *testing.T) {
	t.Parallel()
	g, _ := newGuard(t, testPolicy())
	h := enroll(g, "s1", func(string) {})
	h.Release()
	h.Release()
	if n := g.Enrolled(); n != 0 {
		t.Fatalf("enrolled = %d after a double Release, want 0", n)
	}
}

// TestGuard_DisabledWindowNeverCloses is the feature-off path: an operator who
// sets the window to zero gets the pre-Idle-Close behaviour, and Enroll reports
// it by returning nil so the voice loop wires no activity tap at all.
func TestGuard_DisabledWindowNeverCloses(t *testing.T) {
	t.Parallel()
	g, advance := newGuard(t, Policy{Window: 0, Sweep: 30 * time.Second})
	var rec recorder
	if h := g.Enroll("s1", rec.close); h != nil {
		t.Fatal("Enroll returned a handle for a fully disabled Policy; the caller must be able to skip the tap")
	}
	advance(24 * time.Hour)
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("a disabled Policy closed a session: %v", got)
	}
}

// TestGuard_NilGuardEnrollsNothing keeps the composition root free of nil checks:
// a deployment that built no Guard hands nil around and every call is inert.
func TestGuard_NilGuardEnrollsNothing(t *testing.T) {
	t.Parallel()
	var g *Guard
	if h := g.Enroll("s1", func(string) {}); h != nil {
		t.Fatal("(*Guard)(nil).Enroll returned a handle, want nil")
	}
	// A nil handle must absorb both hot-path calls: the loop wires Mark
	// unconditionally in some paths.
	var h *Session
	h.Mark()
	h.CycleStarted()
	h.Activate()
	h.Release()
	g.Run(context.Background()) // must return at once, not block on a ticker
}

// TestGuard_UnactivatedSessionIsNeverClosed pins the enroll→commit window. An
// owner cannot admit a session before it enrolls — the enrollment IS what supplies
// the loop's activity mark — so there is a gap where a breach would find nothing
// to close. Left open, that callback would no-op, the handle would latch itself
// closed, and the session would run its entire life exempt from every check,
// silently. Until Activate, the sweep must pass over it entirely.
func TestGuard_UnactivatedSessionIsNeverClosed(t *testing.T) {
	t.Parallel()
	p := testPolicy()
	p.MaxCycles = 1
	p.HeapCeiling = 1 << 30
	g, advance := newGuard(t, p)
	g.usage = func() Usage { return Usage{HeapBytes: 1<<30 + 1} } // every trigger armed

	var rec recorder
	h := g.Enroll("starting", rec.close) // deliberately NOT activated
	h.CycleStarted()
	h.CycleStarted()

	advance(time.Hour)
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("closed a session the owner had not admitted yet: %v", got)
	}

	// And once admitted it is closed on the very next sweep — the gate delays the
	// check, it does not cancel it.
	h.Activate()
	advance(time.Minute)
	if got := rec.snapshot(); len(got) != 1 || got[0] != ReasonChurn {
		t.Fatalf("close reasons after Activate = %v, want one %q", got, ReasonChurn)
	}
}

// TestGuard_ActivateIsIdempotent: an owner that retries a start must not be able
// to re-arm a handle it already closed.
func TestGuard_ActivateIsIdempotent(t *testing.T) {
	t.Parallel()
	g, advance := newGuard(t, testPolicy())
	var rec recorder
	h := enroll(g, "s1", rec.close)
	h.Activate()

	advance(15 * time.Minute)
	h.Activate()
	advance(time.Minute)
	if got := rec.snapshot(); len(got) != 1 {
		t.Fatalf("close fired %d times with Activate called after the close, want 1: %v", len(got), got)
	}
}

// TestGuard_HeapCeilingShedsTheLeastRecentlyActiveSession pins the resource
// guard's victim rule. Both sessions are inside their idle window, so only the
// ceiling can fire — and it must shed exactly one, the quietest.
func TestGuard_HeapCeilingShedsTheLeastRecentlyActiveSession(t *testing.T) {
	t.Parallel()
	p := testPolicy()
	p.HeapCeiling = 1 << 30
	g, advance := newGuard(t, p)

	var quiet, busy recorder
	quietH := enroll(g, "quiet", quiet.close)
	busyH := enroll(g, "busy", busy.close)

	// Under the ceiling: nothing is shed however the marks fall.
	g.usage = func() Usage { return Usage{HeapBytes: 1 << 29} }
	quietH.Mark()
	busyH.Mark()
	advance(time.Minute)
	if len(quiet.snapshot())+len(busy.snapshot()) != 0 {
		t.Fatal("shed a session while the heap was under the ceiling")
	}

	// Over the ceiling, with `busy` the more recently active: the quiet one goes.
	g.usage = func() Usage { return Usage{HeapBytes: 1<<30 + 1} }
	busyH.Mark()
	advance(time.Minute)
	if got := quiet.snapshot(); len(got) != 1 || got[0] != ReasonResource {
		t.Fatalf("quiet session close reasons = %v, want exactly one %q", got, ReasonResource)
	}
	if got := busy.snapshot(); len(got) != 0 {
		t.Fatalf("shed the more recently active session too: %v; one sweep sheds at most one, so the freed memory can be observed before the next", got)
	}
}

// TestGuard_GoroutineCeilingSheds is the sibling ceiling: the two are independent,
// either alone is enough to shed.
func TestGuard_GoroutineCeilingSheds(t *testing.T) {
	t.Parallel()
	p := testPolicy()
	p.GoroutineCeiling = 5000
	g, advance := newGuard(t, p)
	var rec recorder
	enroll(g, "s1", rec.close)

	g.usage = func() Usage { return Usage{Goroutines: 5000} }
	advance(time.Minute)
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("shed AT the ceiling (%d goroutines): %v; the ceiling is the last allowed value", 5000, got)
	}

	g.usage = func() Usage { return Usage{Goroutines: 5001} }
	advance(time.Minute)
	if got := rec.snapshot(); len(got) != 1 || got[0] != ReasonResource {
		t.Fatalf("close reasons over the goroutine ceiling = %v, want one %q", got, ReasonResource)
	}
}

// TestGuard_ZeroCeilingsNeverShed: the ceilings are opt-in, so a deployment that
// configures only the idle window must never lose a session to memory pressure it
// did not ask to be protected from.
func TestGuard_ZeroCeilingsNeverShed(t *testing.T) {
	t.Parallel()
	g, advance := newGuard(t, testPolicy()) // ceilings 0
	var rec recorder
	h := enroll(g, "s1", rec.close)

	g.usage = func() Usage { return Usage{HeapBytes: 1 << 40, Goroutines: 1 << 20} }
	h.Mark()
	advance(time.Minute)
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("an unconfigured ceiling shed a session: %v", got)
	}
}

// TestGuard_IdleCloseDefersTheResourceShed: when a sweep already closed an idle
// session, the heap that session was holding has not been reclaimed yet —
// shedding a second one on the same tick would over-correct on a reading that is
// about to change. The next sweep, still over the ceiling, does shed.
func TestGuard_IdleCloseDefersTheResourceShed(t *testing.T) {
	t.Parallel()
	p := testPolicy()
	p.HeapCeiling = 1 << 30
	g, advance := newGuard(t, p)
	// Under the ceiling while the window runs down, so nothing is shed early and
	// the two triggers can be made to collide on one exact sweep.
	g.usage = func() Usage { return Usage{HeapBytes: 1 << 29} }

	var idle, live recorder
	enroll(g, "idle", idle.close)
	liveH := enroll(g, "live", live.close)

	// 29 sweeps of 30s = 14m30s: `live` keeps marking, so only `idle` ages.
	for range 29 {
		liveH.Mark()
		advance(30 * time.Second)
	}
	if got := idle.snapshot(); len(got) != 0 {
		t.Fatalf("idle session closed before its window elapsed: %v", got)
	}

	// The 30th sweep crosses the window AND finds the instance over the ceiling.
	g.usage = func() Usage { return Usage{HeapBytes: 1<<30 + 1} }
	liveH.Mark()
	advance(30 * time.Second)
	if got := idle.snapshot(); len(got) != 1 || got[0] != ReasonIdle {
		t.Fatalf("idle session close reasons = %v, want one %q", got, ReasonIdle)
	}
	if got := live.snapshot(); len(got) != 0 {
		t.Fatalf("shed the live session on the same sweep as an idle close: %v", got)
	}

	// Still over the ceiling on the next sweep: now the shed lands, and it lands on
	// `live` — the already-closed `idle` handle must not absorb it.
	liveH.Mark()
	advance(30 * time.Second)
	if got := live.snapshot(); len(got) != 1 || got[0] != ReasonResource {
		t.Fatalf("live session close reasons on the sweep after an idle close = %v, want one %q", got, ReasonResource)
	}
	if got := idle.snapshot(); len(got) != 1 {
		t.Fatalf("idle session closed %d times, want 1: %v", len(got), got)
	}
}

// TestGuard_ResourceShedSkipsAlreadyClosedSessions: a session the watchdog
// already closed is still enrolled until the Manager's loop returns and releases
// it, and must not be picked again as the quietest victim — otherwise a stuck
// finalizer would absorb every shed and the pressure would never be relieved.
func TestGuard_ResourceShedSkipsAlreadyClosedSessions(t *testing.T) {
	t.Parallel()
	p := Policy{Window: 15 * time.Minute, Sweep: 30 * time.Second, HeapCeiling: 1 << 30}
	g, advance := newGuard(t, p)
	g.usage = func() Usage { return Usage{HeapBytes: 1<<30 + 1} }

	var first, second recorder
	enroll(g, "first", first.close)
	secondH := enroll(g, "second", second.close)
	secondH.Mark()

	advance(time.Minute) // sheds `first` (quietest)
	secondH.Mark()
	advance(time.Minute) // `first` is closed-but-enrolled; `second` must go now
	if got := first.snapshot(); len(got) != 1 {
		t.Fatalf("first session closed %d times, want 1: %v", len(got), got)
	}
	if got := second.snapshot(); len(got) != 1 || got[0] != ReasonResource {
		t.Fatalf("second session close reasons = %v, want one %q — a closed-but-enrolled session must not absorb the next shed", got, ReasonResource)
	}
}

// TestGuard_ClosesSessionThatExceedsTheCycleCeiling is the per-session leak
// guard. Every Discord connect cycle rebuilds the per-cycle world and provably
// does not give all of it back, so a session that has cycled past its ceiling has
// leaked in proportion — whether or not it is also idle.
func TestGuard_ClosesSessionThatExceedsTheCycleCeiling(t *testing.T) {
	t.Parallel()
	p := testPolicy()
	p.MaxCycles = 3
	g, advance := newGuard(t, p)
	var rec recorder
	h := enroll(g, "s1", rec.close)

	// Keep marking so the idle window can never be what fires.
	for range 3 {
		h.CycleStarted()
		h.Mark()
		advance(time.Second)
	}
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("closed AT the cycle ceiling (%d cycles): %v; the ceiling is the last allowed value", p.MaxCycles, got)
	}

	h.CycleStarted()
	h.Mark()
	advance(time.Second)
	got := rec.snapshot()
	if len(got) != 1 || got[0] != ReasonChurn {
		t.Fatalf("close reasons after %d cycles = %v, want one %q", p.MaxCycles+1, got, ReasonChurn)
	}
}

// TestGuard_ChurnOutranksIdle: a session that is both churning and silent gets the
// churn reason, because "this session reconnected 200 times" is the actionable
// diagnosis and "nobody spoke" is its symptom.
func TestGuard_ChurnOutranksIdle(t *testing.T) {
	t.Parallel()
	p := testPolicy()
	p.MaxCycles = 1
	g, advance := newGuard(t, p)
	var rec recorder
	h := enroll(g, "s1", rec.close)
	h.CycleStarted()
	h.CycleStarted()

	advance(15 * time.Minute) // both limits breached on this one sweep
	got := rec.snapshot()
	if len(got) != 1 || got[0] != ReasonChurn {
		t.Fatalf("close reasons for a session that is both churning and idle = %v, want one %q", got, ReasonChurn)
	}
}

// TestGuard_ZeroCycleCeilingNeverCloses keeps the check opt-out.
func TestGuard_ZeroCycleCeilingNeverCloses(t *testing.T) {
	t.Parallel()
	g, advance := newGuard(t, testPolicy()) // MaxCycles 0
	var rec recorder
	h := enroll(g, "s1", rec.close)
	for range 10_000 {
		h.CycleStarted()
	}
	h.Mark()
	advance(time.Minute)
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("an unconfigured cycle ceiling closed a session after 10000 cycles: %v", got)
	}
}

// TestGuard_CycleCeilingAloneEnablesTheGuard: an operator may want churn
// protection with no idle window at all.
func TestGuard_CycleCeilingAloneEnablesTheGuard(t *testing.T) {
	t.Parallel()
	g, advance := newGuard(t, Policy{MaxCycles: 1, Sweep: time.Second})
	var rec recorder
	h := enroll(g, "s1", rec.close)
	if h == nil {
		t.Fatal("Enroll returned nil for a cycle-ceiling-only Policy, want a live handle")
	}
	advance(24 * time.Hour) // no window configured: idleness alone must not close
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("closed on idleness with no Idle Close Window configured: %v", got)
	}
	h.CycleStarted()
	h.CycleStarted()
	advance(time.Second)
	if got := rec.snapshot(); len(got) != 1 || got[0] != ReasonChurn {
		t.Fatalf("close reasons = %v, want one %q", got, ReasonChurn)
	}
}

// TestGuard_RunStopsOnContextCancel pins the goroutine's lifetime to the caller's
// context: a Voice Instance that shuts down must not leave a watchdog ticking.
func TestGuard_RunStopsOnContextCancel(t *testing.T) {
	defer goleak.VerifyNone(t)
	g := New(Policy{Window: time.Minute, Sweep: time.Millisecond}, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.Run(ctx) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of ctx cancel")
	}
}

// TestGuard_RunSweepsOnEveryTick drives Run through its injected tick channel —
// the elector's seam (internal/presence/elector.go) — so the ticker wiring itself
// is covered without a wall-clock wait.
func TestGuard_RunSweepsOnEveryTick(t *testing.T) {
	defer goleak.VerifyNone(t)
	g := New(testPolicy(), slog.New(slog.DiscardHandler))
	ticks := make(chan time.Time)
	g.ticks = ticks
	clk := base
	g.now = func() time.Time { return clk }
	g.usage = func() Usage { return Usage{} }

	var rec recorder
	enroll(g, "s1", rec.close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); g.Run(ctx) }()

	clk = base.Add(16 * time.Minute)
	ticks <- time.Time{} // unbuffered: returns only once the sweep has been taken
	ticks <- time.Time{} // a second tick proves the first sweep completed

	if got := rec.snapshot(); len(got) != 1 || got[0] != ReasonIdle {
		t.Fatalf("close reasons after a tick past the window = %v, want one %q", got, ReasonIdle)
	}
	cancel()
	<-done
}

// TestGuard_RunReturnsImmediatelyWhenDisabled: a fully disabled Policy must not
// leave a ticker running on every Voice Instance in the fleet.
func TestGuard_RunReturnsImmediatelyWhenDisabled(t *testing.T) {
	defer goleak.VerifyNone(t)
	g := New(Policy{}, slog.New(slog.DiscardHandler))
	done := make(chan struct{})
	go func() { defer close(done); g.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run blocked on a fully disabled Policy, want an immediate return")
	}
}

// TestGuard_MarkRacesSweep is the -race gate on the hot path: Mark runs on the
// audio loop (~50 calls/s per speaker) while the watchdog sweeps concurrently.
func TestGuard_MarkRacesSweep(t *testing.T) {
	t.Parallel()
	g := New(testPolicy(), slog.New(slog.DiscardHandler))
	g.usage = func() Usage { return Usage{} }
	h := enroll(g, "s1", func(string) {})

	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			h.Mark()
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 1000 {
			g.sweep(base.Add(time.Duration(i) * time.Second))
		}
		stop.Store(true)
	}()
	wg.Wait()
}

// TestPolicy_Enabled is the pure predicate the composition root branches on.
func TestPolicy_Enabled(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		p    Policy
		want bool
	}{
		"all off":           {Policy{}, false},
		"window only":       {Policy{Window: time.Minute}, true},
		"heap only":         {Policy{HeapCeiling: 1}, true},
		"goroutines only":   {Policy{GoroutineCeiling: 1}, true},
		"cycles only":       {Policy{MaxCycles: 1}, true},
		"negative window":   {Policy{Window: -time.Minute}, false},
		"sweep alone is no": {Policy{Sweep: time.Second}, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := tc.p.Enabled(); got != tc.want {
				t.Fatalf("Policy%+v.Enabled() = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

// TestGuard_SweepIntervalDefaults: a blank or non-positive cadence must not wedge
// the ticker at zero (time.NewTicker panics on a non-positive duration).
func TestGuard_SweepIntervalDefaults(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		sweep, window time.Duration
		want          time.Duration
	}{
		"explicit":               {45 * time.Second, 15 * time.Minute, 45 * time.Second},
		"zero takes default":     {0, 15 * time.Minute, defaultSweep},
		"negative takes default": {-time.Second, 15 * time.Minute, defaultSweep},
		// A window shorter than the default cadence would be checked at most once
		// per window; the cadence follows it down so the close lands on time.
		"short window pulls the cadence down": {0, 20 * time.Second, 10 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			g := New(Policy{Sweep: tc.sweep, Window: tc.window}, slog.New(slog.DiscardHandler))
			if got := g.sweepInterval(); got != tc.want {
				t.Fatalf("sweepInterval() for Policy{Sweep:%v, Window:%v} = %v, want %v", tc.sweep, tc.window, got, tc.want)
			}
		})
	}
}

// TestRuntimeUsage_ReportsSomething keeps the production sampler honest: the
// injected fakes above would happily pass over a sampler that always returned
// zero, which would silently disable both ceilings in production.
func TestRuntimeUsage_ReportsSomething(t *testing.T) {
	t.Parallel()
	u := runtimeUsage()
	if u.HeapBytes == 0 {
		t.Error("runtimeUsage().HeapBytes = 0; the heap ceiling would never fire")
	}
	if u.Goroutines == 0 {
		t.Error("runtimeUsage().Goroutines = 0; the goroutine ceiling would never fire")
	}
}

// TestGuard_MediaSuspectRecolorsIdleClose pins #633's reason split: the same
// idle breach reports media_path_dead when the media watchdog flagged the
// inbound path, so a transport fault stops masquerading as a quiet table.
func TestGuard_MediaSuspectRecolorsIdleClose(t *testing.T) {
	t.Parallel()
	g, advance := newGuard(t, testPolicy())
	var rec recorder
	h := enroll(g, "s1", rec.close)

	h.MediaSuspect()
	advance(15 * time.Minute)
	got := rec.snapshot()
	if len(got) != 1 || got[0] != ReasonMediaDead {
		t.Fatalf("close reasons = %v, want exactly one %q", got, ReasonMediaDead)
	}
}

// TestGuard_AudioClearsMediaSuspect pins the flag's retirement: audio flowing
// again proves the watchdog's rebuild worked, so a LATER quiet stretch closes
// as a plain idle table, not as a dead media path.
func TestGuard_AudioClearsMediaSuspect(t *testing.T) {
	t.Parallel()
	g, advance := newGuard(t, testPolicy())
	var rec recorder
	h := enroll(g, "s1", rec.close)

	h.MediaSuspect()
	h.Mark() // the rebuilt connection heard something
	advance(time.Minute)
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("closed prematurely: %v", got)
	}

	advance(15 * time.Minute)
	got := rec.snapshot()
	if len(got) != 1 || got[0] != ReasonIdle {
		t.Fatalf("close reasons = %v, want exactly one %q — the suspect flag must not outlive recovered audio", got, ReasonIdle)
	}
}

// TestGuard_ChurnOutranksMediaSuspect keeps the existing breach precedence: a
// session cycling itself to death names the churn even when the media path was
// also flagged, since the cycle ceiling is the more actionable end_reason.
func TestGuard_ChurnOutranksMediaSuspect(t *testing.T) {
	t.Parallel()
	p := testPolicy()
	p.MaxCycles = 2
	g, advance := newGuard(t, p)
	var rec recorder
	h := enroll(g, "s1", rec.close)

	h.MediaSuspect()
	for range 3 {
		h.CycleStarted()
	}
	advance(15 * time.Minute)
	got := rec.snapshot()
	if len(got) != 1 || got[0] != ReasonChurn {
		t.Fatalf("close reasons = %v, want exactly one %q", got, ReasonChurn)
	}
}

// TestSession_MediaSuspectNilSafe keeps the feature-off contract every other
// handle method carries.
func TestSession_MediaSuspectNilSafe(t *testing.T) {
	t.Parallel()
	var s *Session
	s.MediaSuspect() // must not panic
}
