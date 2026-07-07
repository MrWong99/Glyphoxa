package wirenpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeSleep records the backoff delays runWithReconnect asks it to wait and
// cancels ctx after a fixed number of calls, so a reconnect loop that would
// otherwise spin forever terminates deterministically with no real waiting. It
// is the injected reconnectPolicy.sleep — the seam that lets these tests drive
// the backoff schedule (issue #44) without Discord or wall-clock time.
type fakeSleep struct {
	delays    []time.Duration
	cancelAt  int
	cancel    context.CancelFunc
	callCount int
}

func (f *fakeSleep) sleep(ctx context.Context, d time.Duration) error {
	f.callCount++
	f.delays = append(f.delays, d)
	if f.callCount >= f.cancelAt {
		f.cancel()
	}
	// Honor a ctx that is (now) cancelled, mirroring the real sleepCtx contract:
	// a backoff sleep on a cancelled ctx returns the ctx error so the loop exits.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// fakeClock is the injected reconnectPolicy.now: tests advance it manually to
// fake a session serving for minutes without wall-clock waits. connected() and
// Advance both run on runWithReconnect's goroutine (attempt calls the callback
// synchronously), so no locking.
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// TestRunWithReconnect_RetriesWithGrowingBackoff pins the core issue-#44
// invariant: when Discord never connects (attempt always errors), the loop does
// not exit — it retries on a capped-exponential schedule. With initial=1s
// factor=2 the first three backoffs are 1s, 2s, 4s, and a SIGTERM-style ctx
// cancel (here driven by the fake sleep after 3 waits) returns a clean nil so
// the pod exits 0 rather than crashlooping.
func TestRunWithReconnect_RetriesWithGrowingBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs := &fakeSleep{cancelAt: 3, cancel: cancel}
	p := reconnectPolicy{initial: time.Second, max: 8 * time.Second, factor: 2, sleep: fs.sleep}

	calls := 0
	err := runWithReconnect(ctx, discardLogger(), p,
		func(ctx context.Context, connected func()) error {
			calls++
			return errors.New("dial tcp: connection refused")
		})

	if err != nil {
		t.Fatalf("runWithReconnect returned %v, want nil on ctx-cancel (clean exit, no crashloop)", err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if !equalDelays(fs.delays, want) {
		t.Errorf("backoff delays = %v, want %v (capped exponential)", fs.delays, want)
	}
	if calls != 3 {
		t.Errorf("attempt called %d times, want 3", calls)
	}
}

// TestRunWithReconnect_ResetsBackoffAfterHealthySession pins the reset contract
// AND that the schedule re-grows from the reset baseline: a session that serves
// at least the healthy threshold and only later drops must reconnect on the
// INITIAL delay, not inherit the grown backoff from earlier failures — and if
// reconnection then keeps failing it must back off again 1s, 2s, 4s. attempt
// errors twice without connecting (delays 1s, 2s), the 3rd call connects and
// serves past healthyAfter before dropping (reset → 1s), and calls 4–5 fail
// again, so delays[2:5] must be 1s, 2s, 4s. This catches both a missing reset
// and a reset that fails to re-advance. Adjusted for issue #141: the reset used
// to key on connected() alone (the buggy reset point); it now requires the
// healthy serve duration, faked here by advancing the injected clock.
func TestRunWithReconnect_ResetsBackoffAfterHealthySession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs := &fakeSleep{cancelAt: 6, cancel: cancel}
	clk := &fakeClock{t: time.Unix(0, 0)}
	p := defaultReconnectPolicy() // healthyAfter = healthySessionDuration
	p.sleep = fs.sleep
	p.now = clk.Now

	calls := 0
	err := runWithReconnect(ctx, discardLogger(), p,
		func(ctx context.Context, connected func()) error {
			calls++
			if calls == 3 {
				connected()                            // session established...
				clk.Advance(healthySessionDuration)    // ...serves the full healthy threshold...
				return errors.New("gateway went away") // ...then drops
			}
			return errors.New("connection refused")
		})

	if err != nil {
		t.Fatalf("runWithReconnect returned %v, want nil", err)
	}
	if len(fs.delays) < 5 {
		t.Fatalf("recorded %d delays, want >= 5", len(fs.delays))
	}
	// delays[2] is the backoff AFTER the healthy 3rd attempt — the initial delay
	// (reset fired). delays[3], delays[4] prove the schedule re-grows from there.
	wantAfterReset := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if !equalDelays(fs.delays[2:5], wantAfterReset) {
		t.Errorf("backoff after a healthy session = %v, want %v (reset after healthy serve, then re-grow)", fs.delays[2:5], wantAfterReset)
	}
}

// TestRunWithReconnect_ImmediatePostJoinFailureKeepsBackoffGrowing pins the
// issue-#141 fix: a cycle that JOINS successfully (connected() fires) but then
// fails immediately — the codec-less build's ErrCodecUnavailable, a persistent
// VAD/ONNX init failure — must count as a connect failure, NOT a healthy
// session. The delays must keep growing exponentially to the cap; a reset here
// would hammer Discord's voice join at 1 Hz forever (the exact failure mode
// the #45 backoff was written to prevent). Uses the production
// defaultReconnectPolicy (real clock: an immediate failure serves ~0s, far
// under any healthy threshold) with only the sleep seam faked.
func TestRunWithReconnect_ImmediatePostJoinFailureKeepsBackoffGrowing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs := &fakeSleep{cancelAt: 7, cancel: cancel}
	p := defaultReconnectPolicy() // initial 1s, max 30s, factor 2
	p.sleep = fs.sleep

	err := runWithReconnect(ctx, discardLogger(), p,
		func(ctx context.Context, connected func()) error {
			connected() // voice join succeeded...
			return errors.New("wire: opus codec unavailable")
		})

	if err != nil {
		t.Fatalf("runWithReconnect returned %v, want nil on ctx-cancel", err)
	}
	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	if !equalDelays(fs.delays, want) {
		t.Errorf("backoff delays = %v, want %v (join-then-immediate-fail must keep growing, never reset to initial)", fs.delays, want)
	}
}

