package spend

import (
	"math"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestEstimateTTSUSD_ModelAware pins the #702 model-aware TTS price with
// HARD-CODED expectations (the meter_test discipline): eleven_v4_turbo bills
// half of eleven_v3's credits per character (the character-cost header showed
// ~0.22 vs ~0.44 credits/char, 2026-11-22), so 1000 v4_turbo characters cost
// half the 1000 v3 ones. A model without its own row keeps the ElevenLabs
// provider rate; an unknown provider keeps the conservative default.
func TestEstimateTTSUSD_ModelAware(t *testing.T) {
	t.Parallel()
	cases := []struct {
		provider observe.Provider
		model    string
		want     float64
	}{
		{observe.ProviderElevenLabs, "eleven_v3", 0.30},
		{observe.ProviderElevenLabs, "eleven_v4_turbo", 0.15},
		{observe.ProviderElevenLabs, "eleven_flash_v2_5", 0.15},
		{observe.ProviderElevenLabs, "", 0.30},
		{observe.ProviderElevenLabs, "some_future_model", 0.30},
		{"unpriced-vendor", "eleven_v4_turbo", 0.50},
	}
	for _, tc := range cases {
		if got := EstimateTTSUSD(tc.provider, tc.model, 1000); !approxEq(got, tc.want) {
			t.Errorf("EstimateTTSUSD(%s, %q, 1000) = %v, want %v", tc.provider, tc.model, got, tc.want)
		}
	}
}

// TestMeterTTSCharacters_PricesByModel: the live spend meter (ADR-0046) prices
// what the pipeline actually synthesized, so a v4_turbo session runs up half the
// estimate of a v3 one for the same characters.
func TestMeterTTSCharacters_PricesByModel(t *testing.T) {
	t.Parallel()
	v3 := NewMeter(Caps{}, nil, nil, nil)
	v3.TTSCharacters(observe.ProviderElevenLabs, "eleven_v3", 1000)
	turbo := NewMeter(Caps{}, nil, nil, nil)
	turbo.TTSCharacters(observe.ProviderElevenLabs, "eleven_v4_turbo", 1000)
	if got, v3USD := turbo.Status().EstimatedUSD, v3.Status().EstimatedUSD; !approxEq(got*2, v3USD) {
		t.Errorf("v4_turbo spend %v is not half of v3 spend %v", got, v3USD)
	}
}
