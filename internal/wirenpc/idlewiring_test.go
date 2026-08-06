package wirenpc

// White-box tests for the Idle Close wiring (ADR-0061). They live in `package
// wirenpc` so they can drive the unexported reconnect loop with the existing
// fakeSleep/fakeClock seams — no Discord, no wall-clock waits.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestCountCycles_CountsEveryAttemptIncludingFailedOnes pins the reconnect-churn
// signal. A session that never manages to join is the WORST churn case — each
// attempt has already built the provider adapters and their http.Transports by
// the time it fails — so a cycle that errors before connecting must still count.
// Counting inside connectAndServe's success path would have missed exactly the
// sessions the ceiling exists to catch.
func TestCountCycles_CountsEveryAttemptIncludingFailedOnes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs := &fakeSleep{cancelAt: 3, cancel: cancel}
	p := reconnectPolicy{initial: time.Second, max: 8 * time.Second, factor: 2, sleep: fs.sleep}

	cycles := 0
	attempts := 0
	err := runWithReconnect(ctx, discardLogger(), p,
		countCycles(func() { cycles++ }, func(context.Context, func()) error {
			attempts++
			return errors.New("dial failed")
		}))
	if err != nil {
		t.Fatalf("runWithReconnect returned %v, want nil on a ctx-cancel exit", err)
	}
	if attempts == 0 {
		t.Fatal("the loop never attempted a cycle; the fixture is wrong")
	}
	if cycles != attempts {
		t.Fatalf("counted %d cycles across %d attempts, want one per attempt: a cycle that fails before joining still rebuilt (and leaked) the per-cycle world", cycles, attempts)
	}
}

// TestCountCycles_NilHookLeavesTheAttemptUnwrapped is the feature-off path: no
// watchdog means no wrapper, so the reconnect loop is byte-identical.
func TestCountCycles_NilHookLeavesTheAttemptUnwrapped(t *testing.T) {
	called := false
	body := func(context.Context, func()) error { called = true; return nil }
	wrapped := countCycles(nil, body)
	if err := wrapped(context.Background(), func() {}); err != nil {
		t.Fatalf("wrapped attempt returned %v, want nil", err)
	}
	if !called {
		t.Fatal("countCycles(nil, body) did not run body")
	}
}

// TestActivityInboundOptions_NilMarkAddsNoOption keeps the inbound loop untouched
// when the Voice Instance armed no watchdog — the same "feature off is
// byte-identical" discipline the tape and highlight taps follow.
func TestActivityInboundOptions_NilMarkAddsNoOption(t *testing.T) {
	if opts := activityInboundOptions(nil); opts != nil {
		t.Fatalf("activityInboundOptions(nil) = %d option(s), want none", len(opts))
	}
	if opts := activityInboundOptions(func() {}); len(opts) != 1 {
		t.Fatalf("activityInboundOptions(mark) = %d option(s), want 1", len(opts))
	}
}
