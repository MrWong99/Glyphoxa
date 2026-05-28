package address

import (
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// agentAt is a test helper: an Agent with the given id at fuzzy-index position
// idx, so a hand-built DecisionContext's nameScores line up.
func agentAt(idx int, id string, opts func(*Agent)) Agent {
	a := Agent{Target: voiceevent.AddressTarget{AgentID: id, Name: id}, index: idx}
	if opts != nil {
		opts(&a)
	}
	return a
}

func TestNameMatch_FlatAboveThreshold(t *testing.T) {
	a := agentAt(0, "bart", nil)
	h := NameMatch{Weight: 1.0, Threshold: 0.6}

	below := &DecisionContext{nameScores: map[int]float64{0: 0.5}}
	if got := h.Score(a, below); got != 0 {
		t.Errorf("below threshold scored %v, want 0", got)
	}

	// A misheard-but-recognized name (0.9 homophone) earns the full flat Weight,
	// the same as a crisp 1.0 match — recognizing the name addresses the Agent.
	homophone := &DecisionContext{nameScores: map[int]float64{0: 0.9}}
	if got := h.Score(a, homophone); got != 1.0 {
		t.Errorf("homophone hit = %v, want flat Weight 1.0", got)
	}
	perfect := &DecisionContext{nameScores: map[int]float64{0: 1.0}}
	if got := h.Score(a, perfect); got != 1.0 {
		t.Errorf("perfect match = %v, want 1.0", got)
	}
}

// TestAmbientHeuristics_SuppressedByNameMatch pins the short-circuit: when any
// Agent is explicitly named this turn, the ambient heuristics contribute
// nothing, so a name match can never be widened by continuation, interruption,
// expertise, or the lone-NPC fallback.
func TestAmbientHeuristics_SuppressedByNameMatch(t *testing.T) {
	now := time.Now()
	a := agentAt(0, "bart", func(a *Agent) { a.Expertise = []string{"ale"} })
	dc := &DecisionContext{
		Now:            now,
		anyNameMatched: true,
		window:         time.Minute,
		lastAddressed:  map[string]bool{"bart": true},
		interruptions:  map[string]time.Time{"bart": now},
		recentWords:    map[string]struct{}{"ale": {}},
		nonAddressable: 1,
	}
	for _, h := range []Heuristic{
		LastAddressed{Weight: 1},
		RecentlyInterrupted{Weight: 1, Within: time.Minute},
		ExpertOnRecentWord{Weight: 1},
		SoleActiveNPC{Weight: 1},
	} {
		if got := h.Score(a, dc); got != 0 {
			t.Errorf("%s fired despite a name match: %v, want 0", h.Name(), got)
		}
	}
}

func TestLastAddressed(t *testing.T) {
	a := agentAt(0, "bart", nil)
	h := LastAddressed{Weight: 0.6}

	yes := &DecisionContext{lastAddressed: map[string]bool{"bart": true}}
	if got := h.Score(a, yes); got != 0.6 {
		t.Errorf("continuation hit = %v, want 0.6", got)
	}
	no := &DecisionContext{lastAddressed: map[string]bool{"other": true}}
	if got := h.Score(a, no); got != 0 {
		t.Errorf("non-continuation = %v, want 0", got)
	}
}

func TestRecentlyInterrupted(t *testing.T) {
	now := time.Now()
	a := agentAt(0, "bart", nil)
	h := RecentlyInterrupted{Weight: 0.6, Within: 10 * time.Second}

	recent := &DecisionContext{
		Now:           now,
		window:        30 * time.Second,
		interruptions: map[string]time.Time{"bart": now.Add(-5 * time.Second)},
	}
	if got := h.Score(a, recent); got != 0.6 {
		t.Errorf("recent interruption = %v, want 0.6", got)
	}

	stale := &DecisionContext{
		Now:           now,
		window:        30 * time.Second,
		interruptions: map[string]time.Time{"bart": now.Add(-20 * time.Second)},
	}
	if got := h.Score(a, stale); got != 0 {
		t.Errorf("stale interruption = %v, want 0 (outside Within)", got)
	}

	none := &DecisionContext{Now: now, window: 30 * time.Second, interruptions: map[string]time.Time{}}
	if got := h.Score(a, none); got != 0 {
		t.Errorf("no interruption = %v, want 0", got)
	}
}

func TestExpertOnRecentWord(t *testing.T) {
	a := agentAt(0, "bart", func(a *Agent) { a.Expertise = []string{"tavern", "ale", "rooms"} })

	dc := &DecisionContext{recentWords: map[string]struct{}{"tavern": {}, "sword": {}}}
	if got := (ExpertOnRecentWord{Weight: 0.5}).Score(a, dc); got != 0.5 {
		t.Errorf("flat expertise hit = %v, want 0.5", got)
	}

	// PerWord scales by distinct keywords mentioned.
	dc2 := &DecisionContext{recentWords: map[string]struct{}{"tavern": {}, "ale": {}}}
	if got := (ExpertOnRecentWord{Weight: 0.5, PerWord: true}).Score(a, dc2); got != 1.0 {
		t.Errorf("per-word expertise (2 hits) = %v, want 1.0", got)
	}

	noHit := &DecisionContext{recentWords: map[string]struct{}{"sword": {}}}
	if got := (ExpertOnRecentWord{Weight: 0.5}).Score(a, noHit); got != 0 {
		t.Errorf("no expertise mention = %v, want 0", got)
	}
}

func TestSoleActiveNPC(t *testing.T) {
	npc := agentAt(0, "bart", nil) // AddressOnly false
	butler := agentAt(1, "butler", func(a *Agent) { a.AddressOnly = true })
	h := SoleActiveNPC{Weight: 0.6}

	sole := &DecisionContext{nonAddressable: 1}
	if got := h.Score(npc, sole); got != 0.6 {
		t.Errorf("sole NPC = %v, want 0.6", got)
	}
	// AddressOnly agents never get the fallback.
	if got := h.Score(butler, sole); got != 0 {
		t.Errorf("AddressOnly agent got fallback = %v, want 0", got)
	}
	// With two NPCs the fallback is ambiguous → nothing.
	if got := h.Score(npc, &DecisionContext{nonAddressable: 2}); got != 0 {
		t.Errorf("two-NPC fallback = %v, want 0", got)
	}
}
