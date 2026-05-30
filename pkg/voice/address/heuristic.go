package address

import "time"

// DecisionContext is the read-only snapshot a [Heuristic] scores against for
// one utterance. The [Matcher] assembles it per call from the current
// transcript and its own conversational state; heuristics never mutate it and
// never reach back into the matcher, which keeps each heuristic a pure function
// of (Agent, DecisionContext) and therefore trivially testable.
type DecisionContext struct {
	// Now is the decision time, taken from the matcher's clock.
	Now time.Time
	// Utterance is the raw transcript text being routed.
	Utterance string
	// Words is the tokenized, lowercased utterance (see tokenize).
	Words []string

	// window bounds how far back "recent" reaches for both words and
	// interruptions.
	window time.Duration

	nameScores     map[int]float64      // agent index → fuzzy name similarity
	anyNameMatched bool                 // did any agent clear the name threshold this turn
	lastAddressed  map[string]bool      // agentID → addressed on the previous turn
	interruptions  map[string]time.Time // agentID → most recent interruption
	recentWords    map[string]struct{}  // distinct words within the window, incl. current
	nonAddressable int                  // count of active, non-AddressOnly agents
}

// AnyNameMatched reports whether some Agent was explicitly named this turn (its
// fuzzy similarity cleared the [NameMatch] threshold). The ambient heuristics —
// continuation, interruption, expertise, single-NPC fallback — return zero when
// this is true, so an explicit name short-circuits them exactly as ADR-0024's
// ordered chain does: name match wins outright and the fallbacks never widen
// the addressed set behind it.
func (dc *DecisionContext) AnyNameMatched() bool { return dc.anyNameMatched }

// NameScore returns the fuzzy name-match similarity in [0,1] computed for the
// agent at index agentIdx this turn (0 if the agent was not named). It is the
// signal the [NameMatch] heuristic weights and the matcher's AddressOnly gate
// consults.
func (dc *DecisionContext) NameScore(agentIdx int) float64 { return dc.nameScores[agentIdx] }

// WasLastAddressed reports whether agentID was among the targets addressed on
// the immediately preceding turn — the continuation signal (ADR-0024
// "last-speaker continuation").
func (dc *DecisionContext) WasLastAddressed(agentID string) bool { return dc.lastAddressed[agentID] }

// InterruptedWithin reports whether agentID was interrupted (barged in on) at
// some point within d of [DecisionContext.Now]. A non-positive d never matches.
func (dc *DecisionContext) InterruptedWithin(agentID string, d time.Duration) bool {
	if d <= 0 {
		return false
	}
	at, ok := dc.interruptions[agentID]
	if !ok {
		return false
	}
	return dc.Now.Sub(at) <= d
}

// MentionedRecently reports whether word (compared case-insensitively after
// tokenization) appears among the words spoken within the recency window,
// including the current utterance.
func (dc *DecisionContext) MentionedRecently(word string) bool {
	for _, w := range tokenize(word) {
		if _, ok := dc.recentWords[w]; ok {
			return true
		}
	}
	return false
}

// NonAddressableCount returns the number of currently active Agents that are
// not AddressOnly — the population the single-NPC fallback reasons about.
func (dc *DecisionContext) NonAddressableCount() int { return dc.nonAddressable }

// Heuristic contributes a score for one candidate Agent on one utterance.
// Scores are additive across the configured heuristics; an Agent whose total
// reaches the matcher's threshold is addressed. A heuristic returns its own
// already-weighted contribution (weight is a field of the concrete type), so
// the stack is reordered and retuned purely by editing the slice handed to the
// [Matcher] — nothing else changes.
//
// Heuristics must be pure functions of (Agent, *DecisionContext): no hidden
// state, no mutation. That is what keeps the matcher deterministic and the
// stack unit-testable one heuristic at a time.
type Heuristic interface {
	// Name is a short, stable identifier used in score breakdowns and tests.
	Name() string
	// Score returns this heuristic's contribution to a's total for this turn.
	Score(a Agent, dc *DecisionContext) float64
}

