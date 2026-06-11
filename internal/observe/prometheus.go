package observe

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// namespace/subsystem give every series the glyphoxa_voice_ prefix (ADR-0032 §2.2).
const (
	namespace = "glyphoxa"
	subsystem = "voice"
)

// latencyBuckets are sized to the engineering SLO (p50 ≤ 1.2s, p95 ≤ 2.5s,
// sprint-2-plan §SLO): dense from 50ms through the p95 target, with tail bins to
// 5s so a regression past SLO is still observable. Shared by the response-latency
// and per-stage histograms so a bench number maps 1:1 to a bucket boundary.
var latencyBuckets = []float64{
	0.05, 0.1, 0.2, 0.35, 0.5, 0.75, 1.0, 1.2, 1.5, 2.0, 2.5, 3.0, 4.0, 5.0,
}

// PrometheusRecorder is the single adapter implementing both metric contracts —
// pkg/voice's [voice.MetricsRecorder] (hot-path plumbing) and [StageRecorder]
// (orchestrator stage/turn timings + provider calls). It owns its own
// *prometheus.Registry so a test can scrape an isolated instance; [Registry]
// exposes it for the /metrics handler and process-collector registration.
//
// Cardinality (ADR-0032 §2.1): guild/agent_id/turn_id are NEVER labels. The
// MetricsRecorder methods take guild for the interface but this adapter discards
// it — the bounded agent_role/provider/stage/outcome enums are the only labels.
type PrometheusRecorder struct {
	reg *prometheus.Registry

	// plumbing (voice.MetricsRecorder)
	framesDropped   prometheus.Counter
	undecodable     prometheus.Counter
	daveDecryptErrs prometheus.Counter
	sessions        prometheus.Gauge
	playbackTotal   *prometheus.CounterVec // interrupted
	bargeCancels    prometheus.Counter

	// latency / per-stage (StageRecorder)
	responseLatency *prometheus.HistogramVec // agent_role
	vadHangover     prometheus.Histogram
	addressDetect   prometheus.Histogram
	codecDecode     prometheus.Histogram
	codecEncode     prometheus.Histogram
	sttRequest      *prometheus.HistogramVec // provider
	ttsTTFB         *prometheus.HistogramVec // provider
	ttsTotal        *prometheus.HistogramVec // provider
	llmRound        *prometheus.HistogramVec // provider, round_index, had_tool_call
	llmTurn         *prometheus.HistogramVec // provider

	// provider health (StageRecorder)
	providerCalls  *prometheus.CounterVec // stage, provider, outcome
	providerErrors *prometheus.CounterVec // stage, provider

	// turn lifecycle (StageRecorder): the survivorship counterpart to
	// response_latency — every turn records one terminal outcome.
	turnTotal *prometheus.CounterVec // outcome, reason

	// embedding backlog: spec-complete stub (ADR-0032). The persistence/embedding
	// layer isn't coded yet, so nothing Sets this — registered so the /metrics
	// surface matches ADR-0032 and a reviewer diffing the two sees no gap.
	embeddingBacklog prometheus.Gauge
}

