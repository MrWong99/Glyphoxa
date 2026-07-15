package wirenpc

import (
	"context"
	"log/slog"
	"time"
)

// reconnectPolicy bounds how the live voice loop backs off between failed or
// dropped Discord connections (issue #44). A serving voice pod must not
// crashloop because Discord is briefly unreachable: Run keeps serving /healthz
// and /readyz (DB-backed) and retries on this schedule instead of exiting.
// Capped exponential, no jitter — one pod reconnecting to one gateway has no
// thundering herd to spread.
type reconnectPolicy struct {
	initial time.Duration
	max     time.Duration
	factor  float64
	// healthyAfter is how long a cycle must serve after connected() fires before
	// it counts as a healthy session and resets the backoff to initial. A cycle
	// that joins but fails sooner (codec-less build, broken ONNX init — issue
	// #141) is a connect failure: the delay keeps growing to its cap instead of
	// retrying the Discord voice join at 1 Hz forever. Zero means reset on join.
	healthyAfter time.Duration
	// sleep blocks for d or until ctx is cancelled (returns ctx.Err() if
	// cancelled first). Injected so tests drive the backoff without real waits.
	sleep func(ctx context.Context, d time.Duration) error
	// now reports the current time for the healthyAfter measurement. Injected so
	// tests fake a long-serving session in milliseconds; nil means time.Now.
	now func() time.Time
}

// healthySessionDuration is how long a session must serve post-join before the
// reconnect backoff forgives past failures and resets to the initial delay.
// Sized to the backoff cap: a session must outlive the maximum backoff before
// the loop trusts it, so a persistent join-then-fail cycle (issue #141) settles
// at cap cadence instead of resetting to 1 Hz.
const healthySessionDuration = 30 * time.Second

func defaultReconnectPolicy() reconnectPolicy {
	return reconnectPolicy{
		initial:      time.Second,
		max:          30 * time.Second,
		factor:       2,
		healthyAfter: healthySessionDuration,
		sleep:        sleepCtx,
		now:          time.Now,
	}
}

// sleepCtx blocks for d or until ctx is cancelled, returning ctx.Err() on
// cancel. A timer (not time.Sleep) so a cancelled ctx returns immediately.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func nextDelay(d time.Duration, p reconnectPolicy) time.Duration {
	next := time.Duration(float64(d) * p.factor)
	if next > p.max {
		return p.max
	}
	return next
}

// runWithReconnect calls attempt repeatedly, keeping the process alive across
// failed or dropped Discord connections. It returns nil (clean shutdown) ONLY
// when ctx is cancelled; every other return from attempt — an error OR a clean
// session-close (nil) — is a lost connection and triggers a backed-off
// reconnect. attempt is handed a connected callback it calls once the join
// succeeds; the backoff resets to initial only if the cycle then serves for at
// least p.healthyAfter (issue #141), so a long-lived session that later drops
// reconnects promptly while a join-then-immediate-fail cycle keeps growing its
// delay instead of hammering the Discord voice join at 1 Hz.
func runWithReconnect(ctx context.Context, log *slog.Logger, p reconnectPolicy, attempt func(ctx context.Context, connected func()) error) error {
	now := p.now
	if now == nil {
		now = time.Now
	}
	delay := p.initial
	for {
		// connectedAt is written by the callback inside attempt and read after
		// attempt returns — connectAndServe invokes it synchronously, same
		// goroutine, so no lock. The delay is only consumed post-return, so
		// deciding the reset here is equivalent to arming a timer on connect.
		var connectedAt time.Time
		err := attempt(ctx, func() { connectedAt = now() })
		if ctx.Err() != nil {
			return nil // shutdown requested — stop retrying, exit clean (fixes SIGTERM->exit1)
		}
		// A fatal, non-retryable gateway rejection (bad Bot token, disallowed
		// intents, gateway reject) can never succeed on retry, so stop at once and
		// surface the classification instead of backing off forever (#123). The
		// session Manager reads this to record the persisted status as failed. A
		// transient failure (or a clean nil return) falls through to the backoff.
		if fe := classifyFatal(err); fe != nil {
			log.Error("voice connection failed fatally; not retrying", "err", fe, "reason", fe.Reason)
			return fe
		}
		if !connectedAt.IsZero() && now().Sub(connectedAt) >= p.healthyAfter {
			delay = p.initial // served healthily — forgive past failures (issue #44)
		}
		if err != nil {
			log.Warn("voice connection failed; reconnecting", "err", err, "backoff", delay)
		} else {
			log.Info("voice session ended; reconnecting", "backoff", delay)
		}
		if serr := p.sleep(ctx, delay); serr != nil {
			return nil // ctx cancelled during backoff — clean shutdown
		}
		delay = nextDelay(delay, p)
	}
}
