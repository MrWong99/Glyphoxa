package tts_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// Compile-time assertions: capability interfaces are independent of
// Synthesizer (a provider may implement only the core), and a provider may
// implement every capability simultaneously.
var (
	_ tts.Synthesizer         = (*coreOnly)(nil)
	_ tts.Synthesizer         = (*fullProvider)(nil)
	_ tts.VoiceLister         = (*fullProvider)(nil)
	_ tts.VoiceCloner         = (*fullProvider)(nil)
	_ tts.VoiceDesigner       = (*fullProvider)(nil)
	_ tts.DialogueSynthesizer = (*fullProvider)(nil)
)

// coreOnly satisfies just the required Synthesizer surface — modelling a
// provider like OpenAI gpt-4o-mini-tts that lacks cloner/designer/dialogue.
type coreOnly struct{}

func (coreOnly) Synthesize(ctx context.Context, req tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}
func (coreOnly) AudioMarkupPrompt(tts.Voice) string {
	return "Speak in plain prose. Do not include bracketed tags or SSML markup."
}

// fullProvider implements the core plus every capability — modelling
// ElevenLabs.
type fullProvider struct{ coreOnly }

func (fullProvider) ListVoices(context.Context) ([]tts.Voice, error) { return nil, nil }
func (fullProvider) CloneVoice(context.Context, tts.CloneRequest) (tts.Voice, error) {
	return tts.Voice{}, nil
}
func (fullProvider) DesignVoice(context.Context, tts.DesignRequest) ([]tts.VoicePreview, error) {
	return nil, nil
}
func (fullProvider) SaveDesignedVoice(context.Context, tts.SaveDesignedVoiceRequest) (tts.Voice, error) {
	return tts.Voice{}, nil
}
func (fullProvider) SynthesizeDialogue(context.Context, tts.DialogueRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}

// TestVoice_SettingsRoundTrip confirms that provider-typed settings JSON
// survives marshal/unmarshal through Voice.Settings — the contract every
// provider implementation depends on.
func TestVoice_SettingsRoundTrip(t *testing.T) {
	t.Parallel()

	type elevenLabsSettings struct {
		ModelID         string  `json:"model_id"`
		Stability       float64 `json:"stability"`
		SimilarityBoost float64 `json:"similarity_boost"`
	}

	in := elevenLabsSettings{
		ModelID:         "eleven_v3",
		Stability:       0.5,
		SimilarityBoost: 0.75,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	voice := tts.Voice{
		ProviderID: "elevenlabs",
		VoiceID:    "21m00Tcm4TlvDq8ikWAM",
		Name:       "Rachel",
		Settings:   raw,
	}

	var out elevenLabsSettings
	if err := json.Unmarshal(voice.Settings, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("round-trip mismatch: got %+v, want %+v", out, in)
	}
}

// TestSynthesizeRequest_OverrideSettingsOptional confirms that omitting
// OverrideSettings (the per-call override) is a valid zero-value request.
func TestSynthesizeRequest_OverrideSettingsOptional(t *testing.T) {
	t.Parallel()

	req := tts.SynthesizeRequest{
		Sentence: "[whispers] hello world",
		Voice:    tts.Voice{ProviderID: "elevenlabs", VoiceID: "v1"},
	}
	if req.OverrideSettings != nil {
		t.Errorf("zero-value OverrideSettings = %v, want nil", req.OverrideSettings)
	}
}
