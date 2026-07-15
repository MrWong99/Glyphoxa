package wirenpc

import (
	"context"
	"log/slog"
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
//
// It returns the unsubscribe func for the caller to defer. A nil tape (campaign not
// armed) does nothing.
func wireTapeConsent(ctx context.Context, bus *voiceevent.Bus, t *tape.Tape, campaignID uuid.UUID, store TapeConsentReader, log *slog.Logger) func() {
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
	reconcile := func() {
		consented, err := store.ListTapeConsent(ctx, campaignID)
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
	return unsub
}
