package address

import (
	"sort"
	"sync"
	"sync/atomic"
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
// (dominant), last-addressed continuation, recent-interruption recovery,
// expert-on-recent-word, and the single-NPC fallback.
//
// The weights encode ADR-0024's ordered chain as additive scores against the
// default threshold of 1.0. An explicit name match (weight 1.0) addresses on
// its own and suppresses every ambient heuristic (see
// [DecisionContext.AnyNameMatched]), so a named Agent is never joined by a
// fallback. When no name is heard the strong ambient signals — continuation,
// interruption recovery, and the lone-NPC fallback — are each individually
// decisive (weight 1.0), mirroring the chain's stages, while expert-on-word is
// a weak hint (0.5) that only reinforces or breaks ties. No ambient signal can
// ever route to an AddressOnly Agent, which stays name-gated regardless.
func DefaultHeuristics() []Heuristic {
	return []Heuristic{
		NameMatch{Weight: 1.0, Threshold: 0.6},
		LastAddressed{Weight: 1.0},
		RecentlyInterrupted{Weight: 1.0, Within: 15 * time.Second},
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
	// index is read lock-free by TargetMatch and swapped wholesale by Add/Remove
	// under m.mu, so a concurrent roster rebuild never tears a scoring pass.
	index atomic.Pointer[fuzzyIndex]

	nameMatch  NameMatchConfig // retained to rebuild the index on roster changes
	enc        Encoder         // retained to rebuild the index on roster changes
	heuristics []Heuristic
	threshold  float64
	maxTargets int // resolved cap: >0 caps at N, <0 is unlimited (0 never stored)
	window     time.Duration
	maxWords   int
	clock      func() time.Time

	mu            sync.Mutex
	agents        []Agent
	lastAddressed map[string]bool
	interruptions map[string]time.Time
	recentWords   []timedWord
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
		agents:        agents,
		lastAddressed: map[string]bool{},
		interruptions: map[string]time.Time{},
	}
	m.index.Store(m.buildIndex())
	return m
}

// buildIndex rebuilds the fuzzy name index from the current m.agents. Callers
// that mutate m.agents (the constructor and Add/Remove) hold m.mu; the
// constructor is the sole exception, running before the Matcher is shared.
func (m *Matcher) buildIndex() *fuzzyIndex {
	names := make([][]string, len(m.agents))
	for i := range m.agents {
		names[i] = m.agents[i].matchableNames()
	}
	return newFuzzyIndex(m.nameMatch, m.enc, names)
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
// Session (a Character NPC joining the scene). It rebuilds and atomically swaps
// the fuzzy index so a concurrent [Matcher.TargetMatch] keeps scoring against a
// consistent index throughout. It panics if any Agent has an empty
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
// rebuilds and atomically swaps the fuzzy index like [Matcher.Add].
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
	}
	m.reindexLocked()
}

// reindexLocked reassigns each Agent's index to its new slice position and swaps
// in a freshly built fuzzy index. Caller holds m.mu. The index Store is atomic,
// so TargetMatch's lock-free read always sees a whole index — the old one until
// the swap, the new one after, never a half-built one.
func (m *Matcher) reindexLocked() {
	for i := range m.agents {
		m.agents[i].index = i
	}
	m.index.Store(m.buildIndex())
}

// TargetMatch scores text against every Agent and returns the addressed set,
// highest total first. It implements the orchestrator's TargetMatcher.
func (m *Matcher) TargetMatch(text string) []voiceevent.AddressRouted {
	now := m.clock()
	words := tokenize(text)
	// Read the index lock-free: Add/Remove swap it atomically, so this scoring
	// pass sees one consistent index even across a concurrent rebuild.
	nameScores := m.index.Load().scoreAll(words)

	m.mu.Lock()

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
		// AddressOnly agents are reachable only by an explicit name match; no
		// ambient heuristic may route to them (CONTEXT.md "Address-Only").
		if a.AddressOnly && nameScores[a.index] < nameThreshold {
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

	// Highest score wins; ties break by Agent order so the result is stable.
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].total > hits[j].total })

	// Cap the published set before building it, so lastAddressed records only
	// the Agents actually addressed: naming two NPCs under the single-target
	// default fires one turn on the top-scored, not two.
	if m.maxTargets >= 0 && len(hits) > m.maxTargets {
		hits = hits[:m.maxTargets]
	}

	addressed := make(map[string]bool, len(hits))
	out := make([]voiceevent.AddressRouted, 0, len(hits))
	for _, h := range hits {
		addressed[h.agent.Target.AgentID] = true
		out = append(out, voiceevent.AddressRouted{
			At:     now,
			Text:   text,
			Target: h.agent.Target,
		})
	}
	// Record this turn's addressees for next turn's continuation heuristic.
	// Stay put on a no-target turn rather than forgetting who held the floor.
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

// nonAddressableCount returns how many Agents are not AddressOnly. Every Agent
// the matcher was built with is considered active for the lifetime of the
// Voice Session.
func (m *Matcher) nonAddressableCount() int {
	n := 0
	for _, a := range m.agents {
		if !a.AddressOnly {
			n++
		}
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
