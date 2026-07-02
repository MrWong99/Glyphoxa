package address_test

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Fixed Agents reused across the matcher tests. The Butler is AddressOnly (the
// CONTEXT.md default), so only an explicit name reaches it; Bart and the Goblin
// are Character NPCs reachable by the ambient heuristics too. Bart carries
// tavern Expertise for the expert-on-recent-word cases.
var (
	butler = address.Agent{
		Target:      voiceevent.AddressTarget{AgentID: "butler", AgentRole: "butler", Name: "Glyphoxa"},
		AddressOnly: true,
	}
	bart = address.Agent{
		Target:    voiceevent.AddressTarget{AgentID: "npc-bart", AgentRole: "character", Name: "Bart"},
		Expertise: []string{"tavern", "ale"},
	}
	goblin = address.Agent{
		Target: voiceevent.AddressTarget{AgentID: "npc-goblin", AgentRole: "character", Name: "Goblin"},
	}
	// dwarf is a spare Character NPC used to keep the lone-NPC fallback inert in
	// roster tests, so an unnamed turn routing somewhere proves continuation
	// rather than the single-NPC fallback.
	dwarf = address.Agent{
		Target: voiceevent.AddressTarget{AgentID: "npc-dwarf", AgentRole: "character", Name: "Durin"},
	}
)

// fakeClock is a manually advanced clock so the recency windows are driven
// deterministically rather than by wall time.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// routedIDs extracts the addressed AgentIDs from a decision set, in order.
func routedIDs(routed []voiceevent.AddressRouted) []string {
	ids := make([]string, len(routed))
	for i, r := range routed {
		ids[i] = r.Target.AgentID
	}
	return ids
}

// assertIDs fails unless the decision set's AgentIDs equal want exactly (order
// included).
func assertIDs(t *testing.T, routed []voiceevent.AddressRouted, want ...string) {
	t.Helper()
	got := routedIDs(routed)
	if len(got) != len(want) {
		t.Fatalf("addressed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("addressed %v, want %v", got, want)
		}
	}
}

func TestMatcher_ExplicitNameRoutesToNPC(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, goblin)
	assertIDs(t, m.TargetMatch("Bart, what's the special tonight?"), "npc-bart")
}

// TestMatcher_FuzzyMishearing is the headline of ADR-0024: the STT hears "bard"
// for "Bart", and the matcher still routes to Bart via the phonetic tier.
func TestMatcher_FuzzyMishearing(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, goblin)
	assertIDs(t, m.TargetMatch("bard, what is the special tonight?"), "npc-bart")
}

// TestMatcher_ButlerIsAddressOnly proves the AddressOnly gate both ways: an
// utterance that names the Butler reaches it, while an ambient utterance in the
// same session never does — even though every fallback would otherwise apply.
func TestMatcher_ButlerIsAddressOnly(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, goblin)

	assertIDs(t, m.TargetMatch("Glyphoxa, roll a perception check for me."), "butler")

	// An unrelated utterance with no name reaches nobody — the Butler is
	// excluded from the fallbacks and the two NPCs make the lone-NPC fallback
	// inapplicable.
	assertIDs(t, m.TargetMatch("let us all roll for initiative"))
}

// TestMatcher_NameMatchSuppressesFallback guards the short-circuit end to end:
// in a lone-NPC session, naming the Butler must address only the Butler, never
// also the NPC the single-NPC fallback would otherwise grab.
func TestMatcher_NameMatchSuppressesFallback(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart)
	assertIDs(t, m.TargetMatch("Glyphoxa, give me a recap."), "butler")
}

// TestMatcher_LastAddressedContinuation covers the continuation heuristic: a
// follow-up with no fresh name stays with the previously addressed NPC, even in
// a multi-NPC session where the lone-NPC fallback cannot fire.
func TestMatcher_LastAddressedContinuation(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, goblin)

	assertIDs(t, m.TargetMatch("Bart, pour me a drink."), "npc-bart")
	assertIDs(t, m.TargetMatch("and what else do you have?"), "npc-bart")
}

// TestMatcher_SoleActiveNPCFallback covers the lone-NPC fallback: with exactly
// one Character NPC active, an unnamed utterance routes to it.
func TestMatcher_SoleActiveNPCFallback(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart)
	assertIDs(t, m.TargetMatch("so what happens next?"), "npc-bart")
}