// TestRunWithReconnect_SubThresholdSessionKeepsBackoffGrowing pins the
// boundary: a session that joins and serves for a while — but less than the
// healthy threshold — before dropping is still a connect failure, not a healthy
// session. Serving healthyAfter-1s each cycle must never reset the delay.
// Distinct from the immediate-failure test: some serving happened, just not
// enough to forgive past failures.
func TestRunWithReconnect_SubThresholdSessionKeepsBackoffGrowing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs := &fakeSleep{cancelAt: 4, cancel: cancel}
	clk := &fakeClock{t: time.Unix(0, 0)}
	p := defaultReconnectPolicy()
	p.sleep = fs.sleep
	p.now = clk.Now

	err := runWithReconnect(ctx, discardLogger(), p,
		func(ctx context.Context, connected func()) error {
			connected()                                       // join succeeded...
			clk.Advance(healthySessionDuration - time.Second) // ...serves, but one second short...
			return errors.New("gateway went away")            // ...then drops
		})

	if err != nil {
		t.Fatalf("runWithReconnect returned %v, want nil on ctx-cancel", err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if !equalDelays(fs.delays, want) {
		t.Errorf("backoff delays = %v, want %v (sub-threshold sessions must not reset the backoff)", fs.delays, want)
	}
}

// TestRunWithReconnect_StopsCleanOnCtxCancelInAttempt models SIGTERM landing
// mid-serve: attempt cancels ctx and then returns an error. The loop must NOT
// treat that as a transient failure to back off from — it must see the cancelled
// ctx, skip the sleep entirely, and exit clean (nil). This is the fix for the
// shutdown path exiting 1.
func TestRunWithReconnect_StopsCleanOnCtxCancelInAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs := &fakeSleep{cancelAt: 1, cancel: cancel}
	p := reconnectPolicy{initial: time.Second, max: 30 * time.Second, factor: 2, sleep: fs.sleep}

	calls := 0
	err := runWithReconnect(ctx, discardLogger(), p,
		func(ctx context.Context, connected func()) error {
			calls++
			cancel() // SIGTERM during serve
			return errors.New("serve interrupted")
		})

	if err != nil {
		t.Fatalf("runWithReconnect returned %v, want nil on shutdown", err)
	}
	if fs.callCount != 0 {
		t.Errorf("sleep called %d times, want 0 (no backoff after a shutdown cancel)", fs.callCount)
	}
	if calls != 1 {
		t.Errorf("attempt called %d times, want 1", calls)
	}
}

// TestRunWithReconnect_CleanSessionCloseReconnects pins that a clean
// session-close (attempt returns nil) with ctx still alive is a LOST connection,
// not a successful shutdown: the loop must back off and reconnect, not exit. A
// dropped Discord gateway often returns no error, so a nil from attempt while
// ctx lives must still reconnect.
func TestRunWithReconnect_CleanSessionCloseReconnects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs := &fakeSleep{cancelAt: 1, cancel: cancel}
	p := reconnectPolicy{initial: time.Second, max: 30 * time.Second, factor: 2, sleep: fs.sleep}

	err := runWithReconnect(ctx, discardLogger(), p,
		func(ctx context.Context, connected func()) error {
			return nil // session closed cleanly, ctx still alive
		})

	if err != nil {
		t.Fatalf("runWithReconnect returned %v, want nil", err)
	}
	if fs.callCount != 1 {
		t.Errorf("sleep called %d times, want 1 (a clean session close reconnects, it is not a shutdown)", fs.callCount)
	}
}