// NewPrometheusRecorder builds the adapter and registers every glyphoxa_voice_*
// series on a fresh registry, plus the standard process/Go collectors so
// /metrics also reports runtime health.
func NewPrometheusRecorder() *PrometheusRecorder {
	reg := prometheus.NewRegistry()
	r := &PrometheusRecorder{reg: reg}

	hist := func(name, help string, labels ...string) *prometheus.HistogramVec {
		return prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: subsystem,
			Name: name, Help: help, Buckets: latencyBuckets,
		}, labels)
	}
	plainHist := func(name, help string) prometheus.Histogram {
		return prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: subsystem,
			Name: name, Help: help, Buckets: latencyBuckets,
		})
	}
	counter := func(name, help string) prometheus.Counter {
		return prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: subsystem, Name: name, Help: help,
		})
	}
	counterVec := func(name, help string, labels ...string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: subsystem, Name: name, Help: help,
		}, labels)
	}
	gauge := func(name, help string) prometheus.Gauge {
		return prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: subsystem, Name: name, Help: help,
		})
	}

	r.framesDropped = counter("inbound_frames_dropped_total", "Inbound frames dropped under the drop-oldest buffer policy.")
	r.undecodable = counter("inbound_undecodable_frames_total", "Inbound frames skipped because codec decode returned a non-fatal error (benign DAVE/SSRC transients).")
	r.daveDecryptErrs = counter("dave_decrypt_errors_total", "disgo DAVE/MLS decrypt failures on inbound packets (benign trickle around epoch rolls; a sustained rate is a handshake fault).")
	r.sessions = gauge("sessions", "Open voice sessions.")
	r.playbackTotal = counterVec("playback_total", "Playbacks finished, by whether they were interrupted.", "interrupted")
	r.bargeCancels = counter("barge_cancels_total", "Confirmed barge-ins that tore down an Agent's active turn (ADR-0027).")

	// Of the per-stage histograms, only response_latency, address_detect and
	// tts_ttfb have a live emit-site this sprint (the bus subscriber); llm_round
	// is emitted by the agenttool adapter. The remaining six — vad_hangover,
	// stt_request, tts_total, codec_decode, codec_encode, llm_turn — are RESERVED:
	// registered so the /metrics surface is spec-complete (ADR-0032), but their
	// emit-sites are carry-over task #11, so they expose an empty histogram (0
	// observations) until wired. The Help text says so, so a consumer doesn't read
	// the absence of samples as a fault.
	const reserved = " RESERVED: emit-site not yet wired (carry-over task #11); empty until then."

	r.responseLatency = hist("response_latency_seconds", "Headline SLO: VAD speech-end to first audio chunk handed to the playback pump.", "agent_role")
	r.vadHangover = plainHist("vad_hangover_seconds", "VAD end-of-speech detection lag (minSilenceFrames*frameMs)."+reserved)
	r.addressDetect = plainHist("address_detect_seconds", "Address-detection stage duration.")
	r.codecDecode = plainHist("codec_decode_seconds", "Opus->PCM decode per inbound frame."+reserved)
	r.codecEncode = plainHist("codec_encode_seconds", "PCM->Opus encode per outbound frame."+reserved)
	r.sttRequest = hist("stt_request_seconds", "STT provider POST round-trip."+reserved, "provider")
	r.ttsTTFB = hist("tts_ttfb_seconds", "TTS Synthesize call to first audio chunk.", "provider")
	r.ttsTotal = hist("tts_total_seconds", "Full TTS synthesis."+reserved, "provider")
	r.llmRound = hist("llm_round_seconds", "One LLM Complete round inside the agenttool loop.", "provider", "round_index", "had_tool_call")
	r.llmTurn = hist("llm_turn_seconds", "Full agenttool loop (all rounds + tool exec)."+reserved, "provider")

	r.providerCalls = counterVec("provider_calls_total", "Vendor calls by stage, provider and outcome.", "stage", "provider", "outcome")
	r.providerErrors = counterVec("provider_errors_total", "Vendor call errors by stage and provider.", "stage", "provider")
	r.turnTotal = counterVec("turn_total", "Turns by terminal outcome and reason — the survivorship counterpart to response_latency (which records only turns that reached first audio).", "outcome", "reason")

	r.embeddingBacklog = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, // process-level, not a voice subsystem metric (ADR-0032)
		Name:      "embedding_backlog",
		Help:      "Transcript chunks awaiting embedding (embedding IS NULL). Stub until the embedding layer lands.",
	})

	reg.MustRegister(
		r.framesDropped, r.undecodable, r.daveDecryptErrs, r.sessions,
		r.playbackTotal, r.bargeCancels,
		r.responseLatency, r.vadHangover, r.addressDetect, r.codecDecode,
		r.codecEncode, r.sttRequest, r.ttsTTFB, r.ttsTotal, r.llmRound, r.llmTurn,
		r.providerCalls, r.providerErrors, r.turnTotal, r.embeddingBacklog,
		// Standard runtime collectors so /metrics also reports process/Go health.
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
	)
	return r
}

// Registry returns the adapter's registry for the /metrics handler.
func (r *PrometheusRecorder) Registry() *prometheus.Registry { return r.reg }

// Handler returns the promhttp handler scraping this adapter's registry — mount
// it at /metrics on the existing web/all server, or on the voice-mode metrics
// listener (see [MetricsServer]).
func (r *PrometheusRecorder) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{})
}

