package orchestrator

import (
	"regexp"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// AddressDetector subscribes to [voiceevent.STTFinal] events on the bus,
// decides which Agent the utterance addresses, and republishes the choice
// as [voiceevent.AddressRouted] using the shared event taxonomy (ADR-0020).
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
type AddressDetector struct {
	bus         *voiceevent.Bus
	butler      voiceevent.AddressTarget
	npcs        []voiceevent.AddressTarget
	npcMatchers []*regexp.Regexp // parallel to npcs; one whole-word matcher per NPC name
	unsubscribe func()
}

// NewAddressDetector wires the detector to bus and subscribes it to
// [voiceevent.STTFinal] events for the lifetime of the returned value.
// The caller must invoke Close to release the subscription (typically via
// t.Cleanup in tests).
//
// butler must carry AgentRole "butler"; npcs is the list of Character NPC
// Agents currently active in the Voice Session, each carrying AgentRole
// "character" and a non-empty display Name. Passing a nil bus, an empty
// Butler Name, or any NPC without a Name panics — these are construction
// errors caught at wiring time, not runtime conditions.
func NewAddressDetector(bus *voiceevent.Bus, butler voiceevent.AddressTarget, npcs []voiceevent.AddressTarget) *AddressDetector {
	if bus == nil {
		panic("orchestrator.NewAddressDetector: bus must not be nil")
	}
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

	d := &AddressDetector{
		bus:         bus,
		butler:      butler,
		npcs:        npcs,
		npcMatchers: matchers,
	}
	d.unsubscribe = bus.Subscribe(func(e voiceevent.Event) {
		final, ok := e.(voiceevent.STTFinal)
		if !ok {
			return
		}
		bus.Publish(voiceevent.AddressRouted{
			At:     time.Now(),
			Text:   final.Text,
			Target: d.routeFor(final.Text),
		})
	})
	return d
}

// Close releases the bus subscription. Calling more than once is a no-op.
func (d *AddressDetector) Close() {
	if d.unsubscribe != nil {
		d.unsubscribe()
		d.unsubscribe = nil
	}
}

func (d *AddressDetector) routeFor(text string) voiceevent.AddressTarget {
	for i, m := range d.npcMatchers {
		if m.MatchString(text) {
			return d.npcs[i]
		}
	}
	return d.butler
}