// TestRunWithReconnect_FatalErrorStopsImmediately pins the #123 core: a
// connect-and-serve attempt that returns a FATAL, non-retryable gateway rejection
// (a wrapped close 4004) makes the loop STOP at once — it returns the classified
// *FatalError, calls attempt exactly once, and NEVER sleeps. A transient failure
// still backs off (the existing growing-backoff tests pin that), so this only
// changes the terminal case.
func TestRunWithReconnect_FatalErrorStopsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// cancelAt is far past the single expected attempt: if the loop ever sleeps,
	// callCount goes non-zero and the test fails before any cancel would fire.
	fs := &fakeSleep{cancelAt: 99, cancel: cancel}
	p := reconnectPolicy{initial: time.Second, max: 30 * time.Second, factor: 2, sleep: fs.sleep}

	fatal := fmt.Errorf("wirenpc: open gateway: %w",
		&websocket.CloseError{Code: 4004, Text: "Authentication failed"})
	calls := 0
	err := runWithReconnect(ctx, discardLogger(), p,
		func(ctx context.Context, connected func()) error {
			calls++
			return fatal
		})

	var fe *FatalError
	if !errors.As(err, &fe) {
		t.Fatalf("runWithReconnect returned %v, want a *FatalError", err)
	}
	if fe.Reason != ReasonInvalidBotToken {
		t.Errorf("fatal reason = %q, want %q", fe.Reason, ReasonInvalidBotToken)
	}
	if calls != 1 {
		t.Errorf("attempt called %d times, want 1 (a fatal error must not retry)", calls)
	}
	if fs.callCount != 0 {
		t.Errorf("sleep called %d times, want 0 (no backoff on a fatal error)", fs.callCount)
	}
}

// TestNextDelay_CappedExponential pins the schedule arithmetic: the delay
// doubles each step until it hits the cap, then stays there, and a value already
// past a clean power of two still clamps to max rather than overshooting.
func TestNextDelay_CappedExponential(t *testing.T) {
	p := reconnectPolicy{initial: time.Second, max: 8 * time.Second, factor: 2}

	steps := []struct{ in, want time.Duration }{
		{1 * time.Second, 2 * time.Second},
		{2 * time.Second, 4 * time.Second},
		{4 * time.Second, 8 * time.Second},
		{8 * time.Second, 8 * time.Second}, // capped, stays
		{6 * time.Second, 8 * time.Second}, // 6*2=12 > 8 -> cap, no overshoot
	}
	for _, s := range steps {
		if got := nextDelay(s.in, p); got != s.want {
			t.Errorf("nextDelay(%v) = %v, want %v", s.in, got, s.want)
		}
	}
}

// TestSleepCtx_ReturnsPromptlyOnCancel pins that an already-cancelled ctx makes
// sleepCtx return its error immediately rather than waiting the full duration —
// the property that lets a SIGTERM during backoff exit fast instead of stalling
// shutdown for up to the max backoff.
func TestSleepCtx_ReturnsPromptlyOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	start := time.Now()
	err := sleepCtx(ctx, time.Hour)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("sleepCtx err = %v, want context.Canceled", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("sleepCtx took %v on a cancelled ctx, want < 100ms (must not wait the full duration)", elapsed)
	}
}

// TestSleepCtx_CompletesAfterDuration pins the happy path: with a live ctx,
// sleepCtx waits out the duration and returns nil (the timer fired, not a
// cancel), so a normal backoff actually delays.
func TestSleepCtx_CompletesAfterDuration(t *testing.T) {
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Errorf("sleepCtx returned %v, want nil after the duration elapsed", err)
	}
}

// TestRun_BadGuildID_FatalNoRetry pins that config validation stays FATAL and
// happens BEFORE the reconnect loop: a guild ID that can never parse is a
// permanent error, so Run must return it promptly (mentioning the guild) instead
// of retrying forever. The parse failure short-circuits before any Discord
// dial, so the test needs no token, no network, and cannot hang.
func TestRun_BadGuildID_FatalNoRetry(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Config{Guild: "not-a-snowflake", Channel: "123"})
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil for a bad guild ID, want a fatal parse error")
		}
		if !strings.Contains(err.Error(), "guild") {
			t.Errorf("Run error = %q, want it to mention the guild", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return promptly on a bad guild ID; it must fail at parse before any retry loop")
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func equalDelays(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
