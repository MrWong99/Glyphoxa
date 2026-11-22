package observe

import (
	"strings"
	"testing"
)

// TestSTTFinal_CountsByTransportAndReason_ThroughTheSpendTee pins the series a
// streaming-STT soak is judged by (#702): how many utterances got their final from
// the stream, and why the rest went to batch. It records through TeeUsage because
// that is what production hands the pipeline (the session Manager always wraps the
// adapter for the spend meter) — STTFinal is on StageRecorder itself, so the tee
// passes it through; a capability recovered by type assertion would read zero here.
func TestSTTFinal_CountsByTransportAndReason_ThroughTheSpendTee(t *testing.T) {
	prom := NewPrometheusRecorder()
	rec := TeeUsage(TeeUsage(prom, &sinkSpy{}), &sinkSpy{})

	rec.STTFinal(ProviderElevenLabs, STTTransportStream, STTFinalCommitted)
	rec.STTFinal(ProviderElevenLabs, STTTransportStream, STTFinalCommitted)
	rec.STTFinal(ProviderElevenLabs, STTTransportBatchFallback, STTFinalStreamDown)
	rec.STTFinal(ProviderElevenLabs, STTTransportBatchFallback, STTFinalCommitTimeout)
	rec.STTFinal(ProviderElevenLabs, STTTransportBatch, STTFinalLaneCap)

	out := scrape(t, prom)
	for _, want := range []string{
		`glyphoxa_voice_stt_final_total{provider="elevenlabs",reason="committed",transport="stream"} 2`,
		`glyphoxa_voice_stt_final_total{provider="elevenlabs",reason="stream_down",transport="batch_fallback"} 1`,
		`glyphoxa_voice_stt_final_total{provider="elevenlabs",reason="commit_timeout",transport="batch_fallback"} 1`,
		`glyphoxa_voice_stt_final_total{provider="elevenlabs",reason="lane_cap",transport="batch"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scrape missing %q\n%s", want, filterGlyphoxa(out))
		}
	}
}

// TestSTTFinal_UnknownLabelsAreClamped: transport and reason are closed enums
// (ADR-0032); a value outside them must not mint a new series.
func TestSTTFinal_UnknownLabelsAreClamped(t *testing.T) {
	prom := NewPrometheusRecorder()
	prom.STTFinal(ProviderElevenLabs, STTTransport("carrier-pigeon"), STTFinalReason("user said: hello"))

	out := scrape(t, prom)
	want := `glyphoxa_voice_stt_final_total{provider="elevenlabs",reason="other",transport="other"} 1`
	if !strings.Contains(out, want) {
		t.Errorf("scrape missing %q\n%s", want, filterGlyphoxa(out))
	}
	if strings.Contains(out, "carrier-pigeon") || strings.Contains(out, "hello") {
		t.Errorf("an unbounded label value reached the registry:\n%s", filterGlyphoxa(out))
	}
}
