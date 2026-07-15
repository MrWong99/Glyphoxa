package tts

import "encoding/json"

// AudioChunk is a provider-native window of synthesized audio with
// self-describing format. It is the pre-conversion envelope returned by a
// [Synthesizer]: per ADR-0022 providers emit chunks at their native sample
// rate and channel count with no duration or alignment constraint, so that
// providers can stay thin and not negotiate format out-of-band.
//
// AudioChunk is distinct from [audio.Frame] — Frame is the pipeline transport
// unit with strict mono + frame-alignment invariants. The orchestrator's
// (not yet implemented) playback aligner resamples, mono-mixes, and
// frame-aligns AudioChunks into a stream of audio.Frames feeding the Opus
// encoder that drives Discord.
type AudioChunk struct {
	// PCM is signed 16-bit little-endian PCM data. Length is provider-defined.
	PCM []byte

	// SampleRate is the chunk's sample rate in Hz (e.g. 16000, 22050, 24000,
	// 48000).
	SampleRate int

	// Channels is the channel count: 1 for mono, 2 for stereo.
	Channels int

	// Err, when non-nil, marks this chunk as the stream's TERMINAL element: the
	// synthesis failed mid-stream and no further audio follows (#436). A terminal
	// error chunk carries no PCM and is emitted immediately before the channel
	// closes, so a drain can distinguish an abnormal termination from clean
	// completion (a close with no Err chunk) — previously indistinguishable, which
	// let a half-delivered sentence commit as fully spoken. Providers emit it only
	// under a live ctx: a ctx-cancelled stream (barge-in) closes WITHOUT one, since
	// the caller cut the stream deliberately.
	Err error
}

// Voice identifies a TTS voice and carries its persisted per-NPC tuning.
//
// ProviderID + VoiceID together name a unique voice across all providers.
// Settings is provider-typed JSON: each provider package defines its own
// settings struct (e.g. ElevenLabsSettings, OpenAISettings) and round-trips
// it through this field via [encoding/json]. The core tts package treats
// Settings as opaque.
type Voice struct {
	// ProviderID names the Provider implementation that owns this voice
	// (e.g. "elevenlabs", "openai"). Stringly-typed by design — see ADR-0022.
	ProviderID string

	// VoiceID is the vendor-side voice identifier (e.g. ElevenLabs voice_id,
	// OpenAI preset name).
	VoiceID string

	// Name is the human-readable display name used in UIs.
	Name string

	// Language is a BCP-47 language tag (e.g. "en-US"); optional. Some voices
	// are multilingual; callers should treat this as a hint, not a constraint.
	Language string

	// Settings carries provider-typed defaults for this voice. Each provider
	// package defines a typed Settings struct and round-trips it through this
	// field via [encoding/json].
	Settings json.RawMessage

	// Metadata holds non-load-bearing per-provider attributes (gender, age,
	// category, accent, etc.) for UI display and filtering. Not consulted by
	// the synthesis path.
	Metadata map[string]string
}

// SynthesizeRequest is the input to [Synthesizer.Synthesize].
type SynthesizeRequest struct {
	// Sentence is the text to synthesize. Treated as opaque by the core tts
	// package; may contain provider-native inline markup (e.g. ElevenLabs v3
	// bracketed tags). The Persona/LLM layer is responsible for producing
	// text in the format the chosen Voice's provider expects — see
	// [Synthesizer.AudioMarkupPrompt].
	Sentence string

	// Voice selects the voice to synthesize with; Voice.Settings supplies
	// per-NPC defaults.
	Voice Voice

	// OverrideSettings, when non-nil, deviates from Voice.Settings for this
	// single call. Same provider-typed shape as Voice.Settings; merged over
	// Voice.Settings by the provider.
	OverrideSettings json.RawMessage
}
