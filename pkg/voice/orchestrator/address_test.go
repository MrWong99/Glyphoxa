package orchestrator_test

import (
	"context"
	"testing"

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
// perception check for me.") names the Butler — the detector still routes
// there because no Character NPC matched, not because it special-cased the
// Butler's display Name.
//
// Wires STT cassette → bus → AddressDetector → AddressRouted; the
// detector's coupling is the STTFinal subscription, not an imperative call.
func TestAddressDetector_HelloTest_RoutesToButler(t *testing.T) {
	h := voicetest.New(t)
	d := orchestrator.NewAddressDetector(h.Bus, butlerTarget, []voiceevent.AddressTarget{bartTarget})
	t.Cleanup(d.Close)

	transcribeClip(t, h, "hello-test", "stt-hello-test")

	const wantText = "Glyphoxa, roll a perception check for me."
	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool {
			return e.Text == wantText &&
				e.Target.AgentRole == "butler" &&
				e.Target.AgentID == butlerTarget.AgentID
		},
		"address.routed → Butler for utterance "+wantText,
	)
}

// TestAddressDetector_BartTest_RoutesToCharacterNPC is TB8: the same surface
// as TB7 with a different routing outcome. The bart-test clip's transcript
// ("Bart, what's the special tonight?") names a Character NPC active in
// the Voice Session, so the detector must select that NPC instead of the
// default Butler route.
func TestAddressDetector_BartTest_RoutesToCharacterNPC(t *testing.T) {
	h := voicetest.New(t)
	d := orchestrator.NewAddressDetector(h.Bus, butlerTarget, []voiceevent.AddressTarget{bartTarget})
	t.Cleanup(d.Close)

	transcribeClip(t, h, "bart-test", "stt-bart-test")

	const wantText = "Bart, what's the special tonight?"
	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool {
			return e.Text == wantText &&
				e.Target.AgentRole == "character" &&
				e.Target.AgentID == bartTarget.AgentID &&
				e.Target.Name == bartTarget.Name
		},
		"address.routed → Bart (character) for utterance "+wantText,
	)
}
