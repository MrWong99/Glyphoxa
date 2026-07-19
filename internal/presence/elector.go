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
	return &OwnerElector{store: store, instanceID: instanceID, onChange: onChange, cfg: cfg, log: log}
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

// reconcile runs one acquire/renew and signals a transition. A transient error
// leaves the last state untouched (no flip): a DB blip that fails our renew also
// fails any challenger's acquire, so relinquishing would only risk a needless gap.
func (e *OwnerElector) reconcile(ctx context.Context) {
	owner, err := e.store.AcquireOrRenewPresenceOwner(ctx, e.instanceID, e.cfg.Expiry)
	if err != nil {
		e.log.Warn("presence: owner election acquire/renew failed; keeping current ownership", "instance", e.instanceID, "owner", e.current, "err", err)
		return
	}
	e.set(owner)
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
