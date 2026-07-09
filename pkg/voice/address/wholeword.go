package address

import (
	"regexp"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// WholeWordMatcher is the minimal Address Detection algorithm: a
// case-insensitive whole-word match on each Character NPC's display Name with
// the Butler as the unconditional fallback. It is the dependency-free
// tracer-bullet alternative to the scoring [Matcher] and satisfies the
// orchestrator's TargetMatcher seam:
//
//	m := address.NewWholeWordMatcher(butler, npcs)
//	d := orchestrator.NewAddressDetector(m)
//
// The first NPC matched wins; disambiguation across multiply-named utterances
// is Q13.4 in DESIGN.md. Unlike [Matcher] it does no fuzzy or phonetic
// matching, so a mishearing ("bard" for "Bart") finds no NPC and falls through
// to the Butler. It always returns exactly one decision.
type WholeWordMatcher struct {
	butler      voiceevent.AddressTarget
	npcs        []voiceevent.AddressTarget
	npcMatchers []*regexp.Regexp // parallel to npcs; one whole-word matcher per NPC name
}

// NewWholeWordMatcher compiles one whole-word matcher per NPC name. butler must
// carry AgentRole "butler" with a non-empty Name and every npc must carry
// AgentRole "character" with a non-empty Name; a violation panics. These are
// wiring errors caught at construction, not runtime conditions.
func NewWholeWordMatcher(butler voiceevent.AddressTarget, npcs []voiceevent.AddressTarget) *WholeWordMatcher {
	if butler.AgentRole != voiceevent.AgentRoleButler {
		panic(`address.NewWholeWordMatcher: butler.AgentRole must be "butler"`)
	}
	if butler.Name == "" {
		panic("address.NewWholeWordMatcher: butler.Name must not be empty")
	}
	matchers := make([]*regexp.Regexp, len(npcs))
	for i, npc := range npcs {
		if npc.AgentRole != voiceevent.AgentRoleCharacter {
			panic(`address.NewWholeWordMatcher: npc.AgentRole must be "character"`)
		}
		if npc.Name == "" {
			panic("address.NewWholeWordMatcher: npc.Name must not be empty")
		}
		matchers[i] = regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(npc.Name) + `\b`)
	}
	return &WholeWordMatcher{
		butler:      butler,
		npcs:        append([]voiceevent.AddressTarget(nil), npcs...),
		npcMatchers: matchers,
	}
}

// TargetMatch routes to the first NPC whose name appears as a whole word in
// text, falling back to the Butler. It implements the orchestrator's
// TargetMatcher and always returns exactly one decision.
func (m *WholeWordMatcher) TargetMatch(text string) []voiceevent.AddressRouted {
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
