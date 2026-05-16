package elevenlabs

import (
	"encoding/json"
	"testing"
)

func TestSampleRateFromOutputFormat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int
	}{
		{"pcm_16000", 16000},
		{"pcm_22050", 22050},
		{"pcm_24000", 24000},
		{"pcm_44100", 44100},
		{"pcm_48000", 48000},
		{"mp3_44100_128", 0}, // non-PCM rejected
		{"opus_48000", 0},    // non-PCM rejected
		{"", 0},
		{"pcm_notanumber", 0},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := sampleRateFromOutputFormat(tc.in); got != tc.want {
				t.Errorf("sampleRateFromOutputFormat(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestMergeSettings_OverridePrecedence(t *testing.T) {
	t.Parallel()

	stability := 0.5
	base := Settings{
		ModelID:      ModelV3,
		OutputFormat: "pcm_24000",
		VoiceSettings: &VoiceSettings{
			Stability: &stability,
		},
		LanguageCode: "en",
	}
	baseJSON, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("marshal base: %v", err)
	}

	// Override: change output format and language code; do NOT touch VoiceSettings.
	overrideJSON := []byte(`{"output_format":"pcm_44100","language_code":"de"}`)

	merged, err := mergeSettings(baseJSON, overrideJSON)
	if err != nil {
		t.Fatalf("mergeSettings: %v", err)
	}

	if merged.ModelID != ModelV3 {
		t.Errorf("ModelID = %q, want %q (preserved from base)", merged.ModelID, ModelV3)
	}
	if merged.OutputFormat != "pcm_44100" {
		t.Errorf("OutputFormat = %q, want %q (from override)", merged.OutputFormat, "pcm_44100")
	}
	if merged.LanguageCode != "de" {
		t.Errorf("LanguageCode = %q, want %q (from override)", merged.LanguageCode, "de")
	}
	if merged.VoiceSettings == nil || merged.VoiceSettings.Stability == nil || *merged.VoiceSettings.Stability != stability {
		t.Errorf("VoiceSettings did not survive merge: %+v", merged.VoiceSettings)
	}
}

func TestMergeSettings_NilInputs(t *testing.T) {
	t.Parallel()
	got, err := mergeSettings(nil, nil)
	if err != nil {
		t.Fatalf("mergeSettings(nil,nil): %v", err)
	}
	if got.ModelID != "" || got.OutputFormat != "" || got.LanguageCode != "" ||
		got.VoiceSettings != nil || got.Seed != nil ||
		len(got.PronunciationDictionaryLocators) != 0 || len(got.SuggestedAudioTags) != 0 {
		t.Errorf("mergeSettings(nil,nil) = %+v, want zero value", got)
	}
}

func TestMergeSettings_OverrideOnly(t *testing.T) {
	t.Parallel()
	override := []byte(`{"model_id":"eleven_v3","output_format":"pcm_24000"}`)
	got, err := mergeSettings(nil, override)
	if err != nil {
		t.Fatalf("mergeSettings: %v", err)
	}
	if got.ModelID != ModelV3 {
		t.Errorf("ModelID = %q, want %q", got.ModelID, ModelV3)
	}
	if got.OutputFormat != "pcm_24000" {
		t.Errorf("OutputFormat = %q, want %q", got.OutputFormat, "pcm_24000")
	}
}

// TestMergeSettings_NestedRecursiveMerge confirms the documented recursive
// merge semantic: an override that touches only one field of voice_settings
// updates that field and preserves the rest of voice_settings from base.
func TestMergeSettings_NestedRecursiveMerge(t *testing.T) {
	t.Parallel()
	stability := 0.5
	similarity := 0.75
	base := Settings{VoiceSettings: &VoiceSettings{Stability: &stability, SimilarityBoost: &similarity}}
	baseJSON, _ := json.Marshal(base)

	overrideJSON := []byte(`{"voice_settings":{"stability":0.9}}`)
	got, err := mergeSettings(baseJSON, overrideJSON)
	if err != nil {
		t.Fatalf("mergeSettings: %v", err)
	}
	if got.VoiceSettings == nil || got.VoiceSettings.Stability == nil || *got.VoiceSettings.Stability != 0.9 {
		t.Errorf("Stability not overridden: %+v", got.VoiceSettings)
	}
	if got.VoiceSettings == nil || got.VoiceSettings.SimilarityBoost == nil || *got.VoiceSettings.SimilarityBoost != similarity {
		t.Errorf("SimilarityBoost not preserved: %+v", got.VoiceSettings)
	}
}
