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

	// malformed tool generations (#398/#399/#410): provider flake frequency by the
	// detection path that caught it. Process-level (namespace glyphoxa_llm_*, no voice
	// subsystem) since it is an LLM-provider health signal, not a voice-pipeline stage;
	// both labels are bounded enums (ADR-0032).
	malformedToolGen *prometheus.CounterVec // provider, path

	// turn lifecycle (StageRecorder): the survivorship counterpart to
	// response_latency — every turn records one terminal outcome.
	turnTotal *prometheus.CounterVec // outcome, reason

	// embedding backlog (ADR-0032): chunks awaiting embedding. The chunk writer
	// (#104) Sets it from CountUnembeddedChunks after each write; the future
	// backfill worker (#116) Sets it too. Always Set-from-COUNT, never Inc/Dec, so
	// it stays idempotent across writers and restarts.
	embeddingBacklog prometheus.Gauge

	// KG embedding backlog (#300, ADR-0032): Knowledge Graph Nodes awaiting an
	// embedding for the review-surface similarity hint. The embedworker Node phase
	// Sets it from CountUnembeddedNodes, Set-from-COUNT like embeddingBacklog.
	kgEmbeddingBacklog prometheus.Gauge

	// highlight classify (#428, ADR-0032): Session-Highlights classifier passes by
	// bounded outcome (ok / parse_failed / llm_error). Non-StageRecorder like
	// memoryRecall/kgFacts — the detector records against a local interface this
	// adapter satisfies. Makes a live classifier pass observable next to the shared
	// llm_tokens meter it is otherwise indistinguishable from.
	highlightClassify *prometheus.CounterVec // outcome

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

	// --- APPEND ZONE A (recorder fields) ---
	// New instruments append their own comment-tagged section HERE, at the end of
	// the struct, rather than editing a neighbour's block — several metric slices
	// land in parallel and a shared edit point is a guaranteed conflict.

	// #623: disgo audio-send failures, fed from the slog filter the same way
	// daveDecryptErrs is. Unlabelled (ADR-0032 §2.1).
	audioSendErrs prometheus.Counter

	// #604: duration of the two BUDGETED turn-path recalls, alongside their existing
	// outcome counters. memoryRecall_total says WHICH way a recall went;
	// memoryRecallSeconds says how close it ran to the hard 250ms budget (ADR-0042)
	// and kgFactsSeconds to the 50ms one — so a slow drift toward the budget edge is
	// visible before it turns into a skip rate. outcome stays the only label
	// (ADR-0032); the histograms mirror their counters' names and namespaces.
	memoryRecallSeconds *prometheus.HistogramVec // outcome
	kgFactsSeconds      *prometheus.HistogramVec // outcome

	// #633: voice UDP transport liveness. Keepalives move ~every 5s per open
	// voice connection (a flat keepalives series while glyphoxa_voice_sessions
	// is >0 is itself a transport alarm); stall rebuilds count the media
	// watchdog declaring the inbound path dead and forcing a reconnect cycle.
	// All unlabelled (ADR-0032 §2.1 — guild is never a label).
	udpKeepalives        prometheus.Counter
	udpKeepaliveSendErrs prometheus.Counter
	mediaStallRebuilds   prometheus.Counter

	// #612 live-transcript SSE (ADR-0014/0032): how many browsers tail the Session
	// screen, and how often one falls far enough behind that the relay drops it and
	// forces an EventSource reconnect — a user-visible transcript stall, invisible
	// until now. Process-level with ZERO labels: the session id is NEVER a label
	// (ADR-0032). The gauge is Set-from-COUNT like embeddingBacklog.
	transcriptSSELagged      prometheus.Counter
	transcriptSSESubscribers prometheus.Gauge

	// #605
	// Postgres query latency (#605, ADR-0032/0042): per-statement-family timing
	// fed by the pgx QueryTracer on the long-lived server pools. query is a
	// bounded label from storage's const family registry (search_chunks,
	// first_line_at_or_after, …) with everything unannotated in "other" — the SQL
	// text and its args NEVER reach a series. Process-level (namespace only): a
	// query is an OLTP read, not a voice-pipeline stage.
	dbQuery *prometheus.HistogramVec // query

	// #606 playback gap: the audible silence between consecutive sentences of one
	// turn, and the #375 look-ahead lane's events. The histogram is LABEL-FREE —
	// turn_id is never a label (ADR-0032 §2.1) and there is nothing else to split
	// by; the counter's event label is a bounded three-value enum.
	intersentenceGap  prometheus.Histogram
	playbackLookahead *prometheus.CounterVec // event

	// #607: optional side-channel on the headline SLO span. Voice mode installs
	// the flight recorder's trigger here (see flight.go) so a tail spike leaves an
	// execution trace behind, not just a histogram bucket. Nil everywhere else —
	// no series, no cost. Written once at boot before any bus subscriber exists
	// (see SetResponseLatencyHook), read on the subscriber goroutine after.
	respLatencyHook func(time.Duration)
}

