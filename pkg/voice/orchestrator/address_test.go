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
	butlerTarget = voiceevent.AddressTarget{AgentID: "butler", AgentRole: "butler", Name: "Glyphoxa"}
	bartTarget   = voiceevent.AddressTarget{AgentID: "npc-bart", AgentRole: "character", Name: "Bart"}
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
var goblinTarget = voiceevent.AddressTarget{AgentID: "npc-goblin", AgentRole: "character", Name: "Goblin"}

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

// TestAddressDetector_PublishesEveryDecision pins the multi-target half of the
// TargetMatcher contract: when one utterance addresses several Agents the
// matcher returns several decisions and the detector publishes each of them,
// not just the first.
func TestAddressDetector_PublishesEveryDecision(t *testing.T) {
	h := voicetest.New(t)
	m := matchFunc(func(text string) []voiceevent.AddressRouted {
		return []voiceevent.AddressRouted{
			{At: time.Now(), Text: text, Target: bartTarget},
			{At: time.Now(), Text: text, Target: goblinTarget},
		}
	})
	d := orchestrator.NewAddressDetector(m)
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "Bart and the Goblin start fighting."})

	voicetest.AssertEventCount[voiceevent.AddressRouted](t, h, 2)
	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool { return e.Target == bartTarget },
		"address.routed → Bart",
	)
	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool { return e.Target == goblinTarget },
		"address.routed → Goblin",
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
