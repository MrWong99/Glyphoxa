package wirenpc

import (
	"testing"

	stteleven "github.com/MrWong99/Glyphoxa/pkg/voice/stt/elevenlabs"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// TestSeedProviderConfigs_RecordTheAdaptersOwnModels (#702): the seeded TTS and
// STT provider_config rows record the models the adapters actually run — the TTS
// default every voice without a model_id synthesizes on, and Scribe's one model —
// so the recorded value cannot drift from what the pipeline does (it had: the
// seed said eleven_multilingual_v2 / scribe_v1 while the adapters ran eleven_v3 /
// scribe_v2).
func TestSeedProviderConfigs_RecordTheAdaptersOwnModels(t *testing.T) {
	if ttsModel != ttseleven.DefaultModel {
		t.Errorf("seed TTS model = %q, want the adapter default %q", ttsModel, ttseleven.DefaultModel)
	}
	if sttModel != stteleven.Model {
		t.Errorf("seed STT model = %q, want the adapter model %q", sttModel, stteleven.Model)
	}
}
