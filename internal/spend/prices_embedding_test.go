package spend

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// TestEstimateEmbeddingUSD pins the #591 query-embed pricing: local Ollama is
// free (the deployment's own hardware), and an unknown provider falls back to
// the conservative default so an unpriced provider over-estimates — the same
// posture as the LLM/TTS/STT fallbacks.
func TestEstimateEmbeddingUSD(t *testing.T) {
	t.Parallel()

	if usd := EstimateEmbeddingUSD(observe.ProviderOllama, 1_000_000); usd != 0 {
		t.Fatalf("Ollama estimate = %v, want 0 (local, no vendor charge)", usd)
	}
	want := defaultEmbeddingPerMTok
	if usd := EstimateEmbeddingUSD(observe.Provider("mystery"), 1_000_000); usd != want {
		t.Fatalf("unknown-provider estimate = %v, want the conservative default %v", usd, want)
	}
	if usd := EstimateEmbeddingUSD(observe.Provider("mystery"), 0); usd != 0 {
		t.Fatalf("zero tokens priced at %v, want 0", usd)
	}
}
