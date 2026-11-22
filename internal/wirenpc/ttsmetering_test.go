package wirenpc

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/billing"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// acceptingSynth accepts every sentence and closes at once (no audio).
type acceptingSynth struct{}

func (acceptingSynth) Synthesize(context.Context, tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}
func (acceptingSynth) AudioMarkupPrompt(tts.Voice) string { return "x" }

// TestTTSStageOptions_LedgerGetsTheSynthesisModel is the #702 production-wiring
// bar for model-aware TTS pricing: the TTS stage built with the live options,
// metering through the spend/ledger tee exactly as a Voice Session does, lands
// the NPC's characters in the Usage Ledger under the model the adapter
// synthesizes on (eleven_v4_turbo for the default voice) at that model's rate —
// not under "" at the provider (full-credit) rate.
func TestTTSStageOptions_LedgerGetsTheSynthesisModel(t *testing.T) {
	ledger := billing.NewLedger(uuid.New(), func() time.Time { return time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC) })
	rec := observe.TeeUsage(observe.Discard{}, ledger)
	stage := orchestrator.NewTTS(voiceevent.NewBus(), acceptingSynth{}, ttsStageOptions(rec, retry.Policy{})...)

	const sentence = "Willkommen, Reisender." // 22 runes
	if err := stage.Dispatch(context.Background(), sentence, npcVoice()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	var rows []storage.UsageRow
	if err := ledger.Flush(context.Background(), func(_ context.Context, r []storage.UsageRow) error {
		rows = r
		return nil
	}); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ledger rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Component != storage.ComponentTTS || r.Model != ttseleven.ModelV4Turbo || r.TTSCharacters != 22 {
		t.Errorf("ledger row = %s/%s/%q %d chars, want tts/elevenlabs/%q 22 chars", r.Component, r.Provider, r.Model, r.TTSCharacters, ttseleven.ModelV4Turbo)
	}
	if want := spend.EstimateTTSUSD(observe.ProviderElevenLabs, ttseleven.ModelV4Turbo, 22); r.EstimatedUSD != want {
		t.Errorf("ledger estimated_usd = %v, want the eleven_v4_turbo rate %v", r.EstimatedUSD, want)
	}
}
