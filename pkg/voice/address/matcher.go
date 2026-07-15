package address

import (
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Agent is one routing candidate the [Matcher] scores: the Tenant's Butler or
// a Campaign's Character NPC (CONTEXT.md "Agent"), enriched with the matching
// and scoring metadata the dumb matcher's bare [voiceevent.AddressTarget]
// lacks.
type Agent struct {
	// Target is the routing decision published when this Agent is addressed.
	Target voiceevent.AddressTarget
	// Aliases are additional spoken names matched fuzzily alongside
	// Target.Name ("Bartholomew", "the innkeeper" for "Bart"). The primary
	// Target.Name is always matchable; aliases extend it.
	Aliases []string
	// TruncationAliases are auto-derived STT-truncation forms of this Agent's
	// name/aliases ("art" for "Bart", #197). Unlike [Agent.Aliases] they are
	// matched EXACT-ONLY and only when the matched window opens the utterance, so
	// a mid-sentence noun ("was für eine Art …") never routes here. Callers derive
	// them with [DeriveTruncationAliases]; empty for Agents with none.
	TruncationAliases []string
	// Expertise lists words this Agent is considered the expert on, consumed by
	// the [ExpertOnRecentWord] heuristic.
	Expertise []string
	// AddressOnly marks an Agent reachable only by an explicit name match
	// (CONTEXT.md "Address-Only"): it is excluded from every non-name heuristic,
	// so continuation, interruption, and single-NPC fallback can never route to
	// it. The Butler is conventionally AddressOnly; Character NPCs are not.
	AddressOnly bool

	index int // position in the matcher's agents slice; set at construction
}

// matchableNames returns the Agent's primary Name followed by its aliases.
func (a Agent) matchableNames() []string {
	names := make([]string, 0, 1+len(a.Aliases))
	if a.Target.Name != "" {
		names = append(names, a.Target.Name)
	}
	names = append(names, a.Aliases...)
	return names
}

// Config tunes a [Matcher]. The zero value is usable: every field has a
// default applied at construction, yielding the default `en` encoder, the
// default heuristic stack, and a threshold of 1.0 (one full-weight signal).
type Config struct {
	// Language selects the phonetic [Encoder] from Encoders (CONTEXT.md
	// "Campaign Language"). An unregistered language falls back to the
	// edit-distance net alone.
	Language string
	// Encoders is the phonetic registry. Nil uses [DefaultEncoders].
	Encoders *EncoderRegistry
	// NameMatch tunes the fuzzy engine (windowing, rune floor, edit bound).
	NameMatch NameMatchConfig
	// Heuristics is the scoring stack. Nil uses [DefaultHeuristics]; an
	// explicitly empty (non-nil, zero-length) slice disables scoring entirely
	// (nothing is ever addressed), which is occasionally useful in tests.
	Heuristics []Heuristic
	// AddressThreshold is the minimum total score an Agent needs to be
	// addressed. Default 1.0.
	AddressThreshold float64
	// MaxTargets caps how many Agents one utterance may address, applied to the
	// score-sorted hits before the decision set is built (so only the published
	// Agents become the next turn's continuation context). Zero defaults to 1
	// (single-target: naming two NPCs fires one turn on the top-scored). A
	// positive N caps at N; a negative value lifts the cap entirely, restoring
	// the full Ensemble Turn set (ADR-0025).
	MaxTargets int
	// ButlerGMGate reports whether a SpeakerID may voice-address the Butler
	// (ADR-0024 GM-only rule; #280 identity, ADR-0050 allowlist membership). When
	// non-nil it arms the matcher-side Butler eligibility drop: on
	// [Matcher.TargetMatchFrom], a Butler-role candidate is removed from the
	// scored set BEFORE scoring, the MaxTargets cap, and lastAddressed whenever the
	// speaker fails the gate (an empty SpeakerID fails closed). This is the #256
	// relocation of the former detector-level drop
	// ([orchestrator.WithButlerGMGate]): pre-cap the Butler can no longer shadow a
	// co-named NPC into a lost slot nor black-hole the next unnamed continuation.
	// Nil leaves the Butler addressable by any speaker (the rollout default, and
	// byte-identical to the pre-gate [Matcher.TargetMatch] path).
	ButlerGMGate func(speakerID string) bool
	// RecencyWindow bounds how long mentioned words and interruptions stay
	// "recent". Default 30s.
	RecencyWindow time.Duration
	// MaxRecentWords caps the rolling mentioned-word buffer. Default 200.
	MaxRecentWords int
	// Clock supplies the current time; nil uses time.Now. Tests inject a fake
	// clock to drive the recency windows deterministically.
	Clock func() time.Time
}

// DefaultHeuristics returns the v1.0 scoring stack: explicit name match
// (dominant), last-addressed continuation, expert-on-recent-word, and the
// single-NPC fallback.
//
// The weights encode ADR-0024's ordered chain as additive scores against the
// default threshold of 1.0. An explicit name match (weight 1.0) addresses on
// its own and suppresses every ambient heuristic (see
// [DecisionContext.AnyNameMatched]), so a named Agent is never joined by a
// fallback. When no name is heard the strong ambient signals — continuation
// and the lone-NPC fallback — are each individually decisive (weight 1.0),
// mirroring the chain's stages, while expert-on-word is a weak hint (0.5) that
// only reinforces or breaks ties. No ambient signal can ever route to an
// AddressOnly Agent, which stays name-gated regardless.
//
// [RecentlyInterrupted] is deliberately NOT a default (#442): its signal
// source — [Matcher.NoteInterruption], fed from the barge path — is deferred to
// v1.5+ per ADR-0027, so as a default it could never fire and would only
// mislead routing tuners. Opt in via Config.Heuristics once the barge wiring
// exists.
func DefaultHeuristics() []Heuristic {
	return []Heuristic{
		NameMatch{Weight: 1.0, Threshold: 0.6},
		LastAddressed{Weight: 1.0},
		ExpertOnRecentWord{Weight: 0.5},
		SoleActiveNPC{Weight: 1.0},
	}
}

// Matcher is the scoring Address Detection algorithm. It satisfies the
// orchestrator's TargetMatcher seam and is handed to
// orchestrator.NewAddressDetector:
//
//	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart)
//	d := orchestrator.NewAddressDetector(m)
//
// Per utterance it fuzzily scores every Agent's name, scores each Agent through
// the heuristic stack, and returns the set whose total reaches the threshold
// (highest score first). The set may be empty (no target — the utterance is
// still transcribed), hold one Agent (the usual case), or hold several (an
// Ensemble Turn, ADR-0025). It then records the decision and the utterance's
// words so the next turn's continuation and expertise heuristics can see them.
//
// A Matcher carries mutable conversational state and is safe for concurrent
// use; the bus delivers events synchronously but possibly from different
// goroutines.
type Matcher struct {
	nameMatch  NameMatchConfig // retained to rebuild the index on roster changes
	enc        Encoder         // retained to rebuild the index on roster changes
	heuristics []Heuristic
	threshold  float64
	maxTargets int // resolved cap: >0 caps at N, <0 is unlimited (0 never stored)
	window     time.Duration
	maxWords   int
	clock      func() time.Time
	// butlerGate is Config.ButlerGMGate: the matcher-side Butler eligibility gate
	// consulted by TargetMatchFrom (#256). Nil means the Butler is addressable by
	// any speaker (rollout default), so TargetMatchFrom matches TargetMatch.
	butlerGate func(speakerID string) bool

	mu sync.Mutex
	// index is rebuilt by Add/Remove in lockstep with agents, and TargetMatch
	// reads both under the same mutex, so one scoring pass always sees a
	// mutually consistent index/roster pair (#145): a departed Agent's name
	// score can never land on a survivor reindexed into its slot.
	index         *fuzzyIndex
	agents        []Agent
	lastAddressed map[string]bool
	interruptions map[string]time.Time
	recentWords   []timedWord
	// muted holds the AgentIDs currently muted (#225). Mute is MATCHER-INTERNAL
	// state guarded by m.mu — the SAME critical section as index and agents — so
	// a scoring pass reads a mutually consistent index/roster/mute triple and
	// SetMuted never rebuilds the index (ADR-0024 one-snapshot invariant, #145).
	// A muted Agent stays in the index and is still matched by name, but is
	// name-gated like an AddressOnly Agent (excluded from every ambient
	// heuristic), is dropped from the published set whenever any unmuted Agent is
	// also addressed, and is never recorded as lastAddressed. It is published
	// only when it is the SOLE addressee, so its decision flows to the reactor's
	// mute gate (which ends the turn pre-audio) instead of re-routing to another
	// NPC — the exact #225 failure this closes.
	muted map[string]struct{}
}

type timedWord struct {
	word string
	at   time.Time
}

// NewMatcher builds a [Matcher] for agents under cfg. It panics if no agents
// are given or if any Agent has an empty Target.AgentID, since a decision that
// cannot name its target downstream is a wiring error, not a runtime condition.
func NewMatcher(cfg Config, agents ...Agent) *Matcher {
	if len(agents) == 0 {
		panic("address.NewMatcher: at least one agent is required")
	}
	encoders := cfg.Encoders
	if encoders == nil {
		encoders = DefaultEncoders()
	}
	enc, _ := encoders.For(cfg.Language)

	heuristics := cfg.Heuristics
	if heuristics == nil {
		heuristics = DefaultHeuristics()
	}
	threshold := cfg.AddressThreshold
	if threshold <= 0 {
		threshold = 1.0
	}
	window := cfg.RecencyWindow
	if window <= 0 {
		window = 30 * time.Second
	}
	maxWords := cfg.MaxRecentWords
	if maxWords <= 0 {
		maxWords = 200
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	maxTargets := cfg.MaxTargets
	if maxTargets == 0 {
		maxTargets = 1
	}

	agents = append([]Agent(nil), agents...)
	for i := range agents {
		if agents[i].Target.AgentID == "" {
			panic("address.NewMatcher: agent Target.AgentID must not be empty")
		}
		if !validAgentRole(agents[i].Target.AgentRole) {
			panic(`address.NewMatcher: agent Target.AgentRole must be "butler" or "character", got ` +
				strconv.Quote(agents[i].Target.AgentRole))
		}
		agents[i].index = i
	}

	m := &Matcher{
		nameMatch:     cfg.NameMatch,
		enc:           enc,
		heuristics:    heuristics,
		threshold:     threshold,
		maxTargets:    maxTargets,
		window:        window,
		maxWords:      maxWords,
		clock:         clock,
		butlerGate:    cfg.ButlerGMGate,
		agents:        agents,
		lastAddressed: map[string]bool{},
		interruptions: map[string]time.Time{},
		muted:         map[string]struct{}{},
	}
	m.index = m.buildIndex()
	return m
}

// validAgentRole reports whether role is one of the two valid Agent Role values
// (CONTEXT.md "Agent Role"). Both matcher constructors reject anything else so a
// mistyped or empty role dies at wiring time rather than silently disarming the
// Butler GM-address gate (#280), which keys off [voiceevent.AgentRoleButler].
func validAgentRole(role string) bool {
	return role == voiceevent.AgentRoleButler || role == voiceevent.AgentRoleCharacter
}

// buildIndex rebuilds the fuzzy name index from the current m.agents. Callers
// that mutate m.agents (the constructor and Add/Remove) hold m.mu; the
// constructor is the sole exception, running before the Matcher is shared.
func (m *Matcher) buildIndex() *fuzzyIndex {
	names := make([][]string, len(m.agents))
	truncations := make([][]string, len(m.agents))
	for i := range m.agents {
		names[i] = m.agents[i].matchableNames()
		truncations[i] = m.agents[i].TruncationAliases
	}
	return newFuzzyIndex(m.nameMatch, m.enc, names, truncations)
}

// NoteInterruption records that the Agent with agentID was just interrupted
// (barged in on). The [RecentlyInterrupted] heuristic reads these. It is the
// seam through which the turn-taking layer feeds Barge-in signals (ADR-0027)
// to the matcher without the matcher depending on a bus event that does not yet
// exist; tests call it directly. Unknown agentIDs are recorded harmlessly.
func (m *Matcher) NoteInterruption(agentID string) {
	m.mu.Lock()
	m.interruptions[agentID] = m.clock()
	m.mu.Unlock()
}

// Add inserts agents into the live roster so they become addressable mid-Voice
// Session (a Character NPC joining the scene). It rebuilds and swaps the fuzzy
// index under the matcher's mutex so a concurrent [Matcher.TargetMatch] keeps
// scoring against a consistent index/roster pair. It panics if any Agent has an empty
// Target.AgentID or an AgentID already on the roster (or duplicated within the
// same call): a roster that cannot uniquely name its targets is a wiring error,
// matching [NewMatcher]'s contract. Adding nothing is a no-op.
func (m *Matcher) Add(agents ...Agent) {
	if len(agents) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	seen := make(map[string]struct{}, len(m.agents)+len(agents))
	for _, a := range m.agents {
		seen[a.Target.AgentID] = struct{}{}
	}
	for _, a := range agents {
		if a.Target.AgentID == "" {
			panic("address.Matcher.Add: agent Target.AgentID must not be empty")
		}
		if !validAgentRole(a.Target.AgentRole) {
			panic(`address.Matcher.Add: agent Target.AgentRole must be "butler" or "character", got ` +
				strconv.Quote(a.Target.AgentRole))
		}
		if _, dup := seen[a.Target.AgentID]; dup {
			panic("address.Matcher.Add: duplicate agent AgentID " + a.Target.AgentID)
		}
		seen[a.Target.AgentID] = struct{}{}
		m.agents = append(m.agents, a)
	}
	m.reindexLocked()
}

// Remove drops the Agents with agentIDs from the live roster (a Character NPC
// leaving the scene) and prunes their continuation and interruption state so a
// later turn never resurrects a departed Agent. Unknown agentIDs are ignored.
// Removing every Agent is allowed: the matcher then routes to nobody. It
// rebuilds and swaps the fuzzy index under the mutex like [Matcher.Add].
func (m *Matcher) Remove(agentIDs ...string) {
	if len(agentIDs) == 0 {
		return
	}
	drop := make(map[string]struct{}, len(agentIDs))
	for _, id := range agentIDs {
		drop[id] = struct{}{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	kept := m.agents[:0]
	for _, a := range m.agents {
		if _, gone := drop[a.Target.AgentID]; gone {
			continue
		}
		kept = append(kept, a)
	}
	m.agents = kept
	for id := range drop {
		delete(m.lastAddressed, id)
		delete(m.interruptions, id)
		delete(m.muted, id) // a re-Added Agent starts unmuted
	}
	m.reindexLocked()
}

// SetMuted toggles whether the Agent with agentID is muted (#225). Mute is
// matcher-internal routing state, NOT a roster change: a muted Agent stays in
// the fuzzy index and is still matched by name, but is name-gated like an
// AddressOnly Agent — excluded from every ambient heuristic, dropped from the
// published set whenever an unmuted Agent is also addressed, and never recorded
// as lastAddressed. It is published only when it is the SOLE addressee, so an
// explicit address to a muted NPC flows to the reactor's mute gate (which ends
// the turn before any audio) instead of re-routing to another NPC.
//
// The flag lives under m.mu — the same critical section as the index and roster
// — so SetMuted never rebuilds the index (unlike Add/Remove) and a concurrent
// TargetMatch reads a consistent index/roster/mute triple (ADR-0024, #145).
// SetMuted is idempotent and a no-op for an unknown agentID. Muting is a state
// TRANSITION check first (wireMutes re-fires SetMuted per event): only an actual
// unmuted→muted flip prunes the Agent's lastAddressed and interruption state,
// mirroring Remove's prune so a later unnamed continuation cannot resurrect the
// muted Agent (#211 parity). Remove clears the flag, so a re-Added Agent is
// unmuted.
func (m *Matcher) SetMuted(agentID string, muted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	known := false
	for i := range m.agents {
		if m.agents[i].Target.AgentID == agentID {
			known = true
			break
		}
	}
	if !known {
		return // unknown Agent: nothing to mute
	}

	if _, already := m.muted[agentID]; muted == already {
		return // already in the requested state: idempotent, no prune
	}
	if !muted {
		delete(m.muted, agentID)
		return
	}
	// Unmuted → muted transition: mute and prune continuation/interruption state
	// so an unnamed follow-up never continues into the muted Agent (#211 parity).
	m.muted[agentID] = struct{}{}
	delete(m.lastAddressed, agentID)
	delete(m.interruptions, agentID)
}

// reindexLocked reassigns each Agent's index to its new slice position and swaps
// in a freshly built fuzzy index. Caller holds m.mu — the same mutex TargetMatch
// scores under — so index and roster change together and a scoring pass never
// pairs one rebuild's index with another's roster (#145).
func (m *Matcher) reindexLocked() {
	for i := range m.agents {
		m.agents[i].index = i
	}
	m.index = m.buildIndex()
}

// TargetMatch scores text against every Agent and returns the addressed set,
// highest total first. It implements the orchestrator's TargetMatcher. It applies
// no Butler GM-gate: the Butler is addressable by any speaker on this path (the
// gate rides the SpeakerID-aware [Matcher.TargetMatchFrom] instead).
func (m *Matcher) TargetMatch(text string) []voiceevent.AddressRouted {
	return m.match(text, false)
}

// TargetMatchFrom is the SpeakerID-aware routing entry (#256): identical to
// [Matcher.TargetMatch] except it applies the [Config.ButlerGMGate] Butler
// eligibility drop. When a ButlerGMGate is configured and speakerID fails it (or
// is empty — fail closed), every Butler-role candidate is excluded from the scored
// set BEFORE scoring, the MaxTargets cap, and lastAddressed — so a non-GM naming
// the Butler neither shadows a co-named NPC's slot nor black-holes the next
// unnamed continuation (the ADR-0024 amendment / #256 blocker). With no gate
// configured it behaves exactly like TargetMatch, so the Butler answers any
// speaker (the rollout default). It satisfies the orchestrator's
// SpeakerAwareMatcher seam.
func (m *Matcher) TargetMatchFrom(speakerID, text string) []voiceevent.AddressRouted {
	excludeButler := m.butlerGate != nil && (speakerID == "" || !m.butlerGate(speakerID))
	return m.match(text, excludeButler)
}

// match is the shared scoring core behind TargetMatch and TargetMatchFrom.
// excludeButler drops every Butler-role candidate before scoring (the #256
// pre-cap Butler GM-gate), so an excluded Butler never scores, never consumes a
// MaxTargets slot, and is never recorded as lastAddressed.
func (m *Matcher) match(text string, excludeButler bool) []voiceevent.AddressRouted {
	now := m.clock()
	// Offset-preserving tokenization on the utterance path: the token TEXT is
	// byte-identical to tokenize(text), and the byte offsets/gap markers drive the
	// Vocative Flag (ADR-0024). words feeds the recency/heuristic state unchanged.
	toks, markAfter := offsetTokenize(text)
	words := make([]string, len(toks))
	for i := range toks {
		words[i] = toks[i].text
	}

	m.mu.Lock()

	// Score the index in the same critical section as the roster it is scored
	// against: reading it before the lock could pair a pre-Remove index with a
	// post-Remove roster and hand one agent's name score to the survivor
	// reindexed into its slot (#145). nameScores stays keyed by m.agents
	// positions only because index and roster come from one snapshot.
	nameScores, nameFlagged, namePositions := m.index.score(toks, markAfter)

	m.pruneLocked(now)
	m.recordWordsLocked(words, now)

	nameThreshold := m.nameThreshold()
	anyNameMatched := false
	for _, score := range nameScores {
		if score >= nameThreshold {
			anyNameMatched = true
			break
		}
	}

	dc := &DecisionContext{
		Now:            now,
		Utterance:      text,
		Words:          words,
		window:         m.window,
		nameScores:     nameScores,
		anyNameMatched: anyNameMatched,
		lastAddressed:  m.lastAddressed,
		interruptions:  m.interruptions,
		recentWords:    m.recentWordSetLocked(),
		nonAddressable: m.nonAddressableCount(),
	}

	type scored struct {
		agent Agent
		total float64
	}
	var hits []scored
	for _, a := range m.agents {
		// Butler GM-gate (#256): a Butler-role candidate the speaker may not
		// address is dropped here — before scoring, the cap, and lastAddressed — so
		// it can neither shadow a co-named NPC's MaxTargets slot nor be recorded as
		// last-addressed. This is the matcher-side relocation of the former
		// detector-level drop (ADR-0024 amendment).
		if excludeButler && a.Target.AgentRole == voiceevent.AgentRoleButler {
			continue
		}
		// AddressOnly agents are reachable only by an explicit name match; no
		// ambient heuristic may route to them (CONTEXT.md "Address-Only"). A muted
		// Agent is name-gated the same way (#225): it stays matchable by name but
		// is excluded from every ambient heuristic, so a muted addressee never
		// re-routes to another NPC via continuation or the sole-NPC fallback.
		if _, isMuted := m.muted[a.Target.AgentID]; (a.AddressOnly || isMuted) && nameScores[a.index] < nameThreshold {
			continue
		}
		var total float64
		for _, h := range m.heuristics {
			total += h.Score(a, dc)
		}
		if total >= m.threshold {
			hits = append(hits, scored{agent: a, total: total})
		}
	}

	// A muted Agent that was named alongside an unmuted one must not shadow the
	// unmuted answer (#225 "Greta und Bart"): if any unmuted Agent was addressed
	// this turn, drop every muted hit BEFORE the score-sort and MaxTargets cap, so
	// a muted score never wins a tie-break and never consumes a cap slot. A muted
	// hit survives only when it is the ONLY thing addressed (a solo address to a
	// muted NPC), so its decision flows to the reactor's mute gate and ends the
	// turn with TurnEndMute rather than re-routing.
	anyUnmuted := false
	for i := range hits {
		if _, isMuted := m.muted[hits[i].agent.Target.AgentID]; !isMuted {
			anyUnmuted = true
			break
		}
	}
	if anyUnmuted {
		kept := hits[:0]
		for _, h := range hits {
			if _, isMuted := m.muted[h.agent.Target.AgentID]; isMuted {
				continue
			}
			kept = append(kept, h)
		}
		hits = kept
	}

	// Tie-break chain (ADR-0024 amendment, #400/#413):
	//   1. highest total wins;
	//   2. a Vocative-flagged name (punctuation-bracketed as a direct address) beats
	//      an unflagged one;
	//   3. higher fuzzy name similarity;
	//   4. earlier addressee position;
	//   5. stable roster order.
	//
	// NameMatch contributes a FLAT weight, so every name-matched hit totals the same
	// (1.0) and tiers 2–4 do the real work. The Vocative Flag (tier 2) is why "So,
	// Glyfoxa, was hat Bart …?" routes to the Butler: "Glyfoxa" is bracketed by
	// commas (flagged) while "Bart" sits mid-clause (unflagged), so the flagged
	// Butler wins even though its phonetic similarity (0.9) is below Bart's exact
	// 1.0 — the #400 headline. Conversely "Bart, ist Glyphoxa hier?" flags only Bart
	// (Glyphoxa's left neighbour is a word), so the Character wins. When neither name
	// is bracketed (no punctuation at all) tier 2 is a wash and tier 3 restores the
	// plain score order (documented no-punctuation degrade). The similarity tier
	// still keeps a phonetic topic-collision ("geht" ≈ "Gott") from stealing an
	// exactly-addressed Character (#413), and #198/#199 char-vs-char resolve on tiers
	// 3–4 exactly as before. (A GM-gated-out Butler is excluded above the sort.)
	flag := func(a Agent) bool { return nameFlagged[a.index] }
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].total != hits[j].total {
			return hits[i].total > hits[j].total
		}
		if fi, fj := flag(hits[i].agent), flag(hits[j].agent); fi != fj {
			return fi
		}
		si, sj := nameScores[hits[i].agent.index], nameScores[hits[j].agent.index]
		if si != sj {
			return si > sj
		}
		return namePositions[hits[i].agent.index] < namePositions[hits[j].agent.index]
	})

	// Cap the published set before building it, so lastAddressed records only
	// the Agents actually addressed: naming two NPCs under the single-target
	// default fires one turn on the top-scored, not two.
	if m.maxTargets >= 0 && len(hits) > m.maxTargets {
		hits = hits[:m.maxTargets]
	}

	addressed := make(map[string]bool, len(hits))
	out := make([]voiceevent.AddressRouted, 0, len(hits))
	for _, h := range hits {
		out = append(out, voiceevent.AddressRouted{
			At:     now,
			Text:   text,
			Target: h.agent.Target,
		})
		// A muted addressee is published (it flows to the reactor's mute gate) but
		// is NEVER recorded as lastAddressed (#225): recording it would black-hole
		// the next unnamed follow-up onto a muted Agent that produces no answer.
		if _, isMuted := m.muted[h.agent.Target.AgentID]; isMuted {
			continue
		}
		// An AddressOnly addressee (the Butler, #299) is likewise published but
		// NEVER recorded as lastAddressed. ADR-0024 excludes AddressOnly Agents
		// from the last-speaker heuristic, so recording it can never help routing
		// — it can only black-hole the next unnamed continuation onto an Agent no
		// ambient heuristic can ever reach. Mirror the muted-agent guard above: a
		// GM naming the Butler mid-scene must not erase who the player was talking
		// to.
		if h.agent.AddressOnly {
			continue
		}
		addressed[h.agent.Target.AgentID] = true
	}
	// Record this turn's UNMUTED addressees for next turn's continuation heuristic.
	// Stay put on a no-target (or all-muted) turn rather than forgetting who held
	// the floor.
	if len(addressed) > 0 {
		m.lastAddressed = addressed
	}

	m.mu.Unlock()
	return out
}

// nameThreshold returns the Threshold of the first [NameMatch] heuristic in the
// stack, used by the AddressOnly gate. If the stack has no NameMatch heuristic
// the gate falls back to "any positive similarity counts as a name hit".
func (m *Matcher) nameThreshold() float64 {
	for _, h := range m.heuristics {
		if nm, ok := h.(NameMatch); ok {
			return nm.Threshold
		}
	}
	return 0.000001
}

// nonAddressableCount returns how many Agents are eligible for the ambient
// heuristics: not AddressOnly and not muted (#225). Every non-muted Agent the
// matcher was built with is active for the lifetime of the Voice Session, so the
// sole-NPC fallback sees the surviving addressable NPCs only — muting one of two
// NPCs makes the other the sole active NPC. Caller holds m.mu.
func (m *Matcher) nonAddressableCount() int {
	n := 0
	for _, a := range m.agents {
		if a.AddressOnly {
			continue
		}
		if _, isMuted := m.muted[a.Target.AgentID]; isMuted {
			continue
		}
		n++
	}
	return n
}

// pruneLocked drops words and interruptions older than the recency window so
// the buffers stay bounded over a long Voice Session. Caller holds m.mu.
func (m *Matcher) pruneLocked(now time.Time) {
	cutoff := now.Add(-m.window)
	keep := m.recentWords[:0]
	for _, w := range m.recentWords {
		if !w.at.Before(cutoff) {
			keep = append(keep, w)
		}
	}
	m.recentWords = keep
	for id, at := range m.interruptions {
		if at.Before(cutoff) {
			delete(m.interruptions, id)
		}
	}
}

// recordWordsLocked appends the current utterance's words to the rolling
// buffer, trimming the oldest entries past the cap. Caller holds m.mu.
func (m *Matcher) recordWordsLocked(words []string, now time.Time) {
	for _, w := range words {
		m.recentWords = append(m.recentWords, timedWord{word: w, at: now})
	}
	if len(m.recentWords) > m.maxWords {
		m.recentWords = m.recentWords[len(m.recentWords)-m.maxWords:]
	}
}

// recentWordSetLocked returns the distinct words currently in the buffer.
// Caller holds m.mu.
func (m *Matcher) recentWordSetLocked() map[string]struct{} {
	set := make(map[string]struct{}, len(m.recentWords))
	for _, w := range m.recentWords {
		set[w.word] = struct{}{}
	}
	return set
}
