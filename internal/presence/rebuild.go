package presence

import (
	"errors"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// Rebuild pacing for standing clients.
//
// Opening a gateway sends an IDENTIFY, and Discord allows a Bot token 1000 of
// them per 24 hours; past that it RESETS THE TOKEN, which takes every session
// on that token down until an operator pastes a new one. A standing client
// that dies is rebuilt by the next [Clients.EnsureTenant], and that runs on
// every reconcile tick (30s by default) and on every Voice Session start. So a
// gateway that dies again shortly after each open — or a token Discord has
// already refused — used to be re-opened twice a minute, forever: 1000
// IDENTIFYs in about eight hours. That is how the production Bot's token got
// reset on 2026-11-20.
//
// The pacing is per token, because the budget is: the delay doubles with each
// consecutive failure and only a client that then stays up for
// [rebuildHealthyAfter] clears the count. A persistent failure settles at one
// attempt per [rebuildMaxDelay], about 50 a day, an order of magnitude inside
// the budget with room left for the other clients on the token.

const (
	// rebuildBaseDelay is the wait after the first failure: one reconcile
	// interval, so a single blip costs no more than it always has.
	rebuildBaseDelay = 30 * time.Second
	// rebuildMaxDelay caps the doubling. It is also where a failure Discord
	// calls terminal (a refused token, disallowed intents) starts: retrying
	// that sooner cannot help, but retrying eventually lets a fix made on
	// Discord's side take effect without a restart.
	rebuildMaxDelay = 30 * time.Minute
	// rebuildHealthyAfter is how long a client must stay up before its token's
	// failure count is forgiven.
	rebuildHealthyAfter = 10 * time.Minute
)

// ErrRebuildBackoff is returned by [Clients.EnsureTenant] (and so by
// [Clients.ClientForTenant]) while a token's standing client is dead and its
// next rebuild attempt is not due yet.
var ErrRebuildBackoff = errors.New("presence: standing client rebuild is backing off")

// rebuildState is one token's pacing record. Guarded by Clients.mu.
type rebuildState struct {
	failures  int       // consecutive failures (failed opens and deaths)
	notBefore time.Time // earliest next open
	openedAt  time.Time // when the current client came up; zero while dead
}

// rebuildWait is how long token's next gateway open must still wait; zero
// means it may go ahead.
func (c *Clients) rebuildWait(token string) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.rebuilds[token]
	if st == nil {
		return 0
	}
	return max(st.notBefore.Sub(c.now()), 0)
}

// noteOpened records that token's client came up. The failure count is kept:
// only surviving [rebuildHealthyAfter] clears it (see noteDiedLocked), so a
// gateway that opens fine and dies a minute later keeps backing off.
func (c *Clients) noteOpened(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if st := c.rebuilds[token]; st != nil {
		st.openedAt = c.now()
	}
}

// noteOpenFailed records a build or gateway open that failed for token.
func (c *Clients) noteOpenFailed(token string, cause error) {
	c.mu.Lock()
	wait := c.noteFailureLocked(token, cause)
	c.mu.Unlock()
	c.log.Warn("presence: standing client open failed; pacing the next attempt", "err", cause, "next_attempt_in", wait.Round(time.Second))
}

// noteDiedLocked records that token's standing gateway died and returns the
// wait before its next rebuild. Caller holds mu.
func (c *Clients) noteDiedLocked(token string, cause error) time.Duration {
	if st := c.rebuilds[token]; st != nil && !st.openedAt.IsZero() {
		if c.now().Sub(st.openedAt) >= rebuildHealthyAfter {
			st.failures = 0 // it served; this death starts a fresh count
		}
		st.openedAt = time.Time{}
	}
	return c.noteFailureLocked(token, cause)
}

// noteFailureLocked counts one failure for token and schedules its next
// attempt. Caller holds mu.
func (c *Clients) noteFailureLocked(token string, cause error) time.Duration {
	st := c.rebuilds[token]
	if st == nil {
		st = &rebuildState{}
		c.rebuilds[token] = st
	}
	st.failures++
	wait := rebuildDelay(st.failures)
	if wirenpc.ClassifyFatal(cause) != nil {
		wait = rebuildMaxDelay
	}
	st.notBefore = c.now().Add(wait)
	return wait
}

// rebuildDelay is the wait after the n-th consecutive failure:
// [rebuildBaseDelay] doubling up to [rebuildMaxDelay].
func rebuildDelay(failures int) time.Duration {
	d := rebuildBaseDelay
	for i := 1; i < failures && d < rebuildMaxDelay; i++ {
		d *= 2
	}
	return min(d, rebuildMaxDelay)
}
