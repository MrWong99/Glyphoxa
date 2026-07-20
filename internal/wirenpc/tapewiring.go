package wirenpc

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/tape"
	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
)

// tapeInboundOptions returns the [wire.Pipeline] options that copy every consented
// inbound Speaker frame into the rollover tape (#306). A nil tape (campaign not
// armed) returns nothing, so the pipeline is byte-identical to the pre-tape loop.
// The tap runs inline on the audio loop: tape.AppendInbound is non-blocking and
// drops unconsented Speakers before any buffer (ADR-0051), so it adds no latency.
func tapeInboundOptions(t *tape.Tape) []wire.Option {
	if t == nil {
		return nil
	}
	return []wire.Option{
		wire.WithInboundTap(func(f gxvoice.Frame) {
			t.AppendInbound(f.UserID.String(), f.Opus, time.Now())
		}),
	}
}

// tapePumpOptions returns the [wire.PlaybackPump] options that copy every agent
// Opus frame pulled to the wire into the rollover tape's always-on agent lane
// (#306, ADR-0051). A nil tape returns nothing (unchanged playback).
func tapePumpOptions(t *tape.Tape) []wire.PumpOption {
	if t == nil {
		return nil
	}
	return []wire.PumpOption{
		wire.WithOutboundOpusTap(func(opus []byte) {
			t.AppendAgent(opus, time.Now())
		}),
	}
}

// TapeConsentReader is the authoritative consent read the tape reseeds from (#306):
// the durable tape_consent rows for a Campaign. *storage.Store satisfies it.
type TapeConsentReader interface {
	ListTapeConsent(ctx context.Context, campaignID uuid.UUID) ([]string, error)
}

// defaultTapeConsentReconcileInterval is the poller cadence when
// GLYPHOXA_TAPE_CONSENT_RECONCILE_INTERVAL is unset or non-positive (#492).
const defaultTapeConsentReconcileInterval = 5 * time.Second

// tapeConsentOpTimeout caps one reconcile read's DB time: min(interval, this),
// mirroring the presence elector's per-op bound (#483) so a stuck connection
// cannot pin the consent poller for the life of the cycle.
const tapeConsentOpTimeout = 3 * time.Second

// tapeConsentReconcileInterval reads the cross-pod consent poller cadence from
// GLYPHOXA_TAPE_CONSENT_RECONCILE_INTERVAL (#492), falling back to 5s on a blank,
// unparsable, or non-positive value. Parsed in the composition root (RunFromDB) so
// the cadence stays a deployment knob.
func tapeConsentReconcileInterval(getenv func(string) string) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(getenv("GLYPHOXA_TAPE_CONSENT_RECONCILE_INTERVAL")))
	if err != nil || d <= 0 {
		return defaultTapeConsentReconcileInterval
	}
	return d
}

// wireTapeConsent keeps the tape's consent set converged to the DURABLE truth
// (#306, ADR-0051), the exact discipline the mute wiring uses (wireMutes): it
// re-reads the authoritative consent rows rather than trusting an event payload, so
// two out-of-order events can't leave the tape granted while the DB says revoked.
//
//   - It reseeds from ListTapeConsent at cycle start, so a grant/revoke that landed
//     while nothing was subscribed (a reconnect backoff gap — the subscription is
//     per cycle but the tape is per session) is applied on the next cycle.
//   - On every TapeConsentChanged it filters e.CampaignID to THIS session's campaign
//     — a press against a stale disclosure for another campaign (a reused channel)
//     must not arm a lane here — then re-reads the store and reconciles the whole set.
//     This is the same-pod fast path: instant when the consent handler and the tape
//     share a process bus.
//   - It runs a poller goroutine that reconciles every `interval` until ctx (the
//     cycle ctx) is done (#492). In the fleet the consent button is dispatched by the
//     elected presence OWNER, which publishes TapeConsentChanged on ITS OWN bus — but
//     the tape may run on a DIFFERENT pod (a claim-plane worker) whose bus never sees
//     that event, so the bus fast path alone would strand the change cross-pod. The
//     poller bounds cross-pod staleness to one interval; ADR-0051 holds because
//     ReconcileConsent authoritatively clears a revoked Speaker's ring.
//
// It returns the unsubscribe func for the caller to defer; the poller stops on ctx
// done (the cycle ctx), so it dies with the cycle. A nil tape (campaign not armed)
// does nothing.
func wireTapeConsent(ctx context.Context, bus *voiceevent.Bus, t *tape.Tape, campaignID uuid.UUID, store TapeConsentReader, interval time.Duration, log *slog.Logger) func() {
	if t == nil {
		return func() {}
	}
	if store == nil {
		// A wiring bug: the tape is armed but no consent reader was threaded through
		// (RunFromDB sets both together). Refuse to reconcile against a nil store —
		// which would panic — and log loudly; the tape keeps its construction-time
		// seed rather than crashing the session.
		log.Error("tape: armed but no consent reader wired; live consent changes will not apply", "campaign", campaignID)
		return func() {}
	}
	if interval <= 0 {
		interval = defaultTapeConsentReconcileInterval
	}
	// Per-op DB bound (#483, the elector's opTimeout discipline): each reconcile
	// read runs under min(interval, 3s) so a hung connection cannot pin the poller
	// past its tick — the raw cycle ctx has no deadline of its own.
	opTimeout := interval
	if opTimeout > tapeConsentOpTimeout {
		opTimeout = tapeConsentOpTimeout
	}
	reconcile := func() {
		octx, cancel := context.WithTimeout(ctx, opTimeout)
		consented, err := store.ListTapeConsent(octx, campaignID)
		cancel()
		if err != nil {
			log.Warn("tape: reconcile consent from store", "campaign", campaignID, "err", err)
			return
		}
		t.ReconcileConsent(consented)
	}
	// Subscribe BEFORE the seed reconcile so an event landing in the seed window is
	// not lost (the wireMutes precedent): worst case is one extra idempotent
	// reconcile, never a dropped consent change.
	unsub := voiceevent.On(bus, func(e voiceevent.TapeConsentChanged) {
		if e.CampaignID != campaignID.String() {
			return // a consent press for a different campaign — not ours
		}
		reconcile() // re-read the durable truth; ignore the (possibly stale/reordered) payload
	})
	reconcile() // authoritative reseed at cycle start (catches changes during a reconnect gap)
	// Cross-pod poller (#492): reconcile every interval until the cycle ctx is done.
	// A reconcile error is logged inside reconcile and the ticker keeps going.
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reconcile()
			}
		}
	}()
	return unsub
}
