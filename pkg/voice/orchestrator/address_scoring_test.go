package orchestrator_test

import (
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// scoringAgents mirrors the TB7/TB8 fixtures as the richer [address.Agent]s the
// scoring matcher consumes: the Butler is AddressOnly (reachable only by name),
// Bart and the Goblin are Character NPCs.
func scoringAgents() (butlerAg, bartAg, goblinAg address.Agent) {
	butlerAg = address.Agent{Target: butlerTarget, AddressOnly: true}
	bartAg = address.Agent{Target: bartTarget}
	goblinAg = address.Agent{Target: goblinTarget}
	return
}

// newScoringDetector wires the scoring matcher (English) into a detector for
// the given agents, the production-shaped matcher.
func newScoringDetector(agents ...address.Agent) *orchestrator.AddressDetector {
	m := address.NewMatcher(address.Config{Language: "en"}, agents...)
	return orchestrator.NewAddressDetector(m)
}

// newUncappedDetector is newScoringDetector with the single-target default cap
// lifted (MaxTargets: -1), so a multi-NPC utterance makes the matcher return its
// full score-sorted set and the detector has to pick from it.
func newUncappedDetector(agents ...address.Agent) *orchestrator.AddressDetector {
	m := address.NewMatcher(address.Config{Language: "en", MaxTargets: -1}, agents...)
	return orchestrator.NewAddressDetector(m)
}

// TestScoringMatcher_HelloTest_RoutesToButler is TB7 through the scoring
// matcher: the hello-test clip ("Glyphoxa, roll a perception check for me")
// names the Butler, so the cassette-replayed transcript must route there. It
// proves the scoring matcher drops into the same STTFinal → AddressRouted seam
// as the default and matches the Butler's name fuzzily (the default only ever
// fell back to the Butler — it never matched its name).
func TestScoringMatcher_HelloTest_RoutesToButler(t *testing.T) {
	h := voicetest.New(t)
	butlerAg, bartAg, goblinAg := scoringAgents()
	d := newScoringDetector(butlerAg, bartAg, goblinAg)
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	transcribeClip(t, h, "hello-test", "stt-hello-test")

	want := voicetest.NormalizeTranscript(helloUtterance)
	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool {
			return voicetest.NormalizeTranscript(e.Text) == want &&
				e.Target.AgentID == butlerTarget.AgentID
		},
		"address.routed → Butler for utterance "+helloUtterance,
	)
}

// TestScoringMatcher_BartTest_RoutesToCharacterNPC is TB8 through the scoring
// matcher: the bart-test clip ("Bart, what's the special tonight?") names a
// Character NPC, so the replayed transcript routes to Bart rather than the
// Butler.
func TestScoringMatcher_BartTest_RoutesToCharacterNPC(t *testing.T) {
	h := voicetest.New(t)
	butlerAg, bartAg, goblinAg := scoringAgents()
	d := newScoringDetector(butlerAg, bartAg, goblinAg)
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	transcribeClip(t, h, "bart-test", "stt-bart-test")

	want := voicetest.NormalizeTranscript(bartUtterance)
	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool {
			return voicetest.NormalizeTranscript(e.Text) == want &&
				e.Target.AgentID == bartTarget.AgentID &&
				e.Target.AgentRole == "character"
		},
		"address.routed → Bart (character) for utterance "+bartUtterance,
	)
}

// TestScoringMatcher_Mishearing_RoutesToNPC drives a misheard transcript
// straight onto the bus (no cassette needed) to show the payoff of fuzzy
// matching: "bard" is heard for "Bart" in vocative position, and the scoring
// matcher recognizes the homophone and routes to Bart.
func TestScoringMatcher_Mishearing_RoutesToNPC(t *testing.T) {
	h := voicetest.New(t)
	butlerAg, bartAg, goblinAg := scoringAgents()
	d := newScoringDetector(butlerAg, bartAg, goblinAg)
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "bard, what is the special tonight?"})

	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool { return e.Target.AgentID == bartTarget.AgentID },
		"address.routed → Bart for the misheard 'bard'",
	)
	voicetest.AssertEventCount[voiceevent.AddressRouted](t, h, 1)
}

// TestScoringMatcher_UncappedSet_PublishesOnlyTopScored proves one utterance
// opens one turn even when the matcher's single-target cap is lifted (ADR-0038):
// an utterance naming two NPCs yields exactly ONE address.routed, for the
// matcher's top-scored target. Ensemble Turns, which would have answered the
// whole set, are not implemented (ADR-0025).
func TestScoringMatcher_UncappedSet_PublishesOnlyTopScored(t *testing.T) {
	h := voicetest.New(t)
	butlerAg, bartAg, goblinAg := scoringAgents()
	m := address.NewMatcher(address.Config{Language: "en", MaxTargets: -1}, butlerAg, bartAg, goblinAg)
	const utterance = "Bart and the Goblin start arguing"
	// The matcher is stateful only in its continuation context, which a second
	// identical named utterance does not change — so asking it directly first
	// tells us which target it ranks on top.
	set := m.TargetMatch(utterance)
	if len(set) != 2 {
		t.Fatalf("uncapped matcher returned %d decisions for two named NPCs, want 2", len(set))
	}
	t.Cleanup(orchestrator.NewAddressDetector(m).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: utterance, TurnID: "T-e"})

	voicetest.AssertEventCount[voiceevent.AddressRouted](t, h, 1)
	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool {
			return e.TurnID == "T-e" && e.Target.AgentID == set[0].Target.AgentID
		},
		"address.routed → the matcher's top-scored target only",
	)
}

// TestScoringMatcher_SingleNameUncapped_PublishesPlainAddressRouted proves a
// single-target utterance is untouched by a lifted cap: one named NPC yields
// exactly one address.routed.
func TestScoringMatcher_SingleNameUncapped_PublishesPlainAddressRouted(t *testing.T) {
	h := voicetest.New(t)
	butlerAg, bartAg, goblinAg := scoringAgents()
	d := newUncappedDetector(butlerAg, bartAg, goblinAg)
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "Bart, what's the special tonight?", TurnID: "T-s"})

	voicetest.AssertEventCount[voiceevent.AddressRouted](t, h, 1)
	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool {
			return e.Target.AgentID == bartTarget.AgentID && e.TurnID == "T-s"
		},
		"address.routed → Bart (single-target, plain path)",
	)
}
