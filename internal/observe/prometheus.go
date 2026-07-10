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

// ttsDeliverBuckets size the tts_total DELIVER span, which is not a sub-second
// latency but the whole-sentence delivery time: under the lockstep TeeSynthesizer
// the drain is paced by the playback pump, so a sentence takes as long to deliver
// as it takes to speak (seconds to tens of seconds). The SLO latencyBuckets top out
// at 5s and would dump every real sentence into +Inf, so this series gets its own
// wide bins (ADR-0044 amendment, #239 review). The provider-latency signal lives
// in tts_ttfb (which keeps the SLO buckets).
var ttsDeliverBuckets = []float64{0.5, 1, 2, 5, 10, 20, 30, 60}

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

	// provider usage (StageRecorder, #127 / ADR-0045): token / character / audio-
	// second spend per provider. llmTokens splits by a required direction label
	// (input|output) because Groq prices the two directions differently; model is
	// NEVER a label (it rides only to the spend meter, ADR-0046). ttsCharacters and
	// sttAudioSeconds carry the provider label only (ADR-0032 bounds).
	llmTokens       *prometheus.CounterVec // provider, direction
	ttsCharacters   *prometheus.CounterVec // provider
	sttAudioSeconds *prometheus.CounterVec // provider

	// turn lifecycle (StageRecorder): the survivorship counterpart to
	// response_latency — every turn records one terminal outcome.
	turnTotal *prometheus.CounterVec // outcome, reason

	// embedding backlog (ADR-0032): chunks awaiting embedding. The chunk writer
	// (#104) Sets it from CountUnembeddedChunks after each write; the future
	// backfill worker (#116) Sets it too. Always Set-from-COUNT, never Inc/Dec, so
	// it stays idempotent across writers and restarts.
	embeddingBacklog prometheus.Gauge

	// memory recall (#122, ADR-0042/0032): NPC Hot Context recalls by outcome —
	// a speculation hit, an inline miss, or a degraded/unconfigured skip. The
	// outcome label is a bounded three-value enum (ADR-0032).
	memoryRecall *prometheus.CounterVec // outcome

	// KG facts (#126, ADR-0008/0032): NPC Hot Context KG-fact reads by outcome —
	// facts injected (ok), no public Nodes / no session (empty), or a degraded read
	// (timeout/DB error). Process-level (like embedding_backlog), bounded 3-value
	// outcome label (ADR-0032).
	kgFacts *prometheus.CounterVec // outcome

	// background jobs (#286, ADR-0049/0032): the generic job runner's per-kind
	// backlog gauge (Set-from-COUNT), terminal-outcome counter and handler-duration
	// histogram. kind is bounded by the handler registry, so it is a safe label; a
	// job's id/tenant/error are NEVER labels (ADR-0032). Process-level (namespace
	// only, no voice subsystem), like embedding_backlog.
	jobsBacklog *prometheus.GaugeVec     // kind
	jobsTotal   *prometheus.CounterVec   // kind, outcome
	jobDuration *prometheus.HistogramVec // kind
}

// jobDurationBuckets size the background-job handler-duration histogram (#286):
// job work spans sub-second bookkeeping to minutes-long media enrichment, so the
// bins run 0.1s..120s — wider and coarser than the voice SLO buckets.
var jobDurationBuckets = []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120}

// FactsOutcome is the bounded outcome label on the KG-fact-read counter
// (glyphoxa_kg_facts_total, #126). Exactly three values reach a series (ADR-0032):
// facts were injected, the read found none to inject, or it degraded to nothing.
type FactsOutcome string

const (
	// FactsOK: at least one gm-public Node fact was injected into the prompt.
	FactsOK FactsOutcome = "ok"
	// FactsEmpty: the read succeeded but had nothing to inject — no public Nodes,
	// or no active session to scope the Campaign.
	FactsEmpty FactsOutcome = "empty"
	// FactsDegraded: the read degraded to no-facts — the budget elapsed or the DB
	// read failed. A barge cancel is NOT degraded (it counts nothing).
	FactsDegraded FactsOutcome = "degraded"
)

// RecallOutcome is the bounded outcome label on the NPC memory-recall counter
// (glyphoxa_voice_memory_recall_total, #122). Exactly three values reach a series
// (ADR-0032): a speculation hit reused a partial-prefetched query, an inline miss
// embedded+searched within the turn budget, and a skip degraded to no-memory
// (budget exceeded, provider/DB down, or a defensive guard).
type RecallOutcome string

