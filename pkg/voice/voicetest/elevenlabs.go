package voicetest

import (
	"encoding/json"
	"os"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// LiveElevenLabsVoiceIDEnv names the environment variable that overrides the
// default ElevenLabs voice used by [LiveElevenLabsVoice]. Useful when a
// reviewer wants the record run to exercise their own voice library entry
// instead of the bundled public preset.
const LiveElevenLabsVoiceIDEnv = "GLYPHOXA_TEST_ELEVENLABS_VOICE_ID"

// defaultLiveElevenLabsVoiceID is the ElevenLabs "George" public preset —
// account-independent, available to every API key, used as the safe default
// so the documented `go test -tags=record` workflow succeeds against any
// valid ElevenLabs account without prior voice-library setup.
const defaultLiveElevenLabsVoiceID = "JBFqnCBsd6RMkjVDRZzb"

// LiveElevenLabsVoice returns a [tts.Voice] suitable for tests that go live
// against the ElevenLabs API under `-tags=record`. The VoiceID resolves from
// [LiveElevenLabsVoiceIDEnv] when set, otherwise [defaultLiveElevenLabsVoiceID].
//
// Settings is pre-populated with [elevenlabs.DefaultV3Settings] so the
// JSON-round-trip path is exercised in record mode (and not just in unit
// tests). In default replay mode VoiceID is unobserved (TTS cassettes pin
// sentences only per ADR-0021), so the helper is safe to use unconditionally.
//
// The env var is read on every call so table-driven tests can use t.Setenv
// without coordination.
func LiveElevenLabsVoice() tts.Voice {
	id := os.Getenv(LiveElevenLabsVoiceIDEnv)
	if id == "" {
		id = defaultLiveElevenLabsVoiceID
	}
	settings, _ := json.Marshal(elevenlabs.DefaultV3Settings())
	return tts.Voice{
		ProviderID: elevenlabs.ProviderID,
		VoiceID:    id,
		Settings:   settings,
	}
}