// jobDurationBuckets size the background-job handler-duration histogram (#286):
// job work spans sub-second bookkeeping to minutes-long media enrichment, so the
// bins run 0.1s..120s — wider and coarser than the voice SLO buckets.
var jobDurationBuckets = []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120}

// --- APPEND ZONE B (bucket vars) ---
// Bucket sets for new histograms append HERE, each in its own comment-tagged
// block.

// recallBuckets bracket the hard 250ms memory-recall budget (#604, ADR-0042): fine
// bins below it, since a speculation hit is expected in single-digit ms and the
// interesting question is how much headroom an inline miss still has, plus one bin
// past the budget so the near-misses that a timeout would otherwise hide are
// countable.
var recallBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.15, 0.25, 0.5}

// kgFactsBuckets bracket the 50ms KG-fact-read budget (#604): one indexed OLTP read
// is sub-millisecond, so the bins start at 1ms and run to 250ms — five times the
// budget — to size how badly a wedged DB overruns it.
var kgFactsBuckets = []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25}

// #605
// dbQueryBuckets size the Postgres query-latency histogram (#605): OLTP reads
// are sub-second work, so the bins run 1ms..1s — dense at the bottom, where a
// healthy indexed read lives, with boundaries at 0.25s and 0.5s so the ANN
// search's p95 reads directly against the 250ms recall budget (ADR-0042). A
// query slower than 1s lands in +Inf, which is itself the alert.
var dbQueryBuckets = []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1}

// #606 gapBuckets size the inter-sentence playback gap: it is not an SLO span but
// the silence a GM hears between an Agent's sentences, which under the no-pre-
// synthesis-pipelining tradeoff is the next sentence's TTS startup latency. Dense
// from 50ms (below which nothing is perceptible as a pause) through 1s (where a
// gap reads as the NPC hesitating), with tail bins to 5s so a provider stall is
// still observable. Own bins, not the shared latencyBuckets (ADR-0044 precedent).
var gapBuckets = []float64{0.05, 0.1, 0.2, 0.35, 0.5, 0.75, 1.0, 1.5, 2.0, 3.0, 5.0}

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

// HighlightOutcome is the bounded outcome label on the Session-Highlights
// classify-outcome counter (glyphoxa_voice_highlight_classify_total, #428). Exactly
// three values reach a series (ADR-0032): the classifier stream parsed a verdict,
// the completed stream carried no parseable verdict, or the provider Complete/stream
// errored. One increment per classify pass; the precedence when more than one could
// apply is llm_error > parse_failed > ok (the detector owns that resolution).
type HighlightOutcome string

