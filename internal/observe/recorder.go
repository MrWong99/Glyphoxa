// Package observe holds Glyphoxa's observability seam: the orchestrator-side
// metric contract (this file) and, in sibling files, the filtering slog.Handler
// that tames the disgo DAVE/codec noise (A1) and the Prometheus adapter that
// implements the recorders (A2, task #3).
//
// Two recording surfaces, deliberately split by where the number is born:
//
//   - pkg/voice's [voice.MetricsRecorder] carries the voice-plumbing counters
//     pkg/voice emits by direct call on the hot path (frame drops, undecodable
//     frames, playback, the session-count gauge, barge cancels).
//   - [StageRecorder] (here) carries the per-stage / per-turn latency histograms
//     and the provider-call counters. The latency spans are derived by a sibling
//     subscriber off the voiceevent bus (out of the hot path) from the [At:]
//     timestamps the events already carry; the provider counters are recorded by
//     the provider adapters directly, because the voiceevent taxonomy stops at
//     vad/stt/address/tts/barge and has no "provider call" event to subscribe to.
//
// Per ADR-0032 §2.1 the only labels that ever reach a series are bounded enums:
// agent_role (butler|character), provider, stage, outcome, round_index,
// had_tool_call. guild/agent_id/turn_id/tenant_id/campaign_id are NEVER labels —
// turn_id is a log/exemplar correlation id only. The Prometheus adapter (task #3)
// is the single implementation; the no-op default ([Discard]) keeps call sites
// nil-check-free, matching pkg/voice's discardMetrics convention.
package observe

import "time"

// AgentRole is the bounded role label substituted for the unbounded agent_id /
// guild on every stage histogram (ADR-0032 §2.1). Exactly two values reach a
// series; anything else collapses to the empty role and is treated as unknown.
type AgentRole string

const (
	// RoleButler is the Tenant's default Butler route.
	RoleButler AgentRole = "butler"
	// RoleCharacter is any Campaign Character NPC (Bart et al.) — a single
	// bounded label value, never the per-NPC agent_id.
	RoleCharacter AgentRole = "character"
)

// Provider is the bounded provider label on stage/provider-call metrics.
type Provider string

const (
	ProviderElevenLabs Provider = "elevenlabs"
	ProviderOpenAI     Provider = "openai"
	ProviderGemini     Provider = "gemini"
	ProviderAnthropic  Provider = "anthropic"
)

// Stage is the bounded stage label on the provider-call counters (which stage of
// the turn issued the vendor call). It mirrors the per-stage histogram family so
// a provider error can be attributed to the stage it broke.
type Stage string

const (
	StageSTT Stage = "stt"
	StageLLM Stage = "llm"
	StageTTS Stage = "tts"
)

// Outcome is the bounded result label on ProviderCall.
type Outcome string

const (
	OutcomeOK      Outcome = "ok"
	OutcomeError   Outcome = "error"
	OutcomeTimeout Outcome = "timeout"
)

// TurnOutcome is the bounded result label on the turn-lifecycle counter
// (glyphoxa_voice_turn_total). It is the survivorship counterpart to
// response_latency: that histogram records ONLY turns that reached first audio,
// so a turn cancelled / errored / abandoned before audio emits no sample and is
// invisible. This counter records EVERY turn's terminal state, so the
// failed/abandoned rate — the headline operational signal for a real-time voice
// product — is observable next to the survivor latency (ADR-0032; latency
// investigation root cause #3).
type TurnOutcome string

const (
	// TurnFirstAudio: the turn reached first audio (it also recorded a
	// response_latency sample). The success state.
	TurnFirstAudio TurnOutcome = "first_audio"
	// TurnAbandoned: the turn opened (STTFinal) but never reached first audio and
	// was reaped by the TTL sweep — cancelled (barge/supersede), errored in TTS, or
	// never synthesized. These are the turns the survivorship-biased
	// response_latency cannot see.
	TurnAbandoned TurnOutcome = "abandoned"
)

// TurnReason is the bounded sub-reason label on the turn-lifecycle counter,
// narrowing WHY a turn ended in its outcome. Kept deliberately small (ADR-0032
// cardinality): the bus subscriber can attribute only what the bus reveals;
// precise cancel attribution (barge vs supersede vs provider 4xx/5xx) needs a
// TurnID-carrying turn-end event and is a follow-up.
type TurnReason string

const (
	// ReasonNone is the no-further-detail reason (the success path).
	ReasonNone TurnReason = "none"
	// ReasonNoFirstAudio: reaped without ever emitting first audio. The coarse
	// catch-all for the abandoned outcome until a precise cancel reason is on the
	// bus.
	ReasonNoFirstAudio TurnReason = "no_first_audio"
)

