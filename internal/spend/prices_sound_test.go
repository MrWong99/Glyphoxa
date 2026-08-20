package spend

import (
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// TestEstimateSoundUSD_KnownModels pins the two #312 price rows with
// HARD-CODED expectations (the meter_test discipline) so a typo in the map is
// caught: a 30s sting at $0.10/min = $0.05; a 60s music track at $0.60/min =
// $0.60.
func TestEstimateSoundUSD_KnownModels(t *testing.T) {
	t.Parallel()
	if got := EstimateSoundUSD(observe.ProviderElevenLabs, "eleven_text_to_sound_v2", 30*time.Second); got != 0.05 {
		t.Errorf("sting estimate = %v, want 0.05", got)
	}
	if got := EstimateSoundUSD(observe.ProviderElevenLabs, "music_v1", time.Minute); got != 0.60 {
		t.Errorf("music estimate = %v, want 0.60", got)
	}
}

// TestEstimateSoundUSD_UnknownOverEstimates pins the conservative-fallback
// posture: an unpriced model must cost MORE per minute than every known row.
func TestEstimateSoundUSD_UnknownOverEstimates(t *testing.T) {
	t.Parallel()
	unknown := EstimateSoundUSD(observe.ProviderElevenLabs, "some-future-model", time.Minute)
	for key, perMin := range soundPricePerMinute {
		if unknown <= perMin {
			t.Errorf("unknown-model estimate %v/min is not above %v/min (%v %s)", unknown, perMin, key.provider, key.model)
		}
	}
}
