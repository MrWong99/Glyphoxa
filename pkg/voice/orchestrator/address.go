package orchestrator

import (
	"context"
	"regexp"
	"time"

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
// The algorithm choice (regex / LLM judge / two-stage / v1 cherry-pick) is open
// per Q13.4 in DESIGN.md; this seam lets the choice be swapped via [WithMatcher]
// without touching the bus wiring. The default matcher (used when no [WithMatcher]
// is given) is the minimal whole-word name match described on [NewAddressDetector].
type TargetMatcher interface {
	TargetMatch(text string) []voiceevent.AddressRouted
}

// TargetIniter is an optional convenience interface a [TargetMatcher] may
// implement to receive the Voice Session's routing targets at construction. If
// the matcher passed to [NewAddressDetector] (or the default matcher) satisfies
// it, TargetInit is called once with the Tenant's Butler and the active Character
// NPCs before the detector is returned — giving the matcher a chance to validate
// the targets and precompute whatever it needs. Matchers that derive their
// targets some other way can simply not implement it.
type TargetIniter interface {
	TargetInit(butler voiceevent.AddressTarget, npcs []voiceevent.AddressTarget)
}

// AddressDetector is a [Reactor] that subscribes to [voiceevent.STTFinal]
// events, decides which Agent(s) the utterance addresses, and republishes the
// choice as [voiceevent.AddressRouted] using the shared event taxonomy
// (ADR-0020).
//
// Per CONTEXT.md "Address Detection" the routing options are exactly the
// Agents present in the Voice Session: a Character NPC if a participant
// named one, otherwise the Tenant's Butler (the default route).
//
// The matching algorithm lives behind a [TargetMatcher]. The default — used
// when no [WithMatcher] option is given — is intentionally minimal:
// case-insensitive whole-word match on each registered Character NPC's display
// Name, the first NPC matched wins, and the Butler is the unconditional
// fallback. Callers needing a different algorithm supply one via [WithMatcher].
//
// Per ADR-0026 the detector is a Reactor: construction sets up the matcher but
// touches no bus; [AddressDetector.Bind] installs the STTFinal subscription and
// returns its teardown. This lets the whole reactive layer be wired uniformly —
// standalone, in a hand-picked subset via [Bind], or bundled by a [Conversation].
type AddressDetector struct {
	matcher TargetMatcher
}

// DetectorOption configures an [AddressDetector] at construction.
type DetectorOption func(*AddressDetector)

// WithMatcher replaces the default whole-word name matcher with a custom
// matching algorithm. If the supplied matcher implements [TargetIniter] it is
// still handed the Butler and NPC targets at construction.
func WithMatcher(m TargetMatcher) DetectorOption {
	return func(d *AddressDetector) { d.matcher = m }
}

// NewAddressDetector builds a detector for the given routing targets. It
// installs nothing — call [AddressDetector.Bind] to subscribe it to a bus.
//
// butler is the Tenant's Butler (the default route) and npcs is the list of
// Character NPC Agents currently active in the Voice Session. With no options
// the detector uses the default whole-word name matcher, which requires the
// Butler to carry AgentRole "butler" with a non-empty Name and every NPC to
// carry AgentRole "character" with a non-empty Name — an empty Butler Name, or
// any NPC without a Name or with the wrong role, panics at wiring time.
//
// Supplying [WithMatcher] swaps in a different algorithm; the targets are then
// only forwarded to that matcher when it implements [TargetIniter], so its own
// validation rules apply instead of the default matcher's.
func NewAddressDetector(butler voiceevent.AddressTarget, npcs []voiceevent.AddressTarget, opts ...DetectorOption) *AddressDetector {
	d := &AddressDetector{}
	for _, opt := range opts {
		opt(d)
	}
	if d.matcher == nil {
		d.matcher = &nameMatcher{}
	}
	if initer, ok := d.matcher.(TargetIniter); ok {
		initer.TargetInit(butler, npcs)
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
			bus.Publish(routed)
		}
	})
}

// nameMatcher is the default [TargetMatcher]: case-insensitive whole-word match
// on each NPC's display Name with the Butler as the unconditional fallback. The
// first NPC matched wins — disambiguation across multiply-named utterances is
// Q13.4 in DESIGN.md.
type nameMatcher struct {
	butler      voiceevent.AddressTarget
	npcs        []voiceevent.AddressTarget
	npcMatchers []*regexp.Regexp // parallel to npcs; one whole-word matcher per NPC name
}

// TargetInit validates the routing targets and compiles one whole-word matcher
// per NPC name. The panics are construction errors caught at wiring time, not
// runtime conditions.
func (m *nameMatcher) TargetInit(butler voiceevent.AddressTarget, npcs []voiceevent.AddressTarget) {
	if butler.AgentRole != "butler" {
		panic(`orchestrator.NewAddressDetector: butler.AgentRole must be "butler"`)
	}
	if butler.Name == "" {
		panic("orchestrator.NewAddressDetector: butler.Name must not be empty")
	}
	matchers := make([]*regexp.Regexp, len(npcs))
	for i, npc := range npcs {
		if npc.AgentRole != "character" {
			panic(`orchestrator.NewAddressDetector: npc.AgentRole must be "character"`)
		}
		if npc.Name == "" {
			panic("orchestrator.NewAddressDetector: npc.Name must not be empty")
		}
		matchers[i] = regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(npc.Name) + `\b`)
	}
	m.butler = butler
	m.npcs = npcs
	m.npcMatchers = matchers
}

// TargetMatch routes to the first NPC whose name appears as a whole word in
// text, falling back to the Butler. It always returns exactly one decision.
func (m *nameMatcher) TargetMatch(text string) []voiceevent.AddressRouted {
	target := m.butler
	for i, re := range m.npcMatchers {
		if re.MatchString(text) {
			target = m.npcs[i]
			break
		}
	}
	return []voiceevent.AddressRouted{{
		At:     time.Now(),
		Text:   text,
		Target: target,
	}}
}
