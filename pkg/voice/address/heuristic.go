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
	// SpeakerID identifies who spoke the utterance (the Speaker Lane's Discord
	// snowflake, ADR-0050). Empty when the caller could not attribute it — the
	// text-only [Matcher.TargetMatch] path and the unattributed default lane.
	SpeakerID string

	nameScores      map[int]float64 // agent index → fuzzy name similarity
	anyNameMatched  bool            // did any agent clear the name threshold this turn
	lastAddressed   map[string]bool // agentID → holds the continuation claim
	lastAddressedAt time.Time       // the claim's anchor (zero: never addressed)
	lastAddressedBy string          // SpeakerID that opened the claim ("" unknown)
	nonAddressable  int             // count of active, non-AddressOnly agents
}

// AnyNameMatched reports whether some Agent was explicitly named this turn (its
// fuzzy similarity cleared the [NameMatch] threshold). The ambient heuristics —
// continuation, single-NPC fallback — return zero when this is true, so an
// explicit name short-circuits them exactly as ADR-0024's ordered chain does:
// name match wins outright and the fallbacks never widen the addressed set
// behind it.
func (dc *DecisionContext) AnyNameMatched() bool { return dc.anyNameMatched }

// NameScore returns the fuzzy name-match similarity in [0,1] computed for the
// agent at index agentIdx this turn (0 if the agent was not named). It is the
// signal the [NameMatch] heuristic weights and the matcher's AddressOnly gate
// consults.
func (dc *DecisionContext) NameScore(agentIdx int) float64 { return dc.nameScores[agentIdx] }

// AddressedWithin reports whether agentID holds the continuation claim for THIS
// speaker and the claim's anchor is no older than d — the continuation signal
// (ADR-0024 "last-speaker continuation"). A non-positive d means "no time
// bound".
//
// The claim is speaker-scoped (ADR-0024 amendment, 2026-11-20): it belongs to
// the speaker whose named address opened it, so only that speaker continues
// without a name. Somebody else at the table talking — to another player, to
// the GM, to nobody — is not continuing a conversation they were never in. The
// scope applies only when BOTH speakers are known; an unattributed utterance or
// an unattributed opener keeps the pre-amendment table-wide behaviour, so a
// deployment without Speaker Lanes loses nothing.
//
// The anchor is per CLAIM, not per Agent: the matcher replaces its whole
// last-addressed set atomically, so every member of a set shares one instant.
// It is the later of the routing decision and the addressed Agent's last
// audible reply activity ([Matcher.ExtendContinuation]).
func (dc *DecisionContext) AddressedWithin(agentID string, d time.Duration) bool {
	if !dc.lastAddressed[agentID] {
		return false
	}
	if !sameSpeaker(dc.SpeakerID, dc.lastAddressedBy) {
		return false
	}
	if d <= 0 {
		return true
	}
	return !dc.lastAddressedAt.IsZero() && dc.Now.Sub(dc.lastAddressedAt) <= d
}

// sameSpeaker reports whether speakerID may act on a continuation claim opened
// by owner. Two KNOWN, different speakers are the only pair that may not; an
// unknown on either side falls back to the table-wide claim.
func sameSpeaker(speakerID, owner string) bool {
	return speakerID == "" || owner == "" || speakerID == owner
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
// The similarity it reads has already been through the address-evidence rule
// ([fuzzyIndex.score]): a non-exact hit is in the score map only when it stood
// in vocative position, so this heuristic never sees an everyday word that
// merely sounds like the name.
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

// LastAddressed rewards the Agent the SAME speaker addressed last, so that
// speaker's follow-up with no fresh name ("and then what happened?") stays with
// it (ADR-0024 "last-speaker continuation"). See
// [DecisionContext.AddressedWithin] for the speaker scope.
//
// Within bounds how long that claim survives (ADR-0024 amendment, 2026-07-27),
// measured from the claim's anchor: the routing decision, moved forward by the
// addressed Agent's reply while it is audibly answering. Unbounded continuation
// was the live "the NPC answers everything" defect: the Agent named once at the
// start of a session kept scoring the full decisive weight on EVERY unnamed
// utterance for the rest of the session. A conversation the table has walked
// away from is over; a zero or negative Within restores the old unbounded
// behaviour for callers that want it.
type LastAddressed struct {
	Weight float64
	Within time.Duration
}

// Name implements [Heuristic].
func (LastAddressed) Name() string { return "last_addressed" }

// Score implements [Heuristic].
func (h LastAddressed) Score(a Agent, dc *DecisionContext) float64 {
	if dc.AnyNameMatched() {
		return 0
	}
	if !dc.AddressedWithin(a.Target.AgentID, h.Within) {
		return 0
	}
	return h.Weight
}

// SoleActiveNPC rewards a non-AddressOnly Agent when it is the only one active:
// with a single Character NPC in the Voice Session, an unaddressed utterance
// most likely targets it (ADR-0024 "single active NPC fallback").
//
// It is NOT part of [DefaultHeuristicsWithin]. The 2026-07-27 amendment demoted
// it from decisive to a 0.5 corroborating hint because at full weight a lone NPC
// answered every utterance anybody made; the only signal it could corroborate
// (expert-on-recent-word) was never fed in production and was removed
// 2026-11-20, which left the hint unable to reach the address threshold with
// anything. It stays as an opt-in for a caller that wants a lone NPC to take
// unaddressed speech — and for the tests that observe the ambient pool through
// it.
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
