package retry_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
)

// fakeSleep records the backoffs it was asked to pause for and returns without
// blocking, so a test exercises the retry loop without wall-clock time
// (ADR-0021). A nil onSleep is a no-op that always succeeds.
func fakeSleep(record *[]time.Duration) func(context.Context, time.Duration) error {
	return func(_ context.Context, d time.Duration) error {
		*record = append(*record, d)
		return nil
	}
}

// fixedRand returns a constant jitter fraction so a full-jitter backoff is
// deterministic in a test. 1.0 yields the ceiling of the jitter window.
func fixedRand(f float64) func() float64 { return func() float64 { return f } }

func retryableErr() error {
	return &providererr.HTTPError{Op: "test.Op", StatusCode: 503, Status: "503 Service Unavailable", Body: "down"}
}

// TestDo_FirstTrySuccess: a call that succeeds first time runs exactly once and
// never sleeps.
func TestDo_FirstTrySuccess(t *testing.T) {
	var sleeps []time.Duration
	attempts := 0
	got, err := retry.Do(context.Background(), retry.Policy{Sleep: fakeSleep(&sleeps), Rand: fixedRand(1)},
		func(context.Context) (string, error) {
			attempts++
			return "ok", nil
		})
	if err != nil {
		t.Fatalf("Do returned error: %v", err)
	}
	if got != "ok" {
		t.Errorf("result = %q, want ok", got)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	if len(sleeps) != 0 {
		t.Errorf("sleeps = %d, want 0", len(sleeps))
	}
}

// TestDo_RetryableThenSuccess: one retryable failure then success completes in
// two attempts with one backoff.
func TestDo_RetryableThenSuccess(t *testing.T) {
	var sleeps []time.Duration
	attempts := 0
	got, err := retry.Do(context.Background(), retry.Policy{Sleep: fakeSleep(&sleeps), Rand: fixedRand(1)},
		func(context.Context) (string, error) {
			attempts++
			if attempts == 1 {
				return "", retryableErr()
			}
			return "ok", nil
		})
	if err != nil {
		t.Fatalf("Do returned error: %v", err)
	}
	if got != "ok" {
		t.Errorf("result = %q, want ok", got)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if len(sleeps) != 1 {
		t.Errorf("sleeps = %d, want 1", len(sleeps))
	}
}

// TestDo_ExhaustsAttempts: a call that keeps returning a retryable error stops
// after exactly MaxAttempts tries and returns the last error.
func TestDo_ExhaustsAttempts(t *testing.T) {
	var sleeps []time.Duration
	attempts := 0
	last := retryableErr()
	_, err := retry.Do(context.Background(), retry.Policy{MaxAttempts: 3, Sleep: fakeSleep(&sleeps), Rand: fixedRand(0.5)},
		func(context.Context) (string, error) {
			attempts++
			return "", last
		})
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if !errors.Is(err, last) {
		t.Errorf("err = %v, want the last retryable error", err)
	}
	// 3 attempts → 2 backoffs between them.
	if len(sleeps) != 2 {
		t.Errorf("sleeps = %d, want 2", len(sleeps))
	}
}

// TestDo_NonRetryableImmediate: a non-retryable error fails fast on the first
// attempt with no sleep.
func TestDo_NonRetryableImmediate(t *testing.T) {
	var sleeps []time.Duration
	attempts := 0
	want := &providererr.HTTPError{Op: "test.Op", StatusCode: 401, Status: "401 Unauthorized", Body: "bad key"}
	_, err := retry.Do(context.Background(), retry.Policy{Sleep: fakeSleep(&sleeps), Rand: fixedRand(1)},
		func(context.Context) (string, error) {
			attempts++
			return "", want
		})
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want the non-retryable error", err)
	}
	if len(sleeps) != 0 {
		t.Errorf("sleeps = %d, want 0", len(sleeps))
	}
}

// TestDo_DefaultMaxAttempts: a zero-value Policy defaults to 3 total attempts.
func TestDo_DefaultMaxAttempts(t *testing.T) {
	var sleeps []time.Duration
	attempts := 0
	_, _ = retry.Do(context.Background(), retry.Policy{Sleep: fakeSleep(&sleeps), Rand: fixedRand(0)},
		func(context.Context) (string, error) {
			attempts++
			return "", retryableErr()
		})
	if attempts != 3 {
		t.Errorf("default attempts = %d, want 3", attempts)
	}
}

// TestRetryable is the classification table (ADR-0044 §Policy defaults).
func TestRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"http 429", &providererr.HTTPError{StatusCode: 429}, true},
		{"http 500", &providererr.HTTPError{StatusCode: 500}, true},
		{"http 503", &providererr.HTTPError{StatusCode: 503}, true},
		{"http 400", &providererr.HTTPError{StatusCode: 400}, false},
		{"http 401", &providererr.HTTPError{StatusCode: 401}, false},
		{"http 403", &providererr.HTTPError{StatusCode: 403}, false},
		{"net.OpError", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"context.Canceled", context.Canceled, false},
		{"context.DeadlineExceeded", context.DeadlineExceeded, false},
		{"prose error", errors.New("something went wrong"), false},
		{"nil", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := retry.Retryable(tc.err); got != tc.want {
				t.Errorf("Retryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestRetryable_WrappedHTTPError: classification is via errors.As, so a wrapped
// HTTPError still classifies by its status code.
func TestRetryable_WrappedHTTPError(t *testing.T) {
	inner := &providererr.HTTPError{StatusCode: 500, Op: "x", Status: "500", Body: "b"}
	wrapped := fmt.Errorf("orchestrator.STT.Transcribe: %w", inner)
	if !retry.Retryable(wrapped) {
		t.Error("wrapped 500 HTTPError should be retryable")
	}
}

// TestDo_DeadlineGuard: with a context deadline shorter than the next backoff,
// Do gives up rather than sleeping past the deadline — it never extends it
// (ADR-0044). The failed attempt ran; no sleep happened.
func TestDo_DeadlineGuard(t *testing.T) {
	var sleeps []time.Duration
	attempts := 0
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	last := retryableErr()
	_, err := retry.Do(ctx, retry.Policy{
		MaxAttempts: 5,
		BaseDelay:   250 * time.Millisecond, // first backoff (fixedRand 1) = 250ms > 100ms remaining
		Sleep:       fakeSleep(&sleeps),
		Rand:        fixedRand(1),
	}, func(context.Context) (string, error) {
		attempts++
		return "", last
	})
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (gave up before the second try)", attempts)
	}
	if len(sleeps) != 0 {
		t.Errorf("sleeps = %d, want 0 (never slept past the deadline)", len(sleeps))
	}
	if !errors.Is(err, last) {
		t.Errorf("err = %v, want the last provider error", err)
	}
}

// TestDo_CancelMidBackoff: a context cancelled while the backoff sleep is
// blocking makes Do return the context error promptly — a barge-in reads as a
// cancel, not a provider failure (ADR-0027).
func TestDo_CancelMidBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Sleep blocks until ctx is cancelled, then returns its error — the real
	// timerSleep contract, driven by the test cancelling ctx.
	blockingSleep := func(sctx context.Context, _ time.Duration) error {
		cancel() // simulate the barge landing during the pause
		<-sctx.Done()
		return sctx.Err()
	}
	attempts := 0
	_, err := retry.Do(ctx, retry.Policy{Sleep: blockingSleep, Rand: fixedRand(1)},
		func(context.Context) (string, error) {
			attempts++
			return "", retryableErr()
		})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (cancel aborted before the second try)", attempts)
	}
}

// TestDo_CancelBeforeFirstAttempt: an already-cancelled context returns the ctx
// error without running the op.
func TestDo_CancelBeforeFirstAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	_, err := retry.Do(ctx, retry.Policy{Rand: fixedRand(1)},
		func(context.Context) (string, error) {
			attempts++
			return "ok", nil
		})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if attempts != 0 {
		t.Errorf("attempts = %d, want 0 (never ran under a dead ctx)", attempts)
	}
}

// TestDo_CtxErrorFromOpNotRetried: when the op returns because the ctx expired
// (a hung provider hitting the total budget), Do returns the ctx error at once —
// it does not retry, so the budget is never multiplied (the #91 wedge guard).
func TestDo_CtxErrorFromOpNotRetried(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	attempts := 0
	_, err := retry.Do(ctx, retry.Policy{Rand: fixedRand(1)},
		func(c context.Context) (string, error) {
			attempts++
			<-c.Done() // hang until the total budget fires
			return "", c.Err()
		})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (a fired budget is not retried)", attempts)
	}
}
