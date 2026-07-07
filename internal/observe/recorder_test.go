package observe

import (
	"context"
	"errors"
	"testing"
)

// TestCallOutcome_ClassifiesErrAndCtx pins the shared provider-call classifier
// (#125, refined #239): a nil error is OK; a context.Canceled ctx is CANCELED (a
// caller barge-in, not a fault); a context.DeadlineExceeded ctx is TIMEOUT (a
// fault); any other error is a vendor error. The three stages code against this so
// they agree on the outcome label — and on which outcomes count as errors.
func TestCallOutcome_ClassifiesErrAndCtx(t *testing.T) {
	t.Run("nil err is ok", func(t *testing.T) {
		if got := CallOutcome(context.Background(), nil); got != OutcomeOK {
			t.Errorf("CallOutcome(bg, nil) = %q, want ok", got)
		}
	})

	t.Run("err with live ctx is error", func(t *testing.T) {
		if got := CallOutcome(context.Background(), errors.New("provider 500")); got != OutcomeError {
			t.Errorf("CallOutcome(bg, err) = %q, want error", got)
		}
	})

	t.Run("err with cancelled ctx is canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got := CallOutcome(ctx, context.Canceled); got != OutcomeCanceled {
			t.Errorf("CallOutcome(cancelled, err) = %q, want canceled (a barge-in is not a fault)", got)
		}
	})

	t.Run("err with deadline-exceeded ctx is timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 0)
		defer cancel()
		<-ctx.Done()
		if got := CallOutcome(ctx, context.DeadlineExceeded); got != OutcomeTimeout {
			t.Errorf("CallOutcome(deadline, err) = %q, want timeout", got)
		}
	})
}

// TestOutcome_IsFault pins which outcomes bump provider_errors: error and timeout
// are faults; ok and canceled are not (a barge-in must never inflate the error
// ratio).
func TestOutcome_IsFault(t *testing.T) {
	for _, tc := range []struct {
		outcome Outcome
		fault   bool
	}{
		{OutcomeOK, false},
		{OutcomeError, true},
		{OutcomeTimeout, true},
		{OutcomeCanceled, false},
	} {
		if got := tc.outcome.IsFault(); got != tc.fault {
			t.Errorf("Outcome(%q).IsFault() = %v, want %v", tc.outcome, got, tc.fault)
		}
	}
}