// TestMatcher_RecentlyInterruptedRecovery covers the interruption heuristic:
// after a participant barges in on the Goblin, the next unnamed utterance lands
// back on the Goblin — not on Bart — even with two NPCs active and no prior
// addressee.
func TestMatcher_RecentlyInterruptedRecovery(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	m := address.NewMatcher(address.Config{Language: "en", Clock: clk.now}, butler, bart, goblin)

	m.NoteInterruption("npc-goblin")
	clk.advance(3 * time.Second)
	assertIDs(t, m.TargetMatch("no wait, let me finish"), "npc-goblin")

	// Past the default 15s window the interruption no longer pulls focus. A
	// fresh matcher isolates this from the continuation the match above would
	// otherwise leave behind (the Goblin became the last addressee).
	stale := &fakeClock{t: time.Unix(1000, 0)}
	m2 := address.NewMatcher(address.Config{Language: "en", Clock: stale.now}, butler, bart, goblin)
	m2.NoteInterruption("npc-goblin")
	stale.advance(30 * time.Second)
	assertIDs(t, m2.TargetMatch("anyway, moving on"))
}

// TestMatcher_ExpertiseAloneIsInsufficient pins the weak-hint design: mentioning
// a word an NPC is expert on is not enough to address it by itself (0.5 < 1.0),
// so topic drift does not hijack routing.
func TestMatcher_ExpertiseAloneIsInsufficient(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, goblin)
	assertIDs(t, m.TargetMatch("the tavern was burned down years ago"))
}

// TestMatcher_CustomHeuristicStack proves the stack is pluggable: a matcher
// configured with only an (up-weighted) expert-on-word heuristic addresses the
// expert NPC on a topic mention alone.
func TestMatcher_CustomHeuristicStack(t *testing.T) {
	m := address.NewMatcher(address.Config{
		Language:   "en",
		Heuristics: []address.Heuristic{address.ExpertOnRecentWord{Weight: 1.0}},
	}, butler, bart, goblin)
	assertIDs(t, m.TargetMatch("pour me an ale would you"), "npc-bart")
}

// TestMatcher_EmptyHeuristicStackNeverAddresses pins that an explicitly empty
// (non-nil) stack disables routing — distinct from a nil stack, which defaults.
func TestMatcher_EmptyHeuristicStackNeverAddresses(t *testing.T) {
	m := address.NewMatcher(address.Config{
		Language:   "en",
		Heuristics: []address.Heuristic{},
	}, butler, bart)
	assertIDs(t, m.TargetMatch("Bart!"))
}

// TestMatcher_MultiTargetEnsemble covers the multi-target half of the contract:
// one utterance naming two NPCs addresses both (an Ensemble Turn, ADR-0025).
// MaxTargets: -1 lifts the single-target default cap so the full named set is
// published — the proof that ensemble routing stays reachable.
func TestMatcher_MultiTargetEnsemble(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en", MaxTargets: -1}, butler, bart, goblin)
	got := m.TargetMatch("Bart and the Goblin start arguing")
	assertIDs(t, got, "npc-bart", "npc-goblin")
}

// TestMatcher_SingleTargetByDefault pins the default cap: naming two NPCs in one
// utterance addresses only the top-scored one (ties break by Agent order), so a
// default-config matcher fires a single turn rather than two.
func TestMatcher_SingleTargetByDefault(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, goblin)
	assertIDs(t, m.TargetMatch("Bart and the Goblin start arguing"), "npc-bart")
}

// TestMatcher_MaxTargetsCap proves the cap is configurable: MaxTargets: 2 keeps
// the top two of a larger named set, while MaxTargets: -1 keeps them all.
func TestMatcher_MaxTargetsCap(t *testing.T) {
	const utter = "Bart, the Goblin, and Glyphoxa all turn to look"

	capped := address.NewMatcher(address.Config{Language: "en", MaxTargets: 2}, butler, bart, goblin)
	if got := routedIDs(capped.TargetMatch(utter)); len(got) != 2 {
		t.Fatalf("MaxTargets: 2 addressed %v, want 2", got)
	}

	unlimited := address.NewMatcher(address.Config{Language: "en", MaxTargets: -1}, butler, bart, goblin)
	if got := routedIDs(unlimited.TargetMatch(utter)); len(got) != 3 {
		t.Fatalf("MaxTargets: -1 addressed %v, want 3", got)
	}
}

