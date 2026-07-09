package orchestrator

import (
	"context"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// TargetMatcher is the pluggable matching algorithm behind an [AddressDetector].
// Given one utterance's transcript it returns the routing decisions to publish:
// the slice may be empty (nothing addressed), hold one entry (the usual case),
// or hold several when an utterance addresses multiple targets at once. The
// detector publishes each returned [voiceevent.AddressRouted] verbatim, so a
// matcher is responsible for the whole event — including its Text — not just the
// Target.
//
// The algorithm and the routing targets it scores against live entirely behind
// this seam: the orchestrator holds no matching logic of its own. The voice/address
// package ships two adapters — address.WholeWordMatcher (the dependency-free
// whole-word default) and address.Matcher (the scoring fuzzy/phonetic engine,
// ADR-0024) — and the algorithm choice (regex / LLM judge / two-stage / v1
// cherry-pick) is open per Q13.4 in DESIGN.md. Construction-time validation of
// the targets is the matcher's responsibility, not the detector's.
type TargetMatcher interface {
	TargetMatch(text string) []voiceevent.AddressRouted
}

// AddressDetector is a [Reactor] that subscribes to [voiceevent.STTFinal]
// events, asks its [TargetMatcher] which Agent(s) the utterance addresses, and
// republishes each choice as [voiceevent.AddressRouted] using the shared event
// taxonomy (ADR-0020).
//
// Per CONTEXT.md "Address Detection" the routing options are exactly the Agents
// present in the Voice Session: a Character NPC if a participant named one,
// otherwise the Tenant's Butler (the default route). Which of those a given
// utterance resolves to is wholly the matcher's call.
//
// Per ADR-0026 the detector is a Reactor: construction holds the matcher but
// touches no bus; [AddressDetector.Bind] installs the STTFinal subscription and
// returns its teardown. This lets the whole reactive layer be wired uniformly —
// standalone, in a hand-picked subset via [Bind], or bundled by a [Conversation].
type AddressDetector struct {
	matcher TargetMatcher
	// isGM reports whether a SpeakerID belongs to the operator allowlist — the
	// deterministic GM identity per ADR-0050 (allowlist membership, not a
	// per-session binding). Nil means the Butler GM-address gate is off: every
	// Butler route publishes as before, the byte-for-byte default for the
	// voice-standalone and bench paths and every pre-gate call site.
	isGM func(speakerID string) bool
}

// DetectorOption configures an [AddressDetector] at construction. Options are
// variadic and backward compatible: a detector built with no options behaves
// exactly as before the option seam existed.
type DetectorOption func(*AddressDetector)

// WithButlerGMGate enforces ADR-0024's Butler GM-only voice-address: a
// Butler-addressed utterance publishes only when its [voiceevent.STTFinal]
// SpeakerID is a GM per isGM (operator-allowlist membership, ADR-0050 /
// ADR-0041). Utterances from a non-allowlisted or empty SpeakerID are dropped
// (fail closed) — the route goes nowhere, the matcher is not re-invoked for a
// fallback, and Character NPC routing is untouched.
//
// A nil isGM leaves the gate off (the default). The Butler identity check is an
// orchestration concern kept out of the pure text matcher (ADR-0024): the
// matcher still scores only transcript text and never sees the SpeakerID.
func WithButlerGMGate(isGM func(speakerID string) bool) DetectorOption {
	return func(d *AddressDetector) { d.isGM = isGM }
}

// NewAddressDetector builds a detector around matcher, which must be non-nil
// (the detector has no matching algorithm of its own to fall back to). It
// installs nothing — call [AddressDetector.Bind] to subscribe it to a bus.
//
// The matcher owns the Voice Session's routing targets and their validation;
// construct it with the Tenant's Butler and the active Character NPCs (see
// address.NewWholeWordMatcher or address.NewMatcher) before handing it here.
// Pass [WithButlerGMGate] to enforce the Butler GM-only address gate; with no
// options the detector routes every matcher decision unconditionally.
func NewAddressDetector(matcher TargetMatcher, opts ...DetectorOption) *AddressDetector {
	if matcher == nil {
		panic("orchestrator.NewAddressDetector: matcher must not be nil")
	}
	d := &AddressDetector{matcher: matcher}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Bind subscribes the detector to [voiceevent.STTFinal] on bus and returns a
// function that removes the subscription. It implements [Reactor]; bus must be
// non-nil.
//
// Routing is a pure function of the transcript text, so the detector ignores
// ctx — it is accepted only to satisfy the Reactor contract (other reactors
// thread it into the STT/TTS calls they trigger).
func (d *AddressDetector) Bind(_ context.Context, bus *voiceevent.Bus) (cancel func()) {
	if bus == nil {
		panic("orchestrator.AddressDetector.Bind: bus must not be nil")
	}
	return voiceevent.On(bus, func(final voiceevent.STTFinal) {
		for _, routed := range d.matcher.TargetMatch(final.Text) {
			// Butler GM-only address gate (ADR-0024): drop a Butler route whose
			// SpeakerID is not an allowlisted GM (empty fails closed). Fail
			// closed means the utterance routes nowhere — the matcher is not
			// re-invoked for a fallback. Character routes are never gated.
			if d.isGM != nil && routed.Target.AgentRole == "butler" &&
				(final.SpeakerID == "" || !d.isGM(final.SpeakerID)) {
				continue
			}
			// Carry the turn correlation id (A3) from the utterance onto each
			// routing decision it produced; the matcher does not know about it.
			routed.TurnID = final.TurnID
			bus.Publish(routed)
		}
	})
}
