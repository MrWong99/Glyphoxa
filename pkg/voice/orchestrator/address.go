package orchestrator

import (
	"context"
	"regexp"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// AddressDetector is a [Reactor] that subscribes to [voiceevent.STTFinal]
// events, decides which Agent the utterance addresses, and republishes the
// choice as [voiceevent.AddressRouted] using the shared event taxonomy
// (ADR-0020).
//
// Per CONTEXT.md "Address Detection" the routing options are exactly the
// Agents present in the Voice Session: a Character NPC if a participant
// named one, otherwise the Tenant's Butler (the default route).
//
// The matcher used here is intentionally minimal — case-insensitive
// whole-word match on each registered Character NPC's display Name, with
// the Butler as the unconditional fallback. The full algorithm choice
// (regex / LLM judge / two-stage / v1 cherry-pick) is open per Q13.4 in
// DESIGN.md; this stub gives downstream stages a routing event to consume
// without committing to an algorithm. The first NPC matched wins —
// disambiguation across multiply-named utterances is also Q13.4.
//
// Per ADR-0026 the detector is a Reactor: construction validates the targets
// and compiles the matchers but touches no bus; [AddressDetector.Bind] installs
// the STTFinal subscription and returns its teardown. This lets the whole
// reactive layer be wired uniformly — standalone, in a hand-picked subset via
// [Bind], or bundled by a [Conversation].
type AddressDetector struct {
	butler      voiceevent.AddressTarget
	npcs        []voiceevent.AddressTarget
	npcMatchers []*regexp.Regexp // parallel to npcs; one whole-word matcher per NPC name
}

// NewAddressDetector validates the routing targets and compiles one whole-word
// matcher per NPC name. It installs nothing — call [AddressDetector.Bind] to
// subscribe it to a bus.
//
// butler must carry AgentRole "butler"; npcs is the list of Character NPC
// Agents currently active in the Voice Session, each carrying AgentRole
// "character" and a non-empty display Name. An empty Butler Name, or any NPC
// without a Name or with the wrong role, panics — these are construction errors
// caught at wiring time, not runtime conditions.
func NewAddressDetector(butler voiceevent.AddressTarget, npcs []voiceevent.AddressTarget) *AddressDetector {
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

	return &AddressDetector{
		butler:      butler,
		npcs:        npcs,
		npcMatchers: matchers,
	}
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
		bus.Publish(voiceevent.AddressRouted{
			At:     time.Now(),
			Text:   final.Text,
			Target: d.routeFor(final.Text),
		})
	})
}

func (d *AddressDetector) routeFor(text string) voiceevent.AddressTarget {
	for i, m := range d.npcMatchers {
		if m.MatchString(text) {
			return d.npcs[i]
		}
	}
	return d.butler
}
