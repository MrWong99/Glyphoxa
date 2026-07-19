package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicecassette"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// fixed Agent identities used by TB7/TB8. The Butler is the Tenant-scoped
// default route per CONTEXT.md; Bart stands in for a Character NPC active
// in the Voice Session.
var (
	butlerTarget = voiceevent.AddressTarget{AgentID: "butler", AgentRole: voiceevent.AgentRoleButler, Name: "Glyphoxa"}
	bartTarget   = voiceevent.AddressTarget{AgentID: "npc-bart", AgentRole: voiceevent.AgentRoleCharacter, Name: "Bart"}
)

// Canonical spoken text of each fixture clip — the meta.yaml script with its
// audio tags removed. ADR-0020 keeps meta.yaml documentation-only, so the
// ground truth lives here in Go. Transcript assertions compare against these
// after [voicetest.NormalizeTranscript], so a re-recorded cassette only fails
// when the transcribed words change, not when a provider tweaks punctuation.
const (
	helloUtterance = "Glyphoxa, roll a perception check for me."
	bartUtterance  = "Bart, what's the special tonight?"
)

// transcribeClip wires the STT cassette for clipName through the orchestrator
// STT stage attached to h.Bus. Used by TB7/TB8 to feed the bus a real
// STTFinal — the surface the AddressDetector subscribes to — without
// duplicating the framing boilerplate from TB5.
func transcribeClip(t *testing.T, h *voicetest.Harness, clipName, cassetteName string) {
	t.Helper()
	clip := voicetest.LoadClip(t, clipName)
	const frameMs = 32
	frames, tail := clip.FramesOf(t, clip.SampleRate*frameMs/1000)
	if tail != 0 {
		t.Logf("%s: trailing %d samples (%d ms) not frame-aligned; discarded",
			clipName, tail, tail*1000/clip.SampleRate)
	}
	stage := orchestrator.NewSTT(h.Bus, voicecassette.LoadSTT(t, cassetteName))
	if err := stage.Transcribe(context.Background(), frames); err != nil {
		t.Fatalf("orchestrator.STT.Transcribe(%q): %v", clipName, err)
	}
}

// TestAddressDetector_HelloTest_RoutesToButler is TB7: a GM utterance with
// no Character NPC named must route to the Butler (CONTEXT.md "Address
// Detection"). The hello-test clip's transcript ("Glyphoxa, roll a
// perception check for me") names the Butler — the detector still routes
// there because no Character NPC matched, not because it special-cased the
// Butler's display Name.
//
// Wires STT cassette → bus → AddressDetector → AddressRouted; the
// detector's coupling is the STTFinal subscription, not an imperative call.
func TestAddressDetector_HelloTest_RoutesToButler(t *testing.T) {
	h := voicetest.New(t)
	d := orchestrator.NewAddressDetector(address.NewWholeWordMatcher(butlerTarget, []voiceevent.AddressTarget{bartTarget}))
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	transcribeClip(t, h, "hello-test", "stt-hello-test")

	want := voicetest.NormalizeTranscript(helloUtterance)
	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool {
			return voicetest.NormalizeTranscript(e.Text) == want &&
				e.Target.AgentRole == "butler" &&
				e.Target.AgentID == butlerTarget.AgentID
		},
		"address.routed → Butler for utterance "+helloUtterance,
	)
}

// TestAddressDetector_BartTest_RoutesToCharacterNPC is TB8: the same surface
// as TB7 with a different routing outcome. The bart-test clip's transcript
// ("Bart, what's the special tonight?") names a Character NPC active in
// the Voice Session, so the detector must select that NPC instead of the
// default Butler route.
func TestAddressDetector_BartTest_RoutesToCharacterNPC(t *testing.T) {
	h := voicetest.New(t)
	d := orchestrator.NewAddressDetector(address.NewWholeWordMatcher(butlerTarget, []voiceevent.AddressTarget{bartTarget}))
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	transcribeClip(t, h, "bart-test", "stt-bart-test")

	want := voicetest.NormalizeTranscript(bartUtterance)
	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool {
			return voicetest.NormalizeTranscript(e.Text) == want &&
				e.Target.AgentRole == "character" &&
				e.Target.AgentID == bartTarget.AgentID &&
				e.Target.Name == bartTarget.Name
		},
		"address.routed → Bart (character) for utterance "+bartUtterance,
	)
}

// matchFunc adapts a plain function to [orchestrator.TargetMatcher]. Used by the
// custom-matcher tests to stand in an algorithm whose output is fully under the
// test's control.
type matchFunc func(text string) []voiceevent.AddressRouted

func (f matchFunc) TargetMatch(text string) []voiceevent.AddressRouted { return f(text) }

// goblinTarget is a Character NPC used only by the custom-matcher tests, to
// keep them independent of the default matcher's name-matching fixtures.
var goblinTarget = voiceevent.AddressTarget{AgentID: "npc-goblin", AgentRole: voiceevent.AgentRoleCharacter, Name: "Goblin"}

