package wirenpc

import (
	"encoding/json"
	"testing"

	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// TestNPCVoice_Emits48kPCM pins the live NPC's TTS output format at pcm_48000 so
// the outbound codec path stays encode-only (Discord's Opus encoder runs at
// 48 kHz; matching the TTS rate avoids a resampler on the live demo). It also
// guards that the Settings blob round-trips as valid ElevenLabs Settings and
// keeps the conversational eleven_v3 model — a regression to the 24 kHz default
// (or a malformed blob) would silently re-introduce the resampler / break
// synthesis.
func TestNPCVoice_Emits48kPCM(t *testing.T) {
	v := npcVoice()
	if len(v.Settings) == 0 {
		t.Fatal("npcVoice has no Settings; output format would fall back to the 24 kHz default")
	}
	var s ttseleven.Settings
	if err := json.Unmarshal(v.Settings, &s); err != nil {
		t.Fatalf("Voice.Settings is not valid ElevenLabs Settings JSON: %v", err)
	}
	if s.OutputFormat != "pcm_48000" {
		t.Errorf("output_format = %q, want pcm_48000 (encode-only outbound path)", s.OutputFormat)
	}
	if s.ModelID != ttseleven.ModelV3 {
		t.Errorf("model_id = %q, want the conversational %q default to be preserved", s.ModelID, ttseleven.ModelV3)
	}
}

// TestNPCMatcher_RoutesNamedAndUnnamedToNPC pins the address-routing intent the
// live loop depends on: with one Character NPC and no Butler, BOTH a named
// utterance and an unnamed one must route to the NPC's Agent. This is the
// behaviorally load-bearing piece of the wiring — if it routed elsewhere (e.g.
// to an absent Butler), the NPC would be silent and the whole live loop would
// look broken on top of brand-new audio code. Keyless: no LLM, no Session.
func TestNPCMatcher_RoutesNamedAndUnnamedToNPC(t *testing.T) {
	npc := hardcodedNPC()
	m := npcMatcher(npc)

	cases := []struct {
		name string
		text string
	}{
		{"named", "Bart, do you have a room?"},
		{"alias", "Innkeeper, what's the news?"},
		{"unnamed single-NPC fallback", "Hello, is anyone here?"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routed := m.TargetMatch(tc.text)
			if len(routed) == 0 {
				t.Fatalf("utterance %q routed to nobody; the lone NPC must answer", tc.text)
			}
			// The lead (highest-scored) target must be the NPC; a stray Butler
			// or empty AgentID would leave the production ReplyFunc silent.
			if got := routed[0].Target.AgentID; got != npc.agentID {
				t.Errorf("utterance %q routed to AgentID %q, want %q", tc.text, got, npc.agentID)
			}
		})
	}
}

// TestNPCMatcher_Constructs guards the construction itself: the matcher must
// build without panicking (the agent carries a non-empty AgentID and a valid
// character role), catching a regression that would crash the binary at
// startup — the bug this test was written to prevent (an earlier draft passed
// an empty Butler to a matcher that panics on it).
func TestNPCMatcher_Constructs(t *testing.T) {
	if npcMatcher(hardcodedNPC()) == nil {
		t.Fatal("npcMatcher returned nil")
	}
}
