package tts

import (
	"context"
	"encoding/json"
)

// VoiceLister is implemented by providers that expose a voice catalog.
//
// Returned [Voice]s have Settings pre-populated with sensible per-model
// defaults so they are immediately usable in [Synthesizer.Synthesize]
// without further configuration.
type VoiceLister interface {
	ListVoices(ctx context.Context) ([]Voice, error)
}

// CloneRequest is the input to [VoiceCloner.CloneVoice].
type CloneRequest struct {
	// Name is the user-visible voice name.
	Name string

	// Samples are WAV-encoded audio samples. At least one is required; the
	// provider may impose vendor-side limits on count and total size.
	Samples [][]byte

	// Description is an optional human-readable description.
	Description string

	// Labels are optional metadata stored alongside the voice (e.g. language,
	// accent).
	Labels map[string]string

	// Settings carries provider-typed cloning options (e.g. ElevenLabs
	// remove_background_noise). Optional.
	Settings json.RawMessage
}

// VoiceCloner is implemented by providers that support cloning a voice from
// audio samples (e.g. ElevenLabs Instant Voice Clone).
type VoiceCloner interface {
	// CloneVoice creates a new voice from the supplied WAV samples.
	//
	// Returns the cloned [Voice] with a stable VoiceID and provider-supplied
	// default Settings. The voice may be quality-pending; check
	// Voice.Metadata["requires_verification"] for the ElevenLabs case.
	CloneVoice(ctx context.Context, req CloneRequest) (Voice, error)
}

// DesignRequest is the input to [VoiceDesigner.DesignVoice].
type DesignRequest struct {
	// Description is a free-text voice description (e.g. "warm female,
	// British accent, slight rasp"). Required.
	Description string

	// SampleText is the text to synthesize for previews. Optional; providers
	// may supply a default.
	SampleText string

	// Settings carries provider-typed design knobs (e.g. ElevenLabs
	// guidance_scale, loudness, seed, should_enhance). Optional.
	Settings json.RawMessage
}

// VoicePreview is one previewable voice candidate returned by
// [VoiceDesigner.DesignVoice].
//
// Audio carries the encoded preview bytes (e.g. mp3) suitable for direct
// playback in a browser <audio> tag — providers do not decode to PCM here
// since previews never enter the hot-path audio pipeline.
type VoicePreview struct {
	// PreviewID is the opaque handle to pass to [VoiceDesigner.SaveDesignedVoice].
	// PreviewIDs may expire on the provider side; persist promptly.
	PreviewID string

	// Audio is the encoded preview audio (e.g. mp3 bytes).
	Audio []byte

	// MIMEType is the IANA media type of Audio (e.g. "audio/mpeg").
	MIMEType string

	// Description is the provider-generated description of the voice.
	// May be empty.
	Description string

	// Metadata is non-load-bearing per-provider attributes for UI display.
	Metadata map[string]string
}

// SaveDesignedVoiceRequest is the input to [VoiceDesigner.SaveDesignedVoice].
type SaveDesignedVoiceRequest struct {
	// PreviewID is the handle from a prior [VoiceDesigner.DesignVoice] response.
	// Required.
	PreviewID string

	// Name is the user-visible voice name. Required.
	Name string

	// Description is an optional human-readable description.
	Description string

	// Labels are optional metadata stored alongside the saved voice.
	Labels map[string]string
}

// VoiceDesigner is implemented by providers that support generating a voice
// from a text description (e.g. ElevenLabs text-to-voice/design).
//
// The lifecycle is two-step: DesignVoice returns ephemeral previews; the
// caller picks one (typically after a UI roundtrip) and SaveDesignedVoice
// persists it as a real [Voice].
type VoiceDesigner interface {
	// DesignVoice generates one or more preview voices from the description.
	// Previews are ephemeral until SaveDesignedVoice is called.
	DesignVoice(ctx context.Context, req DesignRequest) ([]VoicePreview, error)

	// SaveDesignedVoice persists a previously-designed preview as a permanent
	// [Voice] in the provider's library. The returned Voice has a stable
	// VoiceID and is immediately usable in [Synthesizer.Synthesize].
	SaveDesignedVoice(ctx context.Context, req SaveDesignedVoiceRequest) (Voice, error)
}

// DialogueSegment is one speaker's contribution to a dialogue render.
type DialogueSegment struct {
	// Voice is the speaker's voice. May repeat across segments.
	Voice Voice

	// Text is what this speaker says. Treated as opaque; may contain
	// provider-native inline markup.
	Text string
}

// DialogueRequest is the input to [DialogueSynthesizer.SynthesizeDialogue].
type DialogueRequest struct {
	// Segments is the ordered script. Speaker turns are encoded by the
	// sequence of Voices; the provider weaves them together with
	// conversational pacing.
	Segments []DialogueSegment

	// OverrideSettings is provider-typed per-render settings (e.g. ElevenLabs
	// dialogue stability). Applies to the whole render. Optional.
	OverrideSettings json.RawMessage
}

// DialogueSynthesizer is implemented by providers that support multi-voice
// dialogue rendering in a single call (e.g. ElevenLabs text-to-dialogue).
//
// Per ADR-0022 dialogue renders are off the live conversational hot path —
// they exist for batch use cases like recap and cutscenes — and are not
// committed to Transcripts. Cancellation via ctx aborts the render; partial
// audio drained from the channel before cancellation is still valid PCM.
type DialogueSynthesizer interface {
	SynthesizeDialogue(ctx context.Context, req DialogueRequest) (<-chan AudioChunk, error)
}
