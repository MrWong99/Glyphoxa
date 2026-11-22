package spend

import (
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// TestEstimateSTTUSD_ElevenLabs_IsTheRealtimeKeytermRate: an hour of audio is
// priced at what the default configuration sends since #701 — Scribe v2 Realtime
// with keyterms, $0.22 + $0.08 = $0.30 per audio-hour (ElevenLabs API pricing,
// 2026-11-22). That is the dearest of the four combinations the binary can send
// (batch $0.20, batch with keyterms $0.25, realtime $0.22), so the estimate is an
// upper bound whichever transport an utterance took and whether or not
// GLYPHOXA_STT_KEYTERMS is on. The stale figure it replaces was $0.40.
func TestEstimateSTTUSD_ElevenLabs_IsTheRealtimeKeytermRate(t *testing.T) {
	approx(t, EstimateSTTUSD(observe.ProviderElevenLabs, time.Hour), 0.30)
	approx(t, EstimateSTTUSD(observe.ProviderElevenLabs, 90*time.Second), 0.30*90/3600)
}

// TestEstimateSTTUSD_UnknownProvider_OverEstimates: an unpriced recognizer still
// falls back to the deliberately high default.
func TestEstimateSTTUSD_UnknownProvider_OverEstimates(t *testing.T) {
	if got := EstimateSTTUSD(observe.Provider("nobody"), time.Hour); got <= 0.30 {
		t.Errorf("unknown provider priced at $%.2f/h, want above the known Scribe rate", got)
	}
}
