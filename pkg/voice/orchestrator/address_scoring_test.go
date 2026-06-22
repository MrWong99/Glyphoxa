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
// Bart and the Goblin are Character NPCs. Bart carries tavern Expertise so the
// expert-on-word heuristic has something to bite on.
func scoringAgents() (butlerAg, bartAg, goblinAg address.Agent) {
	butlerAg = address.Agent{Target: butlerTarget, AddressOnly: true}
	bartAg = address.Agent{Target: bartTarget, Expertise: []string{"special", "tavern", "ale"}}
	goblinAg = address.Agent{Target: goblinTarget}
	return
}

// newScoringDetector wires the scoring matcher (English) into a detector for
// the given agents, the production-shaped alternative to the default whole-word
// matcher.
func newScoringDetector(agents ...address.Agent) *orchestrator.AddressDetector {
	m := address.NewMatcher(address.Config{Language: "en"}, agents...)
	return orchestrator.NewAddressDetector(m)
}

// newEnsembleDetector is newScoringDetector with the single-target default cap
// lifted (MaxTargets: -1), so a multi-NPC utterance yields the full Ensemble
// Turn set the detector must forward verbatim.
func newEnsembleDetector(agents ...address.Agent) *orchestrator.AddressDetector {
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
// straight onto the bus (no cassette needed) to show the payoff over the
// default matcher: "bard" is heard for "Bart", which the default whole-word
// matcher would miss and silently route to the Butler, while the scoring
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

// TestScoringMatcher_Ensemble_PublishesEveryTarget proves the detector
// publishes every decision the scoring matcher returns: with the single-target
// cap lifted (an ensemble matcher), an utterance naming two NPCs yields two
// address.routed events (an Ensemble Turn, ADR-0025), not one.
func TestScoringMatcher_Ensemble_PublishesEveryTarget(t *testing.T) {
	h := voicetest.New(t)
	butlerAg, bartAg, goblinAg := scoringAgents()
	d := newEnsembleDetector(butlerAg, bartAg, goblinAg)
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "Bart and the Goblin start arguing"})

	voicetest.AssertEventCount[voiceevent.AddressRouted](t, h, 2)
	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool { return e.Target.AgentID == bartTarget.AgentID },
		"address.routed → Bart",
	)
	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool { return e.Target.AgentID == goblinTarget.AgentID },
		"address.routed → Goblin",
	)
}
