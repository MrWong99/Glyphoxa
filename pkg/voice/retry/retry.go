// Package retry is the bounded, deadline-aware backoff helper the orchestrator
// stages wrap their provider start-calls in (ADR-0044). It lives ONCE here rather
// than duplicated inside each adapter, so the STT, TTS and LLM stages share one
// policy and the orchestrator's per-turn deadline stays the authoritative bound.
//
// The two exports are [Do] (run an operation with backoff) and [Retryable]
// (classify an error). Time is injected — [Policy.Sleep] and [Policy.Rand] — so
// cassette suites are deterministic and never sleep wall-clock (ADR-0021).
package retry

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"net"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
)

const (
	// defaultMaxAttempts is the total number of tries (first + retries) a
	// zero-value Policy makes. Three is the ADR-0044 default: two retries is
	// enough to ride out a single transient 429/5xx without risking a slow pile-up
	// against the per-turn deadline.
	defaultMaxAttempts = 3
	// defaultBaseDelay is the first backoff's ceiling; each retry doubles it.
	defaultBaseDelay = 250 * time.Millisecond
	// defaultMaxDelay caps any single backoff so exponential growth cannot pause a
	// single retry longer than a conversational eyeblink.
	defaultMaxDelay = time.Second
)

// Policy configures [Do]'s bounded backoff. The zero value is valid and applies
// the production defaults: 3 attempts, 250ms→1s full-jitter, a real timer sleep,
// math/rand jitter, no logging. Stages inject only [Policy.Log]; tests inject
// [Policy.Sleep] and [Policy.Rand] for determinism.
type Policy struct {
	// MaxAttempts caps the total tries INCLUDING the first. <=0 defaults to 3.
	MaxAttempts int

	// BaseDelay is the first backoff's ceiling; each retry doubles it (capped by
	// MaxDelay). <=0 defaults to 250ms.
	BaseDelay time.Duration

	// MaxDelay caps any single backoff. <=0 defaults to 1s.
	MaxDelay time.Duration

	// Sleep pauses for d or until ctx is done, returning ctx.Err() if the context
	// is cancelled/expired first. nil uses a real timer ([timerSleep]). Injected so
	// cassette tests never sleep wall-clock (ADR-0021).
	Sleep func(ctx context.Context, d time.Duration) error

	// Rand returns a jitter fraction in [0,1); the full-jitter backoff is
	// Rand()*min(MaxDelay, BaseDelay<<n) for the n-th (0-based) retry. nil uses
	// [math/rand.Float64]. Injected so a test gets a fixed backoff.
	Rand func() float64

	// Log receives one Debug line per retry (the attempt, the backoff, the error).
	// It is slog ONLY — per-attempt detail never becomes a metric series (ADR-0032).
	// nil is silent.
	Log *slog.Logger
}

// Do runs op with bounded, deadline-aware backoff and returns op's result, or
// the error that ended the loop. Stop conditions, in order per call:
//
//   - op returns nil error → success, return the result.
//   - the call ctx is cancelled/expired → return the ctx error (a barge-in reads
//     as a cancel, not a provider failure — ADR-0027). Checked before the op runs
//     and again after it returns, so a fired total budget is surfaced at once and
//     never retried (the #91 serial-worker wedge guard).
//   - the error is not [Retryable] → return it (fail fast on 4xx auth, prose).
//   - the attempts are exhausted → return the last error.
//   - the remaining deadline is <= the next backoff → give up rather than sleep
//     past the deadline (the budget is NEVER extended — ADR-0044).
//
// A Sleep that returns a ctx error (a barge landing mid-backoff) aborts the loop
// with that ctx error, not the provider error.
func Do[T any](ctx context.Context, p Policy, op func(ctx context.Context) (T, error)) (T, error) {
	maxAttempts := p.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	base := p.BaseDelay
	if base <= 0 {
		base = defaultBaseDelay
	}
	maxDelay := p.MaxDelay
	if maxDelay <= 0 {
		maxDelay = defaultMaxDelay
	}
	sleep := p.Sleep
	if sleep == nil {
		sleep = timerSleep
	}
	randFn := p.Rand
	if randFn == nil {
		randFn = rand.Float64
	}

	var zero T
	for attempt := 0; ; attempt++ {
		// A ctx already cancelled/expired burns no attempt.
		if err := ctx.Err(); err != nil {
			return zero, err
		}

		result, err := op(ctx)
		if err == nil {
			return result, nil
		}

		// A cancelled/expired ctx wins over the op's own error: the call ended
		// because the caller cut it (barge-in) or the total budget fired, not because
		// the vendor failed — and neither is retried.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return zero, ctxErr
		}

		if !Retryable(err) {
			return zero, err
		}
		if attempt+1 >= maxAttempts {
			return zero, err // budget exhausted; surface the last error
		}

		backoff := computeBackoff(base, maxDelay, attempt, randFn)

		// Never extend the deadline: if the remaining budget is <= the pause, give up
		// now rather than sleep past it (ADR-0044). The per-turn / STT-total deadline
		// stays the hard bound.
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= backoff {
			return zero, err
		}

		if p.Log != nil {
			p.Log.Debug("provider call failed, retrying",
				"attempt", attempt+1, "max_attempts", maxAttempts, "backoff", backoff, "err", err)
		}

		if serr := sleep(ctx, backoff); serr != nil {
			// The sleep was interrupted by the ctx (a barge landing mid-backoff):
			// return the ctx error, not the provider error.
			return zero, serr
		}
	}
}

// computeBackoff returns the full-jitter backoff for the n-th (0-based) retry:
// a uniform sample in [0, min(max, base<<n)). Full jitter (rather than fixed
// exponential) spreads concurrent retriers so they do not resynchronise into a
// thundering herd against a recovering provider.
func computeBackoff(base, max time.Duration, n int, randFn func() float64) time.Duration {
	ceiling := base << n
	// A large n overflows the shift to <=0; clamp to max either way.
	if ceiling <= 0 || ceiling > max {
		ceiling = max
	}
	return time.Duration(randFn() * float64(ceiling))
}

// timerSleep is the default [Policy.Sleep]: pause for d, or return the ctx error
// if the context is cancelled/expired first. A non-positive d returns
// immediately (respecting any already-done ctx).
func timerSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Retryable reports whether err is a transient provider failure worth retrying
// (ADR-0044). A [providererr.HTTPError] is retryable on 429 or any 5xx and not on
// other 4xx; a [net.Error] (dial/reset/reset) is retryable. Context errors and
// untyped prose errors are NOT retryable — under-retry is the safe default, since
// a retry that re-drives a non-transient failure only wastes the deadline.
//
// The context check is FIRST: a [context.DeadlineExceeded] satisfies [net.Error]
// (it reports Timeout), so classifying it before the net.Error branch keeps a
// fired deadline out of the retryable set.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var httpErr *providererr.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == 429 || httpErr.StatusCode >= 500
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}