// TestMatcher_AddAgent proves an Agent added after construction is addressable:
// before the Add naming the Goblin routes to nobody (two other NPCs keep the
// lone-NPC fallback inert), after it the new NPC is reached by name.
func TestMatcher_AddAgent(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, dwarf)
	assertIDs(t, m.TargetMatch("Goblin, what are you plotting?"))

	m.Add(goblin)
	assertIDs(t, m.TargetMatch("Goblin, what are you plotting?"), "npc-goblin")
}

// TestMatcher_RemoveAgent proves a removed Agent stops being addressed and that
// its continuation state is pruned: after the Goblin holds the floor it is
// removed, and the follow-up that would otherwise continue to it routes nowhere.
// Bart and the Dwarf keep the lone-NPC fallback inert throughout, so the only
// thing that could route an unnamed turn to the Goblin is stale continuation.
func TestMatcher_RemoveAgent(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en"}, butler, bart, dwarf, goblin)

	// The Goblin becomes the last addressee, so continuation would normally
	// keep the floor on it.
	assertIDs(t, m.TargetMatch("Goblin, hold the line."), "npc-goblin")

	m.Remove("npc-goblin")

	// Named again, the Goblin is gone, so nobody is addressed...
	assertIDs(t, m.TargetMatch("Goblin, hold the line."))
	// ...and the continuation state was pruned, so an unnamed follow-up does not
	// resurrect it: it routes to nobody rather than continuing to the departed
	// Goblin.
	assertIDs(t, m.TargetMatch("anyway, carry on"))
}

// TestMatcher_Add_Panics pins Add's validation: an empty AgentID and a duplicate
// AgentID are both wiring errors that must panic rather than corrupt the roster.
func TestMatcher_Add_Panics(t *testing.T) {
	assertPanics(t, "empty AgentID", func() {
		m := address.NewMatcher(address.Config{Language: "en"}, butler, bart)
		m.Add(address.Agent{Target: voiceevent.AddressTarget{AgentRole: "character", Name: "Nameless"}})
	})
	assertPanics(t, "duplicate AgentID", func() {
		m := address.NewMatcher(address.Config{Language: "en"}, butler, bart)
		m.Add(address.Agent{Target: voiceevent.AddressTarget{AgentID: "npc-bart", AgentRole: "character", Name: "Bartholomew"}})
	})
}

// TestMatcher_TextEchoedVerbatim proves each decision carries the utterance text
// and the decision time from the matcher's clock, satisfying the AddressRouted
// contract the detector publishes verbatim.
func TestMatcher_TextEchoedVerbatim(t *testing.T) {
	clk := &fakeClock{t: time.Unix(5000, 0)}
	m := address.NewMatcher(address.Config{Language: "en", Clock: clk.now}, butler, bart)
	const utter = "Bart, hello there"
	got := m.TargetMatch(utter)
	if len(got) != 1 {
		t.Fatalf("got %d decisions, want 1", len(got))
	}
	if got[0].Text != utter {
		t.Errorf("Text = %q, want %q", got[0].Text, utter)
	}
	if !got[0].At.Equal(clk.now()) {
		t.Errorf("At = %v, want clock time %v", got[0].At, clk.now())
	}
}

// TestMatcher_CustomThreshold proves the address threshold is configurable: at
// a threshold above any single ambient weight, the lone-NPC fallback alone no
// longer addresses, but combining it with an interruption (1.0 + 1.0) does.
func TestMatcher_CustomThreshold(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	m := address.NewMatcher(address.Config{
		Language:         "en",
		AddressThreshold: 1.5,
		Clock:            clk.now,
	}, butler, bart)

	assertIDs(t, m.TargetMatch("what now?")) // sole-NPC 1.0 < 1.5 → nobody

	m.NoteInterruption("npc-bart")                        // +1.0
	assertIDs(t, m.TargetMatch("go on then"), "npc-bart") // 2.0 ≥ 1.5
}

