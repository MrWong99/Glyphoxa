package presence

import (
	"context"
	"log/slog"
	"time"
)

// OwnerStore is the presence-owner election persistence the OwnerElector drives
// (#492, ADR-0057 (c)); *storage.Store satisfies it. Split out so the elector unit
// tests script elections without a live Postgres.
type OwnerStore interface {
	// AcquireOrRenewPresenceOwner claims or renews the singleton owner row for
	// instanceID, reporting whether instanceID now owns it. It wins on an empty row,
	// a renew of its own claim, or an expired incumbent; it loses to a live other
	// owner.
	AcquireOrRenewPresenceOwner(ctx context.Context, instanceID string, expiry time.Duration) (bool, error)
	// ReleasePresenceOwner drops instanceID's claim (fenced by instance_id) so a
	// challenger wins immediately on a clean drain.
	ReleasePresenceOwner(ctx context.Context, instanceID string) error
}

// OwnerElectorConfig is the elector's cadence: how often it renews (Interval) and
// how long an owner's silence before its row is considered dead (Expiry). Read
// from GLYPHOXA_PRESENCE_OWNER_INTERVAL (5s) / _EXPIRY (15s) in cmd/glyphoxa.
type OwnerElectorConfig struct {
	Interval time.Duration
	Expiry   time.Duration
}

// releaseTimeout bounds the best-effort Release on shutdown: the parent ctx is
// already cancelled by then, so Release runs on a fresh short-lived ctx.
const electorReleaseTimeout = 3 * time.Second

// OwnerElector runs the presence-owner election loop for one Voice Instance (#492,
// ADR-0057 (c)): it renews the singleton presence_owner claim on a ticker and, on
// every ownership transition, calls onChange — wired to Registry.SetActive so only
// the elected owner acts on the interaction stream every session on a shared
// central token receives (P5). On ctx cancel (SIGTERM) it Releases the claim and
// signals loss, so a drained owner hands over immediately rather than forcing the
// fleet to wait out the expiry.
//
// No mid-session takeover concern here (that is the voice claim plane's, #491):
// this elects who DISPATCHES INTERACTIONS, orthogonal to who holds a voice call.
type OwnerElector struct {
	store      OwnerStore
	instanceID string
	onChange   func(owner bool)
	cfg        OwnerElectorConfig
	log        *slog.Logger

	// current is the last-signalled ownership; onChange fires only on a change from
	// it. Seeded false because a -mode voice worker boots inactive.
	current bool

	// lastRenew is the local (monotonic) instant of the last SUCCESSFUL acquire/renew.
	// Self-demotion is judged against elapsed since this — never the DB heartbeat_at
	// or a wall clock — so a partitioned owner that can no longer reach Postgres
	// deactivates its Registry before another node's lease-expiry claim could make two
	// owners dispatch the same interaction.
	lastRenew time.Time

	// now is the clock (default time.Now, which carries a monotonic reading). Injected
	// in tests to drive the demotion timer deterministically.
	now func() time.Time

	// ticks, when non-nil, replaces the internal time.Ticker so tests drive
	// elections deterministically. nil in production → a real ticker at cfg.Interval.
	ticks <-chan time.Time
}

// NewOwnerElector builds an elector over store for instanceID. onChange is called
// on each ownership transition (true on gain, false on loss/shutdown) — wire it to
// the presence Registry's SetActive. A nil log discards.
func NewOwnerElector(store OwnerStore, instanceID string, onChange func(owner bool), log *slog.Logger, cfg OwnerElectorConfig) *OwnerElector {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &OwnerElector{store: store, instanceID: instanceID, onChange: onChange, cfg: cfg, log: log, now: time.Now}
}

// Run drives the election until ctx is cancelled. It attempts an immediate claim at
// boot (so ownership is decided without waiting a full interval), then renews on
// each tick. On ctx cancel it Releases the claim and signals loss. Blocking; run it
// in its own goroutine.
func (e *OwnerElector) Run(ctx context.Context) {
	e.reconcile(ctx)

	ticks := e.ticks
	if ticks == nil {
		ticker := time.NewTicker(e.cfg.Interval)
		defer ticker.Stop()
		ticks = ticker.C
	}

	for {
		select {
		case <-ctx.Done():
			e.release()
			return
		case <-ticks:
			e.reconcile(ctx)
		}
	}
}

