package orchestrator

import (
	"context"
	"log/slog"
	"strings"

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

// SpeakerAwareMatcher is the optional SpeakerID-aware extension of
// [TargetMatcher] (#256): a matcher that implements it also routes an utterance
// with the speaker's identity, so an identity-gated candidate (the Butler's
// GM-only voice address, ADR-0024) is filtered inside the matcher — pre-cap,
// pre-lastAddressed — rather than dropped after the fact by the detector. The
// scoring [address.Matcher] satisfies it. When a matcher implements it,
// [AddressDetector.Bind] prefers TargetMatchFrom and threads the
// [voiceevent.STTFinal] SpeakerID; a plain [TargetMatcher] keeps the text-only
// path with the detector-level [WithButlerGMGate] drop.
type SpeakerAwareMatcher interface {
	TargetMatcher
	TargetMatchFrom(speakerID, text string) []voiceevent.AddressRouted
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
//
// Belt-and-braces since #299: with the Butler on the live roster, the primary
// GM-gate moved matcher-side as a pre-cap eligibility drop (ADR-0024 amendment,
// [SpeakerAwareMatcher] / address.Matcher.TargetMatchFrom) so an excluded Butler
// never consumes a MaxTargets slot nor becomes lastAddressed. This detector-level
// drop is retained as a redundant safety net for any [TargetMatcher] that is NOT
// SpeakerID-aware (e.g. the whole-word matcher, cassette fakes).
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
	// Prefer the SpeakerID-aware routing call when the matcher supports it (#256):
	// this lets the matcher apply the Butler GM-gate as a pre-cap eligibility drop
	// (so an excluded Butler never consumes a slot or lastAddressed). A plain
	// matcher keeps the text-only TargetMatch path. The detector-level isGM drop
	// below is retained belt-and-braces either way.
	sam, speakerAware := d.matcher.(SpeakerAwareMatcher)
	return voiceevent.On(bus, func(final voiceevent.STTFinal) {
		// An empty or whitespace-only final routes NOWHERE (#434). The STT stage
		// publishes empties by pinned contract — "downstream consumers, not this
		// stage, decide" — and this detector is that consumer: the recognizer
		// authoritatively heard nothing, so there is no utterance to address. Left
		// unfiltered, the ambient heuristics (sole-NPC, last-speaker) would route
		// the nothing and the Agent would speak unprompted off a noise burst —
		// polluting its history with an empty user message and spending real
		// LLM+TTS money. The empty final stays observable on the bus for
		// metrics/diagnostics (and the Transcript keeps its own semantics); only
		// address routing drops it. Logged so live sessions show how often noise
		// crosses VAD onset yet transcribes to nothing.
		if strings.TrimSpace(final.Text) == "" {
			slog.Default().Info("orchestrator: empty STT final, routing nowhere",
				slog.String("speaker", final.SpeakerID),
				slog.String("turn", final.TurnID),
			)
			return
		}
		// Route via the SpeakerID-aware call when the matcher supports it (#256), so
		// the matcher-side Butler GM-gate drops an ineligible Butler PRE-CAP — before
		// the ensemble set below is built — then collect the survivors and decide the
		// set's atomicity (ADR-0025, #301): one survivor publishes a plain
		// [voiceevent.AddressRouted] (byte-identical to the pre-ensemble path, the
		// MaxTargets=1 default); two or more publish ONE [voiceevent.EnsembleRouted] so
		// the turn-taking layer runs the set as a single floor-holding Ensemble Turn;
		// zero publishes nothing.
		var routes []voiceevent.AddressRouted
		if speakerAware {
			routes = sam.TargetMatchFrom(final.SpeakerID, final.Text)
		} else {
			routes = d.matcher.TargetMatch(final.Text)
		}
		var survivors []voiceevent.AddressRouted
		for _, routed := range routes {
			// Butler GM-only address gate (ADR-0024): drop a Butler route whose
			// SpeakerID is not an allowlisted GM (empty fails closed). Fail
			// closed means the utterance routes nowhere — the matcher is not
			// re-invoked for a fallback. Character routes are never gated.
			if d.isGM != nil && routed.Target.AgentRole == voiceevent.AgentRoleButler &&
				(final.SpeakerID == "" || !d.isGM(final.SpeakerID)) {
				continue
			}
			// Carry the turn correlation id (A3) from the utterance onto each
			// routing decision it produced; the matcher does not know about it.
			routed.TurnID = final.TurnID
			survivors = append(survivors, routed)
		}
		switch len(survivors) {
		case 0:
			return
		case 1:
			// Single-target: byte-identical to the pre-ensemble path.
			bus.Publish(survivors[0])
		default:
			// Two or more: ONE atomic EnsembleRouted carrying the matcher's
			// score-sorted target set (Targets[0] is the top-scored coalesce anchor).
			targets := make([]voiceevent.AddressTarget, len(survivors))
			for i, s := range survivors {
				targets[i] = s.Target
			}
			bus.Publish(voiceevent.EnsembleRouted{
				At:      survivors[0].At,
				Text:    final.Text,
				TurnID:  final.TurnID,
				Targets: targets,
			})
		}
	})
}