// TestMatcher_UnregisteredLanguageUsesEditNet proves a language with no
// phonetic encoder still matches via the Damerau-Levenshtein net alone
// (ADR-0024), so an unknown Campaign Language degrades rather than failing.
func TestMatcher_UnregisteredLanguageUsesEditNet(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "xx"}, butler, bart, goblin)
	// "goblan" is one edit from "goblin": no phonetic encoder, but within the net.
	assertIDs(t, m.TargetMatch("the goblan lunges at you"), "npc-goblin")
}

func TestNewMatcher_Panics(t *testing.T) {
	assertPanics(t, "no agents", func() { address.NewMatcher(address.Config{}) })
	assertPanics(t, "empty AgentID", func() {
		address.NewMatcher(address.Config{}, address.Agent{
			Target: voiceevent.AddressTarget{AgentRole: "character", Name: "Nameless"},
		})
	})
}

func assertPanics(t *testing.T, desc string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected panic, got none", desc)
		}
	}()
	fn()
}

// TestMatcher_ConcurrentHeadRemoveNeverTransfersNameScore pins the snapshot
// consistency of one TargetMatch pass (#145): the fuzzy index and the roster it
// is scored against must be captured together. Removing the HEAD agent (Alice,
// index 0) mid-match reindexes the survivor (Bob) down into Alice's old slot;
// if the pass scored a pre-Remove index against the post-Remove roster, Bob
// would inherit Alice's perfect name score and answer an utterance that named
// a departed NPC. With only the NameMatch heuristic configured, the sole legal
// outcomes are "Alice" (pre-Remove snapshot) or nobody (post-Remove snapshot)
// — never Bob.
func TestMatcher_ConcurrentHeadRemoveNeverTransfersNameScore(t *testing.T) {
	alice := address.Agent{
		Target:  voiceevent.AddressTarget{AgentID: "npc-alice", AgentRole: "character", Name: "Alice"},
		Aliases: []string{"the herbalist", "keeper of the grove"},
	}
	bob := address.Agent{
		Target:  voiceevent.AddressTarget{AgentID: "npc-bob", AgentRole: "character", Name: "Bob"},
		Aliases: []string{"the blacksmith", "warden of the gate"},
	}
	// Padding words widen the scoring pass so a concurrent Remove has a real
	// window to land in.
	const utter = "alice would you kindly tell the whole table what really happened at the old stone bridge last night before the rain came down"

	for i := 0; i < 2000; i++ {
		m := address.NewMatcher(address.Config{
			Language:   "en",
			Heuristics: []address.Heuristic{address.NameMatch{Weight: 1.0, Threshold: 0.6}},
		}, alice, bob)

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Remove("npc-alice")
		}()
		got := m.TargetMatch(utter)
		wg.Wait()

		for _, r := range got {
			if r.Target.AgentID == "npc-bob" {
				t.Fatalf("iteration %d: concurrent head Remove transferred Alice's name score to Bob: routed %v", i, routedIDs(got))
			}
		}
	}
}

// TestMatcher_ConcurrentUse exercises the matcher's locking: many goroutines
// route, feed interruptions, and churn the roster at once. Run under
// `go test -race` it pins that the shared conversational state stays guarded and
// that a scoring pass sees a consistent index across a concurrent rebuild.
func TestMatcher_ConcurrentUse(t *testing.T) {
	m := address.NewMatcher(address.Config{Language: "en", MaxTargets: -1}, butler, bart)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		// Each iteration churns a uniquely-named NPC so concurrent Adds never
		// collide on a duplicate AgentID (which is a panic), letting roster
		// mutation race freely against routing and interruptions.
		id := fmt.Sprintf("npc-extra-%d", i)
		extra := address.Agent{Target: voiceevent.AddressTarget{AgentID: id, AgentRole: "character", Name: "Extra" + strconv.Itoa(i)}}
		wg.Add(4)
		go func() { defer wg.Done(); m.TargetMatch("Bart and the Goblin talk") }()
		go func() { defer wg.Done(); m.NoteInterruption("npc-bart") }()
		go func() { defer wg.Done(); m.Add(extra) }()
		go func() { defer wg.Done(); m.Remove(id) }()
	}
	wg.Wait()
}