// TestAddressDetector_PublishesDecisionVerbatim proves the detector publishes
// its matcher's [voiceevent.AddressRouted] unchanged — including its Text. The
// matcher returns a Text that differs from the STTFinal it was handed, so a
// passing assertion can only mean the detector forwarded the matcher's event
// rather than re-stamping it from the transcript.
func TestAddressDetector_PublishesDecisionVerbatim(t *testing.T) {
	h := voicetest.New(t)
	decided := voiceevent.AddressRouted{
		At:     time.Now(),
		Text:   "decided-by-matcher",
		Target: goblinTarget,
	}
	m := matchFunc(func(string) []voiceevent.AddressRouted {
		return []voiceevent.AddressRouted{decided}
	})
	d := orchestrator.NewAddressDetector(m)
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "irrelevant transcript"})

	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool {
			return e.Text == decided.Text && e.Target == goblinTarget
		},
		"address.routed published verbatim from custom matcher",
	)
}

// TestAddressDetector_CopiesSpeakerIDOntoRouted pins the SpeakerID carry (the
// transcript-names seam): the detector stamps the STTFinal's Speaker Lane
// attribution (ADR-0050) onto the published [voiceevent.AddressRouted] exactly
// like the TurnID — the matcher knows nothing of either — so the Agent loop can
// attribute the utterance to its human speaker.
func TestAddressDetector_CopiesSpeakerIDOntoRouted(t *testing.T) {
	h := voicetest.New(t)
	m := matchFunc(func(text string) []voiceevent.AddressRouted {
		return []voiceevent.AddressRouted{{At: time.Now(), Text: text, Target: goblinTarget}}
	})
	d := orchestrator.NewAddressDetector(m)
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "Goblin!", TurnID: "T-spk", SpeakerID: "spk-42"})

	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool {
			return e.SpeakerID == "spk-42" && e.TurnID == "T-spk"
		},
		"address.routed carrying the STTFinal's SpeakerID",
	)
}

// TestAddressDetector_MultiDecisionPublishesOneEnsembleRouted pins the
// multi-target half of the TargetMatcher contract under the Ensemble Turn design
// (ADR-0025, #301): when the matcher returns several decisions the detector makes
// the set ATOMIC — ONE address.ensemble carrying every target in the matcher's
// order — rather than N independent address.routed. This REWRITES the former
// TestAddressDetector_PublishesEveryDecision (which asserted N address.routed): the
// #301 contract supersedes it, since an Ensemble Turn is one floor-holding unit.
func TestAddressDetector_MultiDecisionPublishesOneEnsembleRouted(t *testing.T) {
	h := voicetest.New(t)
	m := matchFunc(func(text string) []voiceevent.AddressRouted {
		return []voiceevent.AddressRouted{
			{At: time.Now(), Text: text, Target: bartTarget},
			{At: time.Now(), Text: text, Target: goblinTarget},
		}
	})
	d := orchestrator.NewAddressDetector(m)
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "Bart and the Goblin start fighting.", TurnID: "T-m", SpeakerID: "spk-9"})

	voicetest.AssertEventCount[voiceevent.EnsembleRouted](t, h, 1)
	voicetest.AssertEventCount[voiceevent.AddressRouted](t, h, 0)
	voicetest.AssertEvent(t, h,
		func(e voiceevent.EnsembleRouted) bool {
			return len(e.Targets) == 2 && e.Targets[0] == bartTarget && e.Targets[1] == goblinTarget &&
				e.TurnID == "T-m" && e.Text == "Bart and the Goblin start fighting." &&
				e.SpeakerID == "spk-9"
		},
		"address.ensemble carrying both targets in matcher order plus the SpeakerID",
	)
}

// TestAddressDetector_EmptyResultPublishesNothing pins the empty half of the
// contract: a matcher that addresses no one returns an empty slice and the
// detector stays silent rather than inventing a fallback route. The
// Butler-fallback behaviour belongs to the matcher, not the detector.
func TestAddressDetector_EmptyResultPublishesNothing(t *testing.T) {
	h := voicetest.New(t)
	m := matchFunc(func(string) []voiceevent.AddressRouted { return nil })
	d := orchestrator.NewAddressDetector(m)
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "nobody is being addressed here"})

	voicetest.AssertNoEvent[voiceevent.AddressRouted](t, h)
}

// gmAllow adapts an allowlist set to the WithButlerGMGate predicate. Empty
// SpeakerIDs are never members — the gate fails closed on unattributed lanes.
func gmAllow(ids ...string) func(string) bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return func(id string) bool { return id != "" && set[id] }
}

// butlerMatcher stands in an algorithm that always routes to the Butler, so the
// gate tests exercise the AgentRole == "butler" branch under full control.
func butlerMatcher() matchFunc {
	return func(text string) []voiceevent.AddressRouted {
		return []voiceevent.AddressRouted{{At: time.Now(), Text: text, Target: butlerTarget}}
	}
}

