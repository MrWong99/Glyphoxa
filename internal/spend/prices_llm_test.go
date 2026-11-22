package spend

import (
	"math"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// TestLLMPrices_VoiceCandidatesArePriced pins the price rows for the voice-LLM
// candidates of #702. A model without a row is charged at the conservative
// fallback ($5 in / $15 out per MTok) — 50x the real claude-haiku-5-5 input
// rate, which would trip a trial allowance within a few turns.
func TestLLMPrices_VoiceCandidatesArePriced(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		provider observe.Provider
		model    string
		in, out  float64 // USD per 1M tokens
	}{
		{observe.ProviderAnthropic, "claude-haiku-5-5", 0.10, 0.50},
		{observe.ProviderAnthropic, "claude-haiku-4-5", 1.00, 5.00},
		{observe.ProviderGroq, "qwen/qwen3.8-27b", 0.80, 4.00},
	} {
		usd, known := llmCostUSD(tc.provider, tc.model, 1_000_000, 1_000_000)
		if !known {
			t.Errorf("(%s, %s) has no price row", tc.provider, tc.model)
			continue
		}
		if want := tc.in + tc.out; math.Abs(usd-want) > 1e-9 {
			t.Errorf("(%s, %s) = $%.4f for 1M in + 1M out, want $%.4f", tc.provider, tc.model, usd, want)
		}
	}
}

// TestLLMPrices_ChatCandidatesArePriced pins the price rows of the Planning
// chat's Anthropic models (ADR-0062 amendment 2026-11-22). Without a row a chat
// choice could not run on the Platform Key at all (priced-only rule), and a
// BYOK one would be metered at the flat fallback, which is BELOW Opus's real
// output rate.
func TestLLMPrices_ChatCandidatesArePriced(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		model   string
		in, out float64 // USD per 1M tokens
	}{
		{"claude-sonnet-5-5", 2.00, 10.00},
		{"claude-opus-5-5", 4.00, 20.00},
		{"claude-sonnet-5", 2.00, 10.00},
		{"claude-opus-5", 5.00, 25.00},
	} {
		if !LLMPriced(observe.ProviderAnthropic, tc.model) {
			t.Errorf("%s has no price row", tc.model)
			continue
		}
		usd := EstimateLLMUSD(observe.ProviderAnthropic, tc.model, 1_000_000, 1_000_000)
		if want := tc.in + tc.out; math.Abs(usd-want) > 1e-9 {
			t.Errorf("%s = $%.4f for 1M in + 1M out, want $%.4f", tc.model, usd, want)
		}
	}
}