// StageRecorder records the orchestrator's per-stage / per-turn latency
// histograms and the provider-call counters. It is the contract the bus-driven
// sibling subscriber (latency spans) and the provider adapters (provider calls,
// LLM rounds) code against; the Prometheus adapter (task #3) implements it and
// the no-op [Discard] is the default.
//
// All methods must be safe for concurrent use. Durations are reported as
// time.Duration; the Prometheus adapter exports them in the base unit _seconds
// (Prometheus convention) — a benchmark may print ms but the series is seconds,
// so a bench number maps 1:1 to its histogram (the C1 harness reads the same bus
// timestamps these spans are derived from).
type StageRecorder interface {
	// ResponseLatency is the headline SLO span: VAD speech-end → first
	// tts.AudioChunk handed to the PlaybackPump, for the addressed agent_role.
	// (glyphoxa_voice_response_latency_seconds{agent_role})
	ResponseLatency(role AgentRole, d time.Duration)

	// VADHangover is the speech-end detection lag (minSilenceFrames*frameMs),
	// a fixed per-turn cost B3 tunes. (glyphoxa_voice_vad_hangover_seconds)
	//
	// RESERVED — emit-site not yet wired (carry-over task #11). Defined so the
	// /metrics surface is spec-complete (ADR-0032), but no caller invokes it yet;
	// the histogram stays empty until the VAD/segmenter (orchestrator) stamps it.
	// An empty series here is expected, not a fault.
	VADHangover(d time.Duration)
	// AddressDetect is the address-detection stage duration.
	// (glyphoxa_voice_address_detect_seconds) — WIRED by the bus subscriber.
	AddressDetect(d time.Duration)
	// CodecDecode / CodecEncode are the per-direction Opus<->PCM costs.
	// (glyphoxa_voice_codec_decode_seconds / _codec_encode_seconds)
	//
	// RESERVED — emit-site not yet wired (carry-over task #11); the wire codec
	// (pkg/voice/wire/codec) stamps these. Empty until then, expected.
	CodecDecode(d time.Duration)
	CodecEncode(d time.Duration)

	// STTRequest is the STT provider POST round-trip.
	// (glyphoxa_voice_stt_request_seconds{provider})
	//
	// RESERVED — emit-site not yet wired (carry-over task #11); the STT adapter
	// (pkg/voice/stt/elevenlabs) stamps it. Empty until then, expected.
	STTRequest(provider Provider, d time.Duration)
	// TTSTimeToFirstByte is the Synthesize call → first AudioChunk span
	// (glyphoxa_voice_tts_ttfb_seconds{provider}) — WIRED by the bus subscriber.
	TTSTimeToFirstByte(provider Provider, d time.Duration)
	// TTSTotal is the full synthesis. (glyphoxa_voice_tts_total_seconds{provider})
	//
	// RESERVED — emit-site not yet wired (carry-over task #11); the TTS stage/
	// adapter stamps it. Empty until then, expected.
	TTSTotal(provider Provider, d time.Duration)

	// LLMRound is one Provider.Complete round inside the agenttool loop. roundIndex
	// is 0-based within the turn; hadToolCall separates "thinking time" (H1) from
	// "extra tool rounds" (H2) — the cut B2 needs. Recorded by the provider
	// adapter, one call per Complete.
	// (glyphoxa_voice_llm_round_seconds{provider,round_index,had_tool_call})
	// — WIRED by agenttool.providerAdapter.Generate.
	LLMRound(provider Provider, roundIndex int, hadToolCall bool, d time.Duration)
	// LLMTurn is the full agenttool loop (all rounds + tool exec) for the turn.
	// (glyphoxa_voice_llm_turn_seconds{provider})
	//
	// RESERVED — emit-site not yet wired (carry-over task #11); the agenttool loop
	// wrapper stamps the full-turn span (it currently records only per-round
	// LLMRound). Empty until then, expected.
	LLMTurn(provider Provider, d time.Duration)

	// ProviderCall counts one vendor call at stage with its outcome; ProviderError
	// is the error-only sibling for the convenience error-ratio query. The adapter
	// records ProviderCall on every call and additionally ProviderError when the
	// outcome is not OutcomeOK, so rate(errors)/rate(calls) is well-defined.
	// (glyphoxa_voice_provider_calls_total{stage,provider,outcome},
	//  glyphoxa_voice_provider_errors_total{stage,provider})
	ProviderCall(stage Stage, provider Provider, outcome Outcome)
	ProviderError(stage Stage, provider Provider)

	// TurnOutcome counts one turn's terminal state, the survivorship counterpart
	// to ResponseLatency: every turn records exactly one outcome, so the
	// failed/abandoned rate is visible next to the survivor latency.
	// (glyphoxa_voice_turn_total{outcome,reason}) — WIRED by the bus subscriber
	// (first_audio on the first FirstAudio per turn, abandoned on a TTL reap).
	TurnOutcome(outcome TurnOutcome, reason TurnReason)
}

// Discard is the no-op StageRecorder used when none is configured, so call sites
// never nil-check. It mirrors pkg/voice's discardMetrics.
type Discard struct{}

func (Discard) ResponseLatency(AgentRole, time.Duration)    {}
func (Discard) VADHangover(time.Duration)                   {}
func (Discard) AddressDetect(time.Duration)                 {}
func (Discard) CodecDecode(time.Duration)                   {}
func (Discard) CodecEncode(time.Duration)                   {}
func (Discard) STTRequest(Provider, time.Duration)          {}
func (Discard) TTSTimeToFirstByte(Provider, time.Duration)  {}
func (Discard) TTSTotal(Provider, time.Duration)            {}
func (Discard) LLMRound(Provider, int, bool, time.Duration) {}
func (Discard) LLMTurn(Provider, time.Duration)             {}
func (Discard) ProviderCall(Stage, Provider, Outcome)       {}
func (Discard) ProviderError(Stage, Provider)               {}
func (Discard) TurnOutcome(TurnOutcome, TurnReason)         {}

// Static assertion that the no-op satisfies the contract.
var _ StageRecorder = Discard{}