const (
	// HighlightOK: the classifier completed and its verdict parsed to a score.
	HighlightOK HighlightOutcome = "ok"
	// HighlightParseFailed: the stream completed with no parseable JSON verdict, so
	// the score degraded to zero (the moment is simply not confirmed).
	HighlightParseFailed HighlightOutcome = "parse_failed"
	// HighlightLLMError: the provider Complete call failed, or the stream surfaced an
	// error frame. Distinct from a parse failure — the model never delivered a verdict.
	HighlightLLMError HighlightOutcome = "llm_error"
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

	r.kgEmbeddingBacklog = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, // process-level like embedding_backlog (ADR-0032)
		Name:      "kg_embedding_backlog",
		Help:      "Knowledge Graph Nodes awaiting embedding (embedding IS NULL).",
	})

	// Malformed-tool-generation counter (#398): namespace glyphoxa_llm_* (an
	// LLM-provider health signal, not a voice-pipeline stage), provider + path both
	// bounded enums (ADR-0032).
	r.malformedToolGen = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "llm",
		Name:      "malformed_toolgen_total",
		Help:      "Malformed tool generations by provider and the detection path that caught it (#398: a tool_use_failed retried; #399/#410 add roll_claim/text_leak).",
	}, []string{"provider", "path"})

	r.highlightClassify = counterVec("highlight_classify_total",
		"Session-Highlights classifier passes by bounded outcome (#428, ADR-0032): a parsed verdict (ok), a completed stream with no parseable verdict (parse_failed), or a provider Complete/stream error (llm_error).",
		"outcome")

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
		r.kgEmbeddingBacklog, r.memoryRecall, r.kgFacts, r.highlightClassify,
		r.jobsBacklog, r.jobsTotal, r.jobDuration,
		r.llmTokens, r.ttsCharacters, r.sttAudioSeconds, r.malformedToolGen,
		// Standard runtime collectors so /metrics also reports process/Go health.
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
	)

	// --- APPEND ZONE C (construct + register) ---
	// A new instrument constructs its own field and registers it in its OWN
	// MustRegister call HERE, after the big list above — that list is a shared edit
	// point and several metric slices land in parallel.

	// #623: send-path failures, so a turn abandoned with no_first_audio can be told
	// apart from other abandonment causes.
	r.audioSendErrs = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "audio_send_errors_total",
		Help:      "disgo audio-send failures (voice gateway not Ready or UDP write error); bursts accompany reconnect windows; ref #623.",
	})
	reg.MustRegister(r.audioSendErrs)

	// #604: the two budgeted-recall duration histograms. Each mirrors the namespace
	// of the counter it accompanies — memory recall is a voice-pipeline series
	// (glyphoxa_voice_*), the KG read is process-level (glyphoxa_*) — so the
	// _seconds and _total families sit next to each other in a scrape.
	r.memoryRecallSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: subsystem,
		Name:    "memory_recall_seconds",
		Help:    "NPC memory recall duration by outcome (#604, ADR-0042), against the hard 250ms inline budget. Budget headroom: histogram_quantile(0.95, rate(glyphoxa_voice_memory_recall_seconds_bucket[5m])) / 0.25.",
		Buckets: recallBuckets,
	}, []string{"outcome"})
	r.kgFactsSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, // process-level, matching kg_facts_total (ADR-0032)
		Name:      "kg_facts_seconds",
		Help:      "NPC Hot Context KG-fact read duration by outcome (#604), against the 50ms inline budget. Budget headroom: histogram_quantile(0.95, rate(glyphoxa_kg_facts_seconds_bucket[5m])) / 0.05.",
		Buckets:   kgFactsBuckets,
	}, []string{"outcome"})
	reg.MustRegister(r.memoryRecallSeconds, r.kgFactsSeconds)

	// #612 live-transcript SSE series (ADR-0032): namespace-only, no labels.
	r.transcriptSSELagged = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "transcript_sse_lagged_total",
		Help:      "Live-transcript SSE subscribers dropped for lagging, forcing a client reconnect (#612).",
	})
	r.transcriptSSESubscribers = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "transcript_sse_subscribers",
		Help:      "Live-transcript SSE subscribers currently connected (Set-from-COUNT).",
	})
	reg.MustRegister(r.transcriptSSELagged, r.transcriptSSESubscribers)

	// #605
	r.dbQuery = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, // process-level like embedding_backlog (ADR-0032)
		Name:      "db_query_seconds",
		Help:      "Postgres query latency by bounded statement family (#605); unannotated statements record as \"other\".",
		Buckets:   dbQueryBuckets,
	}, []string{"query"})
	reg.MustRegister(r.dbQuery)

	// #606 playback gap: the histogram carries no labels at all (ADR-0032) and its
	// own gap-sized bins; the lane counter splits by the bounded event enum.
	r.intersentenceGap = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: subsystem,
		Name:    "playback_intersentence_gap_seconds",
		Help:    "Audible silence between consecutive sentences of one turn: sentence N's playback end to N+1's first Opus frame on the wire. Cross-turn and post-barge spans are never sampled.",
		Buckets: gapBuckets,
	})
	r.playbackLookahead = counterVec("playback_lookahead_total",
		"Look-ahead lane events (#375, ADR-0025): a held Reaction sentence released into the play queue, a release latched before its prime, or a held-but-unplayed sentence discarded.",
		"event")
	reg.MustRegister(r.intersentenceGap, r.playbackLookahead)

	// #633: voice UDP transport liveness. Discord's voice server stops routing
	// inbound RTP to an outbound-silent peer after ~13-15 min, so pkg/voice's
	// transport writes one keepalive datagram every ~5s per open connection.
	r.udpKeepalives = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "udp_keepalives_total",
		Help:      "Keepalive datagrams written to voice UDP sockets (~one per 5s per open connection); flat while glyphoxa_voice_sessions > 0 means the transport loop is dead; ref #633.",
	})
	r.udpKeepaliveSendErrs = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "udp_keepalive_send_errors_total",
		Help:      "Voice UDP keepalive datagrams that failed to write (transient socket errors; a closed socket ends the loop instead of counting); ref #633.",
	})
	r.mediaStallRebuilds = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "media_stall_rebuilds_total",
		Help:      "Media watchdog verdicts: participants kept announcing speech while no RTP arrived, so the cycle was torn down to rebuild the voice connection; ref #633.",
	})
	reg.MustRegister(r.udpKeepalives, r.udpKeepaliveSendErrs, r.mediaStallRebuilds)
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
	if h := r.respLatencyHook; h != nil { // #607
		h(d)
	}
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

