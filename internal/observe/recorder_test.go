package observe

import (
	"context"
	"errors"
	"testing"
)

// TestCallOutcome_ClassifiesErrAndCtx pins the shared provider-call classifier
// (#125): a nil error is OK; a non-nil ctx error (deadline/cancel) is a
// timeout-shaped outcome regardless of the returned err; any other error is a
// vendor error. It mirrors the agenttool adapter's existing rule so the three
// stages agree on the outcome label.
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

	t.Run("err with cancelled ctx is timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got := CallOutcome(ctx, context.Canceled); got != OutcomeTimeout {
			t.Errorf("CallOutcome(cancelled, err) = %q, want timeout", got)
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