// TestAddressDetector_ButlerGMGate_AllowlistedPublishes is the gate's allow
// case (ADR-0024/ADR-0050): a Butler-addressed utterance from an allowlisted
// SpeakerID reaches the Butler unchanged.
func TestAddressDetector_ButlerGMGate_AllowlistedPublishes(t *testing.T) {
	h := voicetest.New(t)
	d := orchestrator.NewAddressDetector(butlerMatcher(),
		orchestrator.WithButlerGMGate(gmAllow("gm-snowflake")))
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "Glyphoxa, help.", SpeakerID: "gm-snowflake"})

	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool { return e.Target == butlerTarget },
		"address.routed → Butler for allowlisted SpeakerID",
	)
}

// TestAddressDetector_ButlerGMGate_NonAllowlistedDropped is the gate's deny
// case: a Butler-addressed utterance from a SpeakerID outside the operator
// allowlist routes nowhere (fail closed, no matcher re-invocation).
func TestAddressDetector_ButlerGMGate_NonAllowlistedDropped(t *testing.T) {
	h := voicetest.New(t)
	d := orchestrator.NewAddressDetector(butlerMatcher(),
		orchestrator.WithButlerGMGate(gmAllow("gm-snowflake")))
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "Glyphoxa, help.", SpeakerID: "player-snowflake"})

	voicetest.AssertNoEvent[voiceevent.AddressRouted](t, h)
}

// TestAddressDetector_ButlerGMGate_UnattributedDropped is the fail-closed edge:
// an empty SpeakerID (unattributed lane) is never a GM, so a Butler route is
// dropped.
func TestAddressDetector_ButlerGMGate_UnattributedDropped(t *testing.T) {
	h := voicetest.New(t)
	d := orchestrator.NewAddressDetector(butlerMatcher(),
		orchestrator.WithButlerGMGate(gmAllow("gm-snowflake")))
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "Glyphoxa, help.", SpeakerID: ""})

	voicetest.AssertNoEvent[voiceevent.AddressRouted](t, h)
}

// TestAddressDetector_ButlerGMGate_CharacterUntouched proves the gate is
// Butler-only: a Character NPC route publishes regardless of SpeakerID
// (including empty), so the gate never touches Character address behaviour.
func TestAddressDetector_ButlerGMGate_CharacterUntouched(t *testing.T) {
	h := voicetest.New(t)
	m := matchFunc(func(text string) []voiceevent.AddressRouted {
		return []voiceevent.AddressRouted{{At: time.Now(), Text: text, Target: bartTarget}}
	})
	d := orchestrator.NewAddressDetector(m,
		orchestrator.WithButlerGMGate(gmAllow("gm-snowflake")))
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "Bart, hi.", SpeakerID: "player-snowflake"})

	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool { return e.Target == bartTarget },
		"address.routed → Bart for non-GM SpeakerID (gate is Butler-only)",
	)
}

// speakerAwareMatchFunc adapts a two-arg function to the SpeakerID-aware matcher
// seam. It implements BOTH TargetMatcher and orchestrator.SpeakerAwareMatcher so
// a test can prove the detector prefers TargetMatchFrom and threads the
// STTFinal.SpeakerID.
type speakerAwareMatchFunc func(speakerID, text string) []voiceevent.AddressRouted

func (f speakerAwareMatchFunc) TargetMatch(text string) []voiceevent.AddressRouted {
	return f("", text)
}
func (f speakerAwareMatchFunc) TargetMatchFrom(speakerID, text string) []voiceevent.AddressRouted {
	return f(speakerID, text)
}

// TestAddressDetector_PrefersSpeakerAwareMatcher pins the #256 detector change: a
// matcher that implements SpeakerAwareMatcher is routed through TargetMatchFrom
// with the utterance's SpeakerID (so the matcher-side Butler gate can key off it),
// and the TurnID is still carried onto each decision.
func TestAddressDetector_PrefersSpeakerAwareMatcher(t *testing.T) {
	h := voicetest.New(t)
	var gotSpeaker string
	m := speakerAwareMatchFunc(func(speakerID, text string) []voiceevent.AddressRouted {
		gotSpeaker = speakerID
		return []voiceevent.AddressRouted{{At: time.Now(), Text: text, Target: goblinTarget}}
	})
	d := orchestrator.NewAddressDetector(m)
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "Goblin!", SpeakerID: "spk-7", TurnID: "turn-1"})

	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool { return e.Target == goblinTarget && e.TurnID == "turn-1" },
		"address.routed via TargetMatchFrom carries the TurnID",
	)
	if gotSpeaker != "spk-7" {
		t.Fatalf("TargetMatchFrom got SpeakerID %q, want spk-7", gotSpeaker)
	}
}

// TestAddressDetector_NilMatcher_Panics pins that the detector has no matching
// algorithm to fall back to: construction requires a matcher.
func TestAddressDetector_NilMatcher_Panics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewAddressDetector(nil) did not panic")
		}
	}()
	orchestrator.NewAddressDetector(nil)
}