// MalformedToolGen counts one malformed tool generation by provider and the
// detection path that caught it (#398/#399/#410). Both labels are bounded enums
// (ADR-0032); the series lives at glyphoxa_llm_malformed_toolgen_total.
func (r *PrometheusRecorder) MalformedToolGen(p Provider, path MalformedPath) {
	r.malformedToolGen.WithLabelValues(string(p), string(path)).Inc()
}

// MemoryRecall records one NPC memory recall by its bounded outcome and how long
// it took (#122/#604, ADR-0042/0032). It is the standalone recall-metrics sink the
// internal/recall component records against (its local Metrics interface),
// separate from the StageRecorder contract: recall is not an orchestrator stage.
//
// Counter and histogram move together, on purpose: the counter alone cannot say
// whether a hit is running at 5ms or at 240ms of its 250ms budget.
func (r *PrometheusRecorder) MemoryRecall(o RecallOutcome, d time.Duration) {
	r.memoryRecall.WithLabelValues(string(o)).Inc()
	r.memoryRecallSeconds.WithLabelValues(string(o)).Observe(d.Seconds())
}

// HighlightClassify counts one Session-Highlights classifier pass by its bounded
// outcome (#428, ADR-0032). It is the standalone classify-metrics sink the
// internal/highlight detector records against (its local interface), separate from
// the StageRecorder contract: a classify is not an orchestrator stage.
func (r *PrometheusRecorder) HighlightClassify(o HighlightOutcome) {
	r.highlightClassify.WithLabelValues(string(o)).Inc()
}

// KGFacts records one NPC Hot Context KG-fact read by its bounded outcome and how
// long it took (#126/#604, ADR-0008/0032). It is the standalone facts-metrics sink
// the internal/kgfacts component records against (its local Metrics interface),
// separate from the StageRecorder contract: the KG read is not an orchestrator
// stage.
//
// The duration is what separates the two shapes of "degraded" the counter alone
// conflates: a read that burned the whole 50ms budget, versus one that failed in
// microseconds because the DB refused it.
func (r *PrometheusRecorder) KGFacts(o FactsOutcome, d time.Duration) {
	r.kgFacts.WithLabelValues(string(o)).Inc()
	r.kgFactsSeconds.WithLabelValues(string(o)).Observe(d.Seconds())
}

// SetEmbeddingBacklog publishes the current count of transcript chunks awaiting an
// embedding (#104, ADR-0032). Callers Set from a COUNT(*) — never Inc/Dec — so the
// gauge is idempotent across the chunk writer, the future backfill worker (#116)
// and process restarts: whoever last counted wins, and a restart re-seeds it from
// the DB rather than resuming a drifted in-memory delta.
func (r *PrometheusRecorder) SetEmbeddingBacklog(n int) {
	r.embeddingBacklog.Set(float64(n))
}