// NameMatch is the explicit-address heuristic and the backbone of the stack:
// it contributes its full Weight the moment an Agent's fuzzy name similarity
// reaches Threshold, and nothing below it. The contribution is flat rather than
// similarity-scaled so that a misheard-but-recognized name (a homophone scoring
// 0.9, ADR-0024's core case) crosses the same address threshold a crisp name
// does — recognizing the name at all is what addresses the Agent.
//
// It is also the heuristic the matcher's AddressOnly gate keys off: an
// AddressOnly Agent (the Butler by default) is eligible only when its name
// similarity reaches this Threshold, so ambient roleplay never routes to it.
type NameMatch struct {
	// Weight is the contribution when the name is recognized.
	Weight float64
	// Threshold is the minimum fuzzy similarity that counts as a name hit.
	// Below it the heuristic contributes nothing and the Agent is not
	// considered named (and an AddressOnly Agent stays unreachable).
	Threshold float64
}

// Name implements [Heuristic].
func (NameMatch) Name() string { return "name_match" }

// Score implements [Heuristic].
func (h NameMatch) Score(a Agent, dc *DecisionContext) float64 {
	if dc.NameScore(a.index) < h.Threshold {
		return 0
	}
	return h.Weight
}

// LastAddressed rewards the Agent addressed on the previous turn, so a
// follow-up utterance with no fresh name ("and then what happened?") stays with
// the same Agent (ADR-0024 "last-speaker continuation").
type LastAddressed struct {
	Weight float64
}

// Name implements [Heuristic].
func (LastAddressed) Name() string { return "last_addressed" }

// Score implements [Heuristic].
func (h LastAddressed) Score(a Agent, dc *DecisionContext) float64 {
	if dc.AnyNameMatched() {
		return 0
	}
	if dc.WasLastAddressed(a.Target.AgentID) {
		return h.Weight
	}
	return 0
}

// RecentlyInterrupted rewards an Agent that was barged in on within Within of
// now: a participant who cut the Agent off is very likely still talking to it,
// so the next utterance should land back on that Agent even without a name.
// Interruptions are fed to the matcher via [Matcher.NoteInterruption].
type RecentlyInterrupted struct {
	Weight float64
	Within time.Duration
}

// Name implements [Heuristic].
func (RecentlyInterrupted) Name() string { return "recently_interrupted" }

// Score implements [Heuristic].
func (h RecentlyInterrupted) Score(a Agent, dc *DecisionContext) float64 {
	if dc.AnyNameMatched() {
		return 0
	}
	within := h.Within
	if within <= 0 {
		within = dc.window
	}
	if dc.InterruptedWithin(a.Target.AgentID, within) {
		return h.Weight
	}
	return 0
}

// ExpertOnRecentWord rewards an Agent whose Expertise keywords were mentioned
// recently: when the table keeps saying "tavern" and "ale", the innkeeper NPC
// who is the expert on those words is the likely addressee even before anyone
// says his name. Matching is exact (post-tokenization) for determinism.
type ExpertOnRecentWord struct {
	Weight float64
	// PerWord, when true, scales the contribution by how many distinct
	// expertise keywords were mentioned rather than awarding a flat Weight on
	// the first hit. Capped at Weight × len(Expertise) either way.
	PerWord bool
}

// Name implements [Heuristic].
func (ExpertOnRecentWord) Name() string { return "expert_on_recent_word" }

// Score implements [Heuristic].
func (h ExpertOnRecentWord) Score(a Agent, dc *DecisionContext) float64 {
	if dc.AnyNameMatched() {
		return 0
	}
	hits := 0
	for _, kw := range a.Expertise {
		if dc.MentionedRecently(kw) {
			hits++
			if !h.PerWord {
				return h.Weight
			}
		}
	}
	return h.Weight * float64(hits)
}

// SoleActiveNPC rewards a non-AddressOnly Agent when it is the only one active:
// with a single Character NPC in the Voice Session, an unaddressed utterance
// most likely targets it (ADR-0024 "single active NPC fallback"). It is the
// scoring stack's parity with the dumb matcher's lone-NPC behaviour, expressed
// as just another pluggable heuristic.
type SoleActiveNPC struct {
	Weight float64
}

// Name implements [Heuristic].
func (SoleActiveNPC) Name() string { return "sole_active_npc" }

// Score implements [Heuristic].
func (h SoleActiveNPC) Score(a Agent, dc *DecisionContext) float64 {
	if dc.AnyNameMatched() {
		return 0
	}
	if !a.AddressOnly && dc.NonAddressableCount() == 1 {
		return h.Weight
	}
	return 0
}