// DAVEDecryptHook returns the increment hook for NewLogger's onDAVEDecrypt, so
// the filtered benign-noise log site feeds glyphoxa_voice_dave_decrypt_errors_total.
func (r *PrometheusRecorder) DAVEDecryptHook() func() {
	return func() { r.daveDecryptErrs.Inc() }
}

// --- voice.MetricsRecorder (guild discarded: ADR-0032 §2.1) ---

func (r *PrometheusRecorder) InboundFramesDropped(_ string, n int) {
	r.framesDropped.Add(float64(n))
}
func (r *PrometheusRecorder) InboundUndecodableFrame(string) { r.undecodable.Inc() }
func (r *PrometheusRecorder) SessionOpened(string)           { r.sessions.Inc() }
func (r *PrometheusRecorder) SessionClosed(string)           { r.sessions.Dec() }
func (r *PrometheusRecorder) PlaybackStarted(string)         {}
func (r *PrometheusRecorder) PlaybackFinished(_ string, interrupted bool) {
	r.playbackTotal.WithLabelValues(boolLabel(interrupted)).Inc()
}
func (r *PrometheusRecorder) BargeCancelled(string) { r.bargeCancels.Inc() }

// --- StageRecorder ---

func (r *PrometheusRecorder) ResponseLatency(role AgentRole, d time.Duration) {
	r.responseLatency.WithLabelValues(string(role)).Observe(d.Seconds())
}
func (r *PrometheusRecorder) VADHangover(d time.Duration)   { r.vadHangover.Observe(d.Seconds()) }
func (r *PrometheusRecorder) AddressDetect(d time.Duration) { r.addressDetect.Observe(d.Seconds()) }
func (r *PrometheusRecorder) CodecDecode(d time.Duration)   { r.codecDecode.Observe(d.Seconds()) }
func (r *PrometheusRecorder) CodecEncode(d time.Duration)   { r.codecEncode.Observe(d.Seconds()) }
func (r *PrometheusRecorder) STTRequest(p Provider, d time.Duration) {
	r.sttRequest.WithLabelValues(string(p)).Observe(d.Seconds())
}
func (r *PrometheusRecorder) TTSTimeToFirstByte(p Provider, d time.Duration) {
	r.ttsTTFB.WithLabelValues(string(p)).Observe(d.Seconds())
}
func (r *PrometheusRecorder) TTSTotal(p Provider, d time.Duration) {
	r.ttsTotal.WithLabelValues(string(p)).Observe(d.Seconds())
}
func (r *PrometheusRecorder) LLMRound(p Provider, roundIndex int, hadToolCall bool, d time.Duration) {
	r.llmRound.WithLabelValues(string(p), roundIndexLabel(roundIndex), boolLabel(hadToolCall)).Observe(d.Seconds())
}
func (r *PrometheusRecorder) LLMTurn(p Provider, d time.Duration) {
	r.llmTurn.WithLabelValues(string(p)).Observe(d.Seconds())
}
func (r *PrometheusRecorder) ProviderCall(s Stage, p Provider, o Outcome) {
	r.providerCalls.WithLabelValues(string(s), string(p), string(o)).Inc()
}
func (r *PrometheusRecorder) ProviderError(s Stage, p Provider) {
	r.providerErrors.WithLabelValues(string(s), string(p)).Inc()
}
func (r *PrometheusRecorder) TurnOutcome(outcome TurnOutcome, reason TurnReason) {
	r.turnTotal.WithLabelValues(string(outcome), string(reason)).Inc()
}

// Static assertions that the one adapter satisfies both contracts. The
// voice.MetricsRecorder assertion lives in a build-tagged sibling to avoid a
// pkg/voice import cycle concern — see metricsrecorder_assert.go.
var _ StageRecorder = (*PrometheusRecorder)(nil)

func boolLabel(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// roundIndexLabel bounds the round_index label: the agenttool loop is capped at a
// handful of rounds (tool.DefaultMaxRounds), so the cardinality is small, but we
// clamp anything unexpectedly large to "many" so a runaway can never explode the
// series space.
func roundIndexLabel(i int) string {
	switch {
	case i < 0:
		return "0"
	case i <= 5:
		return roundIndexNames[i]
	default:
		return "many"
	}
}

var roundIndexNames = [...]string{"0", "1", "2", "3", "4", "5"}
