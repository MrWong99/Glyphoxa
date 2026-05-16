package elevenlabs

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ModelV3 is the canonical ElevenLabs v3 model identifier. v3 is the model
// family that introduced inline bracketed audio tags ("[whispers]",
// "[laughs]", "[pause]", …); see [Client.AudioMarkupPrompt].
const ModelV3 = "eleven_v3"

// DefaultOutputFormat is the streaming PCM format the adapter requests when a
// Voice does not specify one. 24 kHz mono int16 PCM is ElevenLabs's highest
// streaming-friendly PCM rate; the orchestrator's resampler downconverts to
// Discord's 48 kHz Opus pipeline.
const DefaultOutputFormat = "pcm_24000"

// PronunciationDictionaryLocator names one ElevenLabs pronunciation
// dictionary version to apply during a synthesis call. Up to 3 are supported
// per call by the vendor.
type PronunciationDictionaryLocator struct {
	PronunciationDictionaryID string `json:"pronunciation_dictionary_id"`
	VersionID                 string `json:"version_id,omitempty"`
}

// VoiceSettings is the inner voice_settings block of an ElevenLabs synthesis
// request. Pointer fields distinguish "absent — use vendor default" from
// "explicitly zero".
type VoiceSettings struct {
	Stability       *float64 `json:"stability,omitempty"`
	SimilarityBoost *float64 `json:"similarity_boost,omitempty"`
	Style           *float64 `json:"style,omitempty"`
	UseSpeakerBoost *bool    `json:"use_speaker_boost,omitempty"`
	Speed           *float64 `json:"speed,omitempty"`
}

// Settings is the provider-typed payload stored in [tts.Voice.Settings] (and
// optionally [tts.SynthesizeRequest.OverrideSettings]) for ElevenLabs voices.
// JSON tags match the ElevenLabs API where applicable so a single struct
// round-trips through Postgres jsonb (per ADR-0022) and through outbound
// request bodies.
//
// SuggestedAudioTags is a Glyphoxa-side hint surfaced by [Client.AudioMarkupPrompt]
// to bias the LLM toward a per-voice tag palette; the field shape matches the
// ElevenLabs conversational-agent schema for forward compatibility.
type Settings struct {
	ModelID                         string                           `json:"model_id,omitempty"`
	LanguageCode                    string                           `json:"language_code,omitempty"`
	OutputFormat                    string                           `json:"output_format,omitempty"`
	VoiceSettings                   *VoiceSettings                   `json:"voice_settings,omitempty"`
	Seed                            *int64                           `json:"seed,omitempty"`
	PronunciationDictionaryLocators []PronunciationDictionaryLocator `json:"pronunciation_dictionary_locators,omitempty"`
	SuggestedAudioTags              []string                         `json:"suggested_audio_tags,omitempty"`
}

// DefaultV3Settings returns the baseline eleven_v3 settings used as the
// pre-populated [tts.Voice.Settings] for voices returned by [Client.ListVoices]
// (and as the implicit defaults when a Voice arrives with an empty Settings
// blob). Tuned for conversational delivery: moderate stability/similarity,
// speaker-boost on, 24 kHz PCM.
func DefaultV3Settings() Settings {
	stability := 0.5
	similarity := 0.75
	boost := true
	return Settings{
		ModelID:      ModelV3,
		OutputFormat: DefaultOutputFormat,
		VoiceSettings: &VoiceSettings{
			Stability:       &stability,
			SimilarityBoost: &similarity,
			UseSpeakerBoost: &boost,
		},
	}
}

// mergeSettings produces the effective Settings for one call by decoding base
// (Voice.Settings) and then overlaying override (SynthesizeRequest.OverrideSettings).
// Fields present in override replace the corresponding field in base; absent
// fields are preserved. The merge is recursive for nested objects: an override
// of {"voice_settings":{"stability":0.9}} updates only Stability and leaves
// the other [VoiceSettings] fields from base intact, matching Go encoding/json's
// "unmarshal-into-existing-value" semantics.
func mergeSettings(base, override json.RawMessage) (Settings, error) {
	var s Settings
	if len(base) > 0 {
		if err := json.Unmarshal(base, &s); err != nil {
			return Settings{}, fmt.Errorf("elevenlabs: decode base Settings: %w", err)
		}
	}
	if len(override) > 0 {
		if err := json.Unmarshal(override, &s); err != nil {
			return Settings{}, fmt.Errorf("elevenlabs: decode override Settings: %w", err)
		}
	}
	return s, nil
}

// sampleRateFromOutputFormat parses an ElevenLabs output_format string into a
// PCM sample rate. Returns 0 for non-PCM formats — callers must reject those
// because the orchestrator's resampler is wired against PCM input only.
func sampleRateFromOutputFormat(f string) int {
	if !strings.HasPrefix(f, "pcm_") {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(f, "pcm_"))
	if err != nil {
		return 0
	}
	return n
}
