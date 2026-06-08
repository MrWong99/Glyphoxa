// Package voicebench is the latency benchmark harness for the voice pipeline
// (Sprint 2, Epic C1). It is a thin *consumer* of [voicetest.Harness]: it drives
// clips through the real orchestrator, reads the timestamped event log the
// Harness already collects (plus the A3 hooks), and reduces per-turn stage spans
// to a distribution. It deliberately does NOT re-implement event observation —
// the Harness owns that (ADR-0020).
//
// Two tiers per ADR-0033: a keyless cassette tier (cassettes for STT/TTS/LLM,
// runs every PR, catches *our* orchestration regressions) and a live tier
// (-tags=live, real vendors, nightly cron, measures vendor/thinking variance
// H1–H3). The numbers the harness reports map 1:1 onto the prod
// glyphoxa_voice_* histograms (a bench number == a Prometheus series) — so the
// stage boundaries here MUST match task A3's metric subscriber, not just the
// plan prose.
//
// This file holds the tier-independent reduction: stage vocabulary, the
// percentile engine, and the JSON report shape that the C2 CI job asserts
// against the SLO budgets.
package voicebench

import (
	"sort"
	"time"
)

// Stage names the per-turn latency stages the benchmark reports. Each maps 1:1
// onto a prod glyphoxa_voice_<stage>_seconds histogram (the base unit is seconds
// per Prometheus convention; this package carries durations and prints ms). The
// set and spelling are LOCKED by the Sprint-2 plan + observability.md — keep it
// identical so a bench number and a Prometheus series are the same number.
type Stage string

const (
	// StageResponseLatency is the headline SLO span: vad.speech_end → first
	// AudioChunk reaching PlaybackPump. Asserted at ≤1.2s p50 / ≤2.5s p95.
	StageResponseLatency Stage = "response_latency"

	StageVADHangover   Stage = "vad_hangover"
	StageSTTRequest    Stage = "stt_request"
	StageAddressDetect Stage = "address_detect"
	StageLLMRound      Stage = "llm_round"
	StageLLMTurn       Stage = "llm_turn"
	StageTTSTTFB       Stage = "tts_ttfb"
	StageTTSTotal      Stage = "tts_total"
	StageCodecDecode   Stage = "codec_decode"
	StageCodecEncode   Stage = "codec_encode"
)

// Stages is the canonical report order (headline first, then the contributing
// stages roughly in pipeline order). A reporter iterates this so the JSON
// artifact and any printed table are stable across runs.
var Stages = []Stage{
	StageResponseLatency,
	StageVADHangover,
	StageSTTRequest,
	StageAddressDetect,
	StageLLMRound,
	StageLLMTurn,
	StageTTSTTFB,
	StageTTSTotal,
	StageCodecDecode,
	StageCodecEncode,
}

// Distribution is the reduced latency summary for one stage over N replays. The
// tail (p95/p99) is the point — Bart is "sometimes" slow — so the mean is
// deliberately omitted from the asserted contract (kept only for human context).
type Distribution struct {
	N    int     `json:"n"`
	P50  float64 `json:"p50_ms"`
	P95  float64 `json:"p95_ms"`
	P99  float64 `json:"p99_ms"`
	Max  float64 `json:"max_ms"`
	Mean float64 `json:"mean_ms"`
}

// Summarize reduces a sample of stage durations to a [Distribution] in
// milliseconds. Returns the zero Distribution for an empty sample (N=0) so a
// stage that never fired in a tier (e.g. codec on the PCM cassette path)
// reports cleanly rather than panicking. Percentiles use the
// nearest-rank-on-a-0..n-1-index convention — simple, deterministic, and
// matching the live A/B harness so the two read consistently.
func Summarize(samples []time.Duration) Distribution {
	if len(samples) == 0 {
		return Distribution{}
	}
	ms := make([]float64, len(samples))
	var sum float64
	for i, d := range samples {
		ms[i] = float64(d.Microseconds()) / 1000.0
		sum += ms[i]
	}
	sort.Float64s(ms)
	return Distribution{
		N:    len(ms),
		P50:  percentile(ms, 0.50),
		P95:  percentile(ms, 0.95),
		P99:  percentile(ms, 0.99),
		Max:  ms[len(ms)-1],
		Mean: sum / float64(len(ms)),
	}
}

// percentile returns the p-quantile (0..1) of an already-sorted slice using the
// nearest-rank index p*(n-1). Callers pass a non-empty, ascending slice.
func percentile(sorted []float64, p float64) float64 {
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

// Report is the JSON artifact the C2 CI job emits and asserts against the SLO
// budgets. Tier is "cassette" or "live"; Stages maps each stage name to its
// distribution over the run. Corpus records which clip tiers fed the run so a
// reader can tell a dice-heavy run (H2) from a trivial one.
type Report struct {
	Tier   string                 `json:"tier"`
	Corpus []string               `json:"corpus"`
	N      int                    `json:"replays"`
	Stages map[Stage]Distribution `json:"stages"`
}