// reconcile runs one acquire/renew and signals a transition. The acquire is bounded
// by a per-op DB timeout (min(interval, 3s)) so a stuck connection can never block
// the loop past its own tick — otherwise the demotion check below would never run.
//
// On success it stamps lastRenew and signals ownership. On error it SELF-DEMOTES —
// SetActive(false) via set — once the monotonic elapsed since the last successful
// renew reaches demoteAfter (Expiry - Interval - opTimeout): a partitioned owner
// that can no longer reach Postgres must stop dispatching STRICTLY BEFORE another
// node's lease-expiry claim (the DB steal horizon, heartbeat_at + Expiry) promotes
// a second owner — else both dispatch the same interaction for several seconds
// (#483). The threshold is padded below Expiry because the demotion check itself
// runs only on ticks: after the elapsed crosses it, up to one more Interval passes
// before the next tick and that tick's failing acquire can burn its whole opTimeout
// before this check runs, so a bare Expiry threshold would demote as late as
// Expiry + Interval + opTimeout — well inside the challenger's ownership. It does
// NOT Release on demotion — the DB is unreachable by assumption, so a local
// deactivation is all that is possible and the row's own lease expiry hands
// ownership over. Re-promotion happens ONLY via the next SUCCESSFUL acquire in the
// normal loop (which re-stamps lastRenew and re-signals true). Before the elapsed
// reaches the threshold a transient blip keeps ownership: the same blip fails a
// challenger's acquire too, so relinquishing early would only risk a needless gap.
func (e *OwnerElector) reconcile(ctx context.Context) {
	// Stamp the renew instant BEFORE issuing the acquire (#506 re-review): the DB
	// heartbeat_at is written server-side DURING the call, so a lastRenew stamped
	// AFTER it returns would sit up to opTimeout LATER than the server stamp —
	// leaving a residual dual-owner window of that acquire leg (the demotion is
	// then judged against a too-recent local instant while the challenger's steal
	// horizon runs off the earlier server stamp). Stamping before makes lastRenew
	// never later than the server heartbeat_at, so the single-opTimeout demoteAfter
	// margin below holds strictly.
	started := e.now()
	opCtx, cancel := context.WithTimeout(ctx, e.opTimeout())
	owner, err := e.store.AcquireOrRenewPresenceOwner(opCtx, e.instanceID, e.cfg.Expiry)
	cancel()
	if err != nil {
		e.log.Warn("presence: owner election acquire/renew failed", "instance", e.instanceID, "owner", e.current, "err", err)
		if e.current && e.now().Sub(e.lastRenew) >= e.demoteAfter() {
			e.log.Warn("presence: owner lease unrenewed past the demotion threshold; self-demoting (no Release — DB unreachable)",
				"instance", e.instanceID, "threshold", e.demoteAfter(), "expiry", e.cfg.Expiry)
			e.set(false)
		}
		return
	}
	e.lastRenew = started
	e.set(owner)
}

// demoteAfter is the self-demotion threshold: Expiry - Interval - opTimeout, so
// the LOCAL deactivate deadline (threshold + one tick Interval + that tick's
// opTimeout, the worst-case detection lag) lands strictly before the DB steal
// horizon (Expiry) where a challenger can be promoted (#483). This SINGLE-opTimeout
// margin holds only because lastRenew is stamped BEFORE the acquire (never later
// than the server heartbeat_at, see reconcile, #506); a lastRenew stamped after the
// call would need a 2×opTimeout margin. A degenerate cadence
// (Interval + opTimeout >= Expiry) clamps to 0 — demote on the first failed renew,
// the only safe posture when the window leaves no slack; warnElectorCadence in
// cmd/glyphoxa flags such configs at boot.
func (e *OwnerElector) demoteAfter() time.Duration {
	d := e.cfg.Expiry - e.cfg.Interval - e.opTimeout()
	if d < 0 {
		return 0
	}
	return d
}

// opTimeout bounds a single acquire/renew DB call: min(Interval, 3s), so a stuck
// connection cannot pin the loop past its tick and starve the self-demotion check.
// Falls back to 3s when Interval is non-positive.
func (e *OwnerElector) opTimeout() time.Duration {
	const cap = 3 * time.Second
	if e.cfg.Interval > 0 && e.cfg.Interval < cap {
		return e.cfg.Interval
	}
	return cap
}

// set fires onChange only when ownership actually changes.
func (e *OwnerElector) set(owner bool) {
	if owner == e.current {
		return
	}
	e.current = owner
	if owner {
		e.log.Info("presence: won owner election", "instance", e.instanceID)
	} else {
		e.log.Info("presence: lost owner election", "instance", e.instanceID)
	}
	e.onChange(owner)
}

// release best-effort drops the claim on shutdown (parent ctx already cancelled, so
// a fresh short ctx) and signals loss so the Registry deactivates.
func (e *OwnerElector) release() {
	rctx, cancel := context.WithTimeout(context.Background(), electorReleaseTimeout)
	defer cancel()
	if err := e.store.ReleasePresenceOwner(rctx, e.instanceID); err != nil {
		e.log.Warn("presence: releasing owner claim on shutdown failed; it will expire", "instance", e.instanceID, "err", err)
	}
	e.set(false)
}
