package voicetest_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// TestLiveElevenLabsVoice_DefaultsToGeorgePreset pins the public-preset default
// per the PR #2 architecture review: cassette re-record runs must work against
// a stock ElevenLabs account, so the default voice ID must be a public preset
// that exists in every account (George — JBFqnCBsd6RMkjVDRZzb).
func TestLiveElevenLabsVoice_DefaultsToGeorgePreset(t *testing.T) {
	t.Setenv("GLYPHOXA_TEST_ELEVENLABS_VOICE_ID", "")

	v := voicetest.LiveElevenLabsVoice()

	if v.ProviderID != elevenlabs.ProviderID {
		t.Fatalf("ProviderID = %q, want %q", v.ProviderID, elevenlabs.ProviderID)
	}
	const georgePreset = "JBFqnCBsd6RMkjVDRZzb"
	if v.VoiceID != georgePreset {
		t.Fatalf("VoiceID = %q, want %q (public preset)", v.VoiceID, georgePreset)
	}
	if len(v.Settings) == 0 {
		t.Fatalf("Settings is empty; want pre-populated DefaultV3Settings payload")
	}
	if !json.Valid(v.Settings) {
		t.Fatalf("Settings is not valid JSON: %q", string(v.Settings))
	}
}

// TestLiveElevenLabsVoice_EnvOverridesVoiceID pins the env-override contract.
// Tests that wire a cloned or paid voice must be able to flip the VoiceID via
// t.Setenv without ceremony — which requires the helper to read the env on
// every call, not cache it in TestMain.
func TestLiveElevenLabsVoice_EnvOverridesVoiceID(t *testing.T) {
	const custom = "custom-voice-id-from-env"
	t.Setenv("GLYPHOXA_TEST_ELEVENLABS_VOICE_ID", custom)

	v := voicetest.LiveElevenLabsVoice()

	if v.VoiceID != custom {
		t.Fatalf("VoiceID = %q, want %q (from env)", v.VoiceID, custom)
	}
}

// TestLiveElevenLabsVoice_SettingsCarriesDefaultV3Payload pins what the
// pre-populated Settings blob actually contains. The PR review's reason for
// pre-populating Settings (rather than leaving it nil) is to exercise the
// Voice.Settings → request-body JSON round-trip in record mode; that only
// works if the payload matches what production voices would carry.
func TestLiveElevenLabsVoice_SettingsCarriesDefaultV3Payload(t *testing.T) {
	v := voicetest.LiveElevenLabsVoice()

	var got elevenlabs.Settings
	if err := json.Unmarshal(v.Settings, &got); err != nil {
		t.Fatalf("decode Settings: %v", err)
	}
	want := elevenlabs.DefaultV3Settings()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Settings round-trip mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

// TestLiveElevenLabsVoice_EnvReadPerCall pins the "read on every call" half of
// the contract: a single test that flips the env between calls must observe
// the change, because table-driven tests rely on that.
func TestLiveElevenLabsVoice_EnvReadPerCall(t *testing.T) {
	t.Setenv("GLYPHOXA_TEST_ELEVENLABS_VOICE_ID", "first")
	first := voicetest.LiveElevenLabsVoice()

	t.Setenv("GLYPHOXA_TEST_ELEVENLABS_VOICE_ID", "second")
	second := voicetest.LiveElevenLabsVoice()

	if first.VoiceID != "first" {
		t.Fatalf("first VoiceID = %q, want %q", first.VoiceID, "first")
	}
	if second.VoiceID != "second" {
		t.Fatalf("second VoiceID = %q, want %q (env was re-read)", second.VoiceID, "second")
	}
}