const (
	// RecallHit: the final utterance matched a speculated partial, so the
	// prefetched vector/world chunks were reused at zero added turn latency.
	RecallHit RecallOutcome = "hit"
	// RecallMiss: no usable speculation, so recall embedded and searched inline
	// within the bounded-sync budget (ADR-0042).
	RecallMiss RecallOutcome = "miss"
	// RecallSkip: recall degraded to no-memory — the budget elapsed, the
	// embeddings/DB path failed, or a defensive guard (unparseable agent id / no
	// active session) tripped. A barge cancel is NOT a skip (it counts nothing).
	RecallSkip RecallOutcome = "skip"
)

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

	// Every per-stage histogram now has a live emit-site (#125): response_latency,
	// address_detect and tts_ttfb from the bus subscriber; llm_round from the
	// agenttool adapter; and the six formerly-reserved families — vad_hangover,
	// stt_request, tts_total, codec_decode, codec_encode, llm_turn — from the VAD /
	// STT / TTS orchestrator stages, the wire codec, and the agenttool loop wrapper.
	// So none carries a RESERVED marker: a consumer that sees no samples is reading a
	// genuinely idle stage, not an unwired one.
	r.responseLatency = hist("response_latency_seconds", "Headline SLO: VAD speech-end to first audio chunk handed to the playback pump.", "agent_role")
	r.vadHangover = plainHist("vad_hangover_seconds", "VAD end-of-speech detection lag (minSilenceFrames*frameMs).")
	r.addressDetect = plainHist("address_detect_seconds", "Address-detection stage duration.")
	r.codecDecode = plainHist("codec_decode_seconds", "Opus->PCM decode per inbound frame.")
	r.codecEncode = plainHist("codec_encode_seconds", "PCM->Opus encode per outbound frame.")
	r.sttRequest = hist("stt_request_seconds", "STT provider POST round-trip.", "provider")
	r.ttsTTFB = hist("tts_ttfb_seconds", "TTS Synthesize call to first audio chunk.", "provider")
	// tts_total is a DELIVER span (synthesis + paced playback), not synthesis time,
	// so it uses the wide ttsDeliverBuckets rather than the shared SLO buckets
	// (ADR-0044 amendment, #239 review). Built inline because hist() bakes in
	// latencyBuckets.
	r.ttsTotal = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: subsystem,
		Name:    "tts_total_seconds",
		Help:    "TTS deliver span: synthesis plus paced playback delivery of one sentence. Provider latency lives in tts_ttfb.",
		Buckets: ttsDeliverBuckets,
	}, []string{"provider"})
	r.llmRound = hist("llm_round_seconds", "One LLM Complete round inside the agenttool loop.", "provider", "round_index", "had_tool_call")
	r.llmTurn = hist("llm_turn_seconds", "Full agenttool loop (all rounds + tool exec).", "provider")

	r.providerCalls = counterVec("provider_calls_total", "Vendor calls by stage, provider and outcome.", "stage", "provider", "outcome")
	r.providerErrors = counterVec("provider_errors_total", "Vendor call errors by stage and provider.", "stage", "provider")
	r.turnTotal = counterVec("turn_total", "Turns by terminal outcome and reason — the survivorship counterpart to response_latency (which records only turns that reached first audio).", "outcome", "reason")

	// Provider usage meters (#127, ADR-0045). direction is required on llm_tokens
	// (Groq prices input/output differently); model is never a label (ADR-0032).
	r.llmTokens = counterVec("llm_tokens_total", "LLM tokens metered by provider and direction (provider-reported, or a ceil(chars/4) estimate when none is reported — never zero).", "provider", "direction")
	r.ttsCharacters = counterVec("tts_characters_total", "TTS characters (utf8 runes) submitted per provider (billed even if a later barge cuts the audio).", "provider")
	r.sttAudioSeconds = counterVec("stt_audio_seconds_total", "STT audio seconds submitted per provider (batch clip length, or streamed voiced+pre-roll duration).", "provider")

	r.embeddingBacklog = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, // process-level, not a voice subsystem metric (ADR-0032)
		Name:      "embedding_backlog",
		Help:      "Transcript chunks awaiting embedding (embedding IS NULL).",
	})

	r.memoryRecall = counterVec("memory_recall_total",
		"NPC memory recalls by outcome (#122, ADR-0042): a speculation hit, an inline miss, or a degraded skip.",
		"outcome")

	// Process-level (namespace only, no voice subsystem) like embedding_backlog: the
	// KG-fact read is an OLTP read shared by any NPC turn, not a voice-pipeline stage.
	r.kgFacts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "kg_facts_total",
		Help:      "NPC Hot Context KG-fact reads by outcome (#126): facts injected (ok), none to inject (empty), or degraded.",
	}, []string{"outcome"})

	// Background job runner series (#286, ADR-0049): namespace-only (no voice
	// subsystem), kind bounded by the handler registry (ADR-0032).
	r.jobsBacklog = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "jobs_backlog",
		Help:      "Runnable background jobs awaiting a worker, per kind (Set-from-COUNT).",
	}, []string{"kind"})
	r.jobsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "jobs_total",
		Help:      "Background jobs by kind and terminal outcome (done, retry, dead).",
	}, []string{"kind", "outcome"})
	r.jobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "job_duration_seconds",
		Help:      "Background job handler execution time, per kind (success or failure).",
		Buckets:   jobDurationBuckets,
	}, []string{"kind"})

	reg.MustRegister(
		r.framesDropped, r.undecodable, r.daveDecryptErrs, r.sessions,
		r.playbackTotal, r.bargeCancels,
		r.responseLatency, r.vadHangover, r.addressDetect, r.codecDecode,
		r.codecEncode, r.sttRequest, r.ttsTTFB, r.ttsTotal, r.llmRound, r.llmTurn,
		r.providerCalls, r.providerErrors, r.turnTotal, r.embeddingBacklog,
		r.memoryRecall, r.kgFacts,
		r.jobsBacklog, r.jobsTotal, r.jobDuration,
		r.llmTokens, r.ttsCharacters, r.sttAudioSeconds,
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

// LLMTokens records a completion's input/output token usage (#127, ADR-0045). The
// model argument is DROPPED here — it exists only for the per-model spend meter
// (ADR-0046); model is never a Prometheus label (ADR-0032). The two directions
// land on separate series because Groq prices them differently.
func (r *PrometheusRecorder) LLMTokens(p Provider, _ string, inputTokens, outputTokens int) {
	r.llmTokens.WithLabelValues(string(p), "input").Add(float64(inputTokens))
	r.llmTokens.WithLabelValues(string(p), "output").Add(float64(outputTokens))
}

// TTSCharacters records characters submitted to a TTS synthesizer (#127).
func (r *PrometheusRecorder) TTSCharacters(p Provider, chars int) {
	r.ttsCharacters.WithLabelValues(string(p)).Add(float64(chars))
}

// STTAudioSeconds records audio-seconds submitted to an STT recognizer (#127),
// exported in the base unit seconds (Prometheus convention).
func (r *PrometheusRecorder) STTAudioSeconds(p Provider, d time.Duration) {
	r.sttAudioSeconds.WithLabelValues(string(p)).Add(d.Seconds())
}

// MemoryRecall counts one NPC memory recall by its bounded outcome (#122,
// ADR-0042/0032). It is the standalone recall-metrics sink the internal/recall
// component records against (its local Metrics interface), separate from the
// StageRecorder contract: recall is not an orchestrator stage.
func (r *PrometheusRecorder) MemoryRecall(o RecallOutcome) {
	r.memoryRecall.WithLabelValues(string(o)).Inc()
}

// KGFacts counts one NPC Hot Context KG-fact read by its bounded outcome (#126,
// ADR-0008/0032). It is the standalone facts-metrics sink the internal/kgfacts
// component records against (its local Metrics interface), separate from the
// StageRecorder contract: the KG read is not an orchestrator stage.
func (r *PrometheusRecorder) KGFacts(o FactsOutcome) {
	r.kgFacts.WithLabelValues(string(o)).Inc()
}

// SetEmbeddingBacklog publishes the current count of transcript chunks awaiting an
// embedding (#104, ADR-0032). Callers Set from a COUNT(*) — never Inc/Dec — so the
// gauge is idempotent across the chunk writer, the future backfill worker (#116)
// and process restarts: whoever last counted wins, and a restart re-seeds it from
// the DB rather than resuming a drifted in-memory delta.
func (r *PrometheusRecorder) SetEmbeddingBacklog(n int) {
	r.embeddingBacklog.Set(float64(n))
}

// --- background job runner (jobs.Metrics, #286/ADR-0049) ---

// JobOutcome counts one background job's terminal outcome by kind (done, retry,
// dead). kind is bounded by the runner's handler registry; a job's id/error are
// never labels (ADR-0032).
func (r *PrometheusRecorder) JobOutcome(kind, outcome string) {
	r.jobsTotal.WithLabelValues(kind, outcome).Inc()
}

// JobDuration observes one background job handler execution's wall time by kind,
// recorded for both successes and failures.
func (r *PrometheusRecorder) JobDuration(kind string, d time.Duration) {
	r.jobDuration.WithLabelValues(kind).Observe(d.Seconds())
}

// SetJobBacklog publishes the current count of runnable jobs for a kind. Callers
// Set from a COUNT(*) — never Inc/Dec — so the gauge stays idempotent across
// runner replicas and restarts (ADR-0032), mirroring SetEmbeddingBacklog.
func (r *PrometheusRecorder) SetJobBacklog(kind string, n int) {
	r.jobsBacklog.WithLabelValues(kind).Set(float64(n))
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