// SetKGEmbeddingBacklog publishes the current count of Knowledge Graph Nodes
// awaiting an embedding (#300, ADR-0032). Set-from-COUNT like SetEmbeddingBacklog,
// so it stays idempotent across the embedworker Node phase and process restarts.
func (r *PrometheusRecorder) SetKGEmbeddingBacklog(n int) {
	r.kgEmbeddingBacklog.Set(float64(n))
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

// --- APPEND ZONE D (recorder methods) ---
// Recording methods for new instruments append HERE, at the end of the file, each
// in its own comment-tagged section. #604 has none: its two methods (MemoryRecall,
// KGFacts) already existed and were extended in place above.

// --- #623 disgo audio-send failures ---

// AudioSendErrorHook returns the increment hook for NewLogger's
// LogHooks.OnAudioSendError, so disgo's rate-limited "failed to send audio"
// records feed glyphoxa_voice_audio_send_errors_total. Every matched record
// increments (the rate limit trims the LOG line only), so a reconnect window that
// abandons N turns shows up as N here — the send path becoming distinguishable
// from the other causes of a turn_total{outcome="abandoned",reason="no_first_audio"}.
func (r *PrometheusRecorder) AudioSendErrorHook() func() {
	return func() { r.audioSendErrs.Inc() }
}

// --- #612 live-transcript SSE (transcript.SSEMetrics) ---

// TranscriptSSELagged counts one live-transcript SSE subscriber dropped for
// lagging (#612): its fan-out channel overflowed, so the relay closed the stream
// and the browser's EventSource reconnects and replays from Last-Event-ID
// (ADR-0014's designed degradation). One event per dropped subscriber, so the
// rate reads as user-visible transcript stalls per second — not per lost frame.
// Non-blocking (a bare Inc): the relay calls it under its own lock.
func (r *PrometheusRecorder) TranscriptSSELagged() {
	r.transcriptSSELagged.Inc()
}

// SetTranscriptSSESubscribers publishes the number of browsers currently tailing
// a live transcript (#612). The relay Sets it from len(subs) on every attach and
// detach — never Inc/Dec — so the gauge cannot drift across concurrent handlers
// or a restart, mirroring SetEmbeddingBacklog.
func (r *PrometheusRecorder) SetTranscriptSSESubscribers(n int) {
	r.transcriptSSESubscribers.Set(float64(n))
}

// #605
// DBQuery records one Postgres query's latency under its statement family
// (#605). query MUST come from storage's const family registry — the pgx
// QueryTracer is the sole caller and emits only registered names or "other", so
// this method does no clamping and the label space stays bounded (ADR-0032).
// Called on the query hot path (the ANN search inside the 250ms recall budget,
// ADR-0042), so it is one Observe and nothing else.
func (r *PrometheusRecorder) DBQuery(query string, d time.Duration) {
	r.dbQuery.WithLabelValues(query).Observe(d.Seconds())
}

// --- #606 playback gap ---

// IntersentenceGap records the audible silence between two consecutive sentences
// of ONE turn — sentence N's playback end to N+1's first Opus frame on the wire.
// It is the number that decides whether pre-synthesis pipelining is worth building
// (today an ordinary gap is N+1's TTS startup latency). The pump samples only the
// same-turn span: a turn's first sentence belongs to response_latency, a turn
// boundary is conversational, and a barge-torn sentence opens no span.
func (r *PrometheusRecorder) IntersentenceGap(d time.Duration) {
	r.intersentenceGap.Observe(d.Seconds())
}

// PlaybackLookahead counts one look-ahead lane event (#375, ADR-0025), so the
// lane's gap-hiding is readable next to the gap histogram it suppresses.
func (r *PrometheusRecorder) PlaybackLookahead(ev LookaheadEvent) {
	r.playbackLookahead.WithLabelValues(string(ev)).Inc()
}

// --- #607 flight recorder ---

// SetResponseLatencyHook installs a side-channel on ResponseLatency (#607).
// The hook runs INLINE on the caller's goroutine — today the observe bus
// subscriber, holding its lock — so it must cost nanoseconds and never block or
// touch I/O; [FlightRecorder.LatencyBreach] is the intended implementation (a
// comparison plus a non-blocking channel send).
//
// Call it exactly once, at boot, BEFORE the bus subscriber that records spans
// is started. That ordering is what makes the plain field safe: the goroutine
// that reads it is created after the write, so the write happens-before every
// read. A caller that cannot promise it must not use this seam.
func (r *PrometheusRecorder) SetResponseLatencyHook(h func(time.Duration)) {
	r.respLatencyHook = h
}

// --- #633 voice UDP transport liveness (voice.MetricsRecorder) ---

// UDPKeepaliveSent counts one keepalive datagram written to a voice UDP socket
// (#633). Called from the per-connection keepalive goroutine, ~every 5s.
func (r *PrometheusRecorder) UDPKeepaliveSent() { r.udpKeepalives.Inc() }

// UDPKeepaliveSendError counts one keepalive datagram that failed to write
// (#633). A closed socket ends the keepalive loop instead of counting here, so
// a moving rate means a live-but-erroring socket.
func (r *PrometheusRecorder) UDPKeepaliveSendError() { r.udpKeepaliveSendErrs.Inc() }

// MediaStallRebuild counts one media-watchdog verdict (#633): remote
// participants kept announcing speech while no RTP arrived for the stall
// window, so the connect cycle was ended to rebuild the voice connection. The
// guild arg is discarded like every voice.MetricsRecorder label (ADR-0032).
func (r *PrometheusRecorder) MediaStallRebuild(string) { r.mediaStallRebuilds.Inc() }
