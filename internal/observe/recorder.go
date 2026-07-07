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

import (
	"context"
	"time"
)

// CallOutcome classifies a provider call's terminal state from its error and the
// call ctx, so the STT and TTS stages record [StageRecorder.ProviderCall] with
// the same rule the agenttool adapter already applies: a nil err is [OutcomeOK];
// a non-nil ctx error (a fired deadline or a barge/supersede cancel) is the
// timeout-shaped [OutcomeTimeout] regardless of the returned err; any other error
// is a vendor [OutcomeError]. Keeping the classification in one place means the
// three provider stages agree on the outcome label.
func CallOutcome(ctx context.Context, err error) Outcome {
	if err == nil {
		return OutcomeOK
	}
	if ctx.Err() != nil {
		return OutcomeTimeout
	}
	return OutcomeError
}

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
	ProviderGroq       Provider = "groq"
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
	// TurnYielded: a late segment of one VAD-over-split utterance that the floor's
	// coalesce grace window folded into the turn already speaking (root cause #2).
	// It was never spoken — distinct from abandoned so the over-split rate (and the
	// dropped-text residual) is observable on its own.
	TurnYielded TurnOutcome = "yielded"
)

// TurnReason is the bounded sub-reason label on the turn-lifecycle counter,
// narrowing WHY a turn ended in its outcome. Kept deliberately small (ADR-0032
// cardinality): each value is published by the seam that knows the cause (the
// orchestrator's BargeIn / Replier via [voiceevent.TurnEnded]); only a turn that
// vanishes with no signal at all falls back to the coarse no_first_audio.
type TurnReason string

const (
	// ReasonNone is the no-further-detail reason (the success path).
	ReasonNone TurnReason = "none"
	// ReasonNoFirstAudio: reaped by the TTL sweep without ever emitting first audio
	// AND with no turn-end signal — the coarse fallback for a turn that simply
	// vanished (the precise reasons below cover turns that ended for a known cause).
	ReasonNoFirstAudio TurnReason = "no_first_audio"
	// ReasonSupersessionGrace: the yielded outcome's reason — the floor's
	// same-utterance coalesce window folded this late segment into the in-flight
	// turn (it did not supersede it).
	ReasonSupersessionGrace TurnReason = "supersession_grace"
	// ReasonBarge: the turn was cut by a confirmed human barge-in before audio.
	ReasonBarge TurnReason = "barge"
	// ReasonTTSError: the turn's TTS synthesis failed (a real provider/synth error,
	// not a context cancel).
	ReasonTTSError TurnReason = "tts_error"
	// ReasonProviderError: the reply producer (LLM round / tool loop) failed before
	// the turn could produce audio.
	ReasonProviderError TurnReason = "provider_error"
	// ReasonMute: a GM muted the Agent, cutting its turn (#211) — distinct from
	// ReasonBarge (human interruption), so a mute does not collapse into
	// no_first_audio.
	ReasonMute TurnReason = "mute"
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
	// WIRED (#125) by the VAD stage: one span per VADSpeechEnd, the constant the
	// stage is constructed with.
	VADHangover(d time.Duration)
	// AddressDetect is the address-detection stage duration.
	// (glyphoxa_voice_address_detect_seconds) — WIRED by the bus subscriber.
	AddressDetect(d time.Duration)
	// CodecDecode / CodecEncode are the per-direction Opus<->PCM costs.
	// (glyphoxa_voice_codec_decode_seconds / _codec_encode_seconds) — WIRED (#125)
	// by the wire codec: CodecDecode per inbound frame decoded, CodecEncode per
	// outbound frame encoded (the enc.Encode section only, never the chunk wait).
	CodecDecode(d time.Duration)
	CodecEncode(d time.Duration)

	// STTRequest is the STT provider POST round-trip.
	// (glyphoxa_voice_stt_request_seconds{provider}) — WIRED by the STT stage
	// (batch Transcribe and the streamed commit path).
	STTRequest(provider Provider, d time.Duration)
	// TTSTimeToFirstByte is the Synthesize call → first AudioChunk span
	// (glyphoxa_voice_tts_ttfb_seconds{provider}) — WIRED by the bus subscriber.
	TTSTimeToFirstByte(provider Provider, d time.Duration)
	// TTSTotal is the full synthesis. (glyphoxa_voice_tts_total_seconds{provider})
	// WIRED (#125) by the TTS stage: one span per successful Dispatch (Synthesize
	// call through the full audio-chunk drain).
	TTSTotal(provider Provider, d time.Duration)

	// LLMRound is one Provider.Complete round inside the agenttool loop. roundIndex
	// is 0-based within the turn; hadToolCall separates "thinking time" (H1) from
	// "extra tool rounds" (H2) — the cut B2 needs. Recorded by the provider
	// adapter, one call per Complete.
	// (glyphoxa_voice_llm_round_seconds{provider,round_index,had_tool_call})
	// — WIRED by agenttool.providerAdapter.Generate.
	LLMRound(provider Provider, roundIndex int, hadToolCall bool, d time.Duration)
	// LLMTurn is the full agenttool loop (all rounds + tool exec) for the turn.
	// (glyphoxa_voice_llm_turn_seconds{provider}) — WIRED (#125) by the agenttool
	// Engine: one span per Generate/GenerateStream, spanning every round, recorded
	// on the success AND error path so a failed turn's latency is still visible.
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
