package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// TTS is the orchestrator stage that hands one sentence at a time to a
// [tts.Synthesizer] and republishes the dispatch as a [voiceevent.TTSInvoked]
// on the shared bus.
//
// Per ADR-0021's TTS cassette policy this stage's observable contract is the
// dispatch signal, not the audio: synthesized chunks are drained and dropped
// here. Playback wiring (resampler, Opus encoder, Discord transport) attaches
// on top in a later tracer bullet.
type TTS struct {
	bus         *voiceevent.Bus
	synthesizer tts.Synthesizer

	// rec/provider carry the #125 instrumentation: one tts_total span per
	// successful [Dispatch] (the full Synthesize-through-drain synthesis) and a
	// provider-call/error counter per Dispatch. Both default to no-ops
	// (observe.Discard, empty provider label) so the keyless path stays silent —
	// the mirror of [STT]'s stt_request wiring.
	rec      observe.StageRecorder
	provider observe.Provider

	// retry bounds transient-failure retries around the Synthesize START call
	// (#124, ADR-0044): a single 429/5xx or net.Error is retried with backoff,
	// a non-retryable error fails fast, and a barge cutting ctx aborts at once.
	// ONLY the start is retried — a mid-stream failure (a chunk-drain error) is
	// never retried because audio may already have been dispatched (re-speak risk).
	// Zero value is a valid retries-on policy; [WithTTSRetry] threads the shared one.
	retry retry.Policy

	mu        sync.Mutex
	nextIndex int
}

// TTSOption configures a [TTS] at construction. [WithTTSMetrics] opts the
// tts_total / provider-call instrumentation in.
type TTSOption func(*TTS)

// WithTTSMetrics injects the #125 instrumentation: rec receives one
// [observe.StageRecorder.TTSTotal] span per successful [Dispatch] plus the
// provider-call/error counters, labelled with provider (the bounded provider enum
// for the wired synthesizer). A nil rec leaves the no-op default in place. It is
// the TTS mirror of [WithSTTMetrics].
func WithTTSMetrics(rec observe.StageRecorder, provider observe.Provider) TTSOption {
	return func(s *TTS) {
		if rec != nil {
			s.rec = rec
		}
		s.provider = provider
	}
}

// WithTTSRetry sets the [retry.Policy] wrapping the Synthesize start call (#124):
// a transient 429/5xx or net.Error is retried with backoff before first audio, a
// non-retryable error fails fast, and a barge cutting ctx aborts at once. Only
// the start is retried — never a mid-stream chunk failure. Its injected Sleep/Rand
// keep cassette runs deterministic (ADR-0021). Unset leaves the zero-value policy.
func WithTTSRetry(p retry.Policy) TTSOption {
	return func(s *TTS) { s.retry = p }
}

// NewTTS wires synthesizer into bus. Both must be non-nil; passing nil panics.
// Pass [WithTTSMetrics] to record the synthesis round-trip and provider health;
// without it the stage records nothing (the keyless default).
func NewTTS(bus *voiceevent.Bus, synthesizer tts.Synthesizer, opts ...TTSOption) *TTS {
	if bus == nil {
		panic("orchestrator.NewTTS: bus must not be nil")
	}
	if synthesizer == nil {
		panic("orchestrator.NewTTS: synthesizer must not be nil")
	}
	s := &TTS{bus: bus, synthesizer: synthesizer, rec: observe.Discard{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Dispatch hands one sentence to the configured synthesizer and publishes a
// [voiceevent.TTSInvoked] event identifying the sentence and its 0-based
// position in the turn.
//
// TTSInvoked is published BEFORE the [tts.Synthesizer.Synthesize] call — it
// announces the dispatch attempt, not a success. A sentence whose Synthesize
// start-errors (empty VoiceID, auth failure, bad request) therefore still emits
// TTSInvoked, so the failed sentence is visible as invoked-but-never-spoke rather
// than vanishing with no per-sentence signal (#20); the start error is then
// wrapped and returned. (A FirstAudio for the sentence is what marks it actually
// spoken — see [voiceevent.FirstAudio].)
//
// Per ADR-0022 callers must drain the returned audio channel to release the
// implementation's goroutines; this stage drains and discards the chunks
// itself (per ADR-0021's TTS cassette policy the audio is not observable to
// tests).
//
// Forward-looking shape — when the playback pipeline lands, the drain loop
// becomes a forward into the resampler/aligner that feeds the Opus encoder
// driving Discord (ADR-0005: audio stays in-process). The bus event remains
// the same dispatch signal; production consumers attach to it for
// turn-tracking, the SSE relay (ADR-0014), and barge-in (Q13.5 open) —
// barge-in cancels ctx, the synthesizer closes the channel mid-stream, the
// forward loop unwinds, and ADR-0012's deliver-then-commit boundary decides
// which already-spoken sentences become committed Transcript entries.
// nextIndex is 0-based within the current turn; turn-taking (later tracer
// bullet) will scope a fresh TTS per Agent reply rather than reset this one
// in place.
func (s *TTS) Dispatch(ctx context.Context, sentence string, voice tts.Voice) error {
	// Assign the per-turn index under the lock so concurrent dispatches (an
	// Ensemble Turn or a barge-in canceller, both anticipated below) get
	// distinct, monotonically increasing indices. The index counts dispatch
	// attempts within the turn, assigned before Synthesize so a start-errored
	// sentence still consumes its slot and stays visible.
	// Look-ahead pre-render (#375, F1): a sentence held in the pump's look-ahead lane
	// must NOT announce itself yet — TTSInvoked at pre-render time would make the relay
	// persist a line under the reaction's id BEFORE its EnsembleReaction attributes who
	// spoke (a misattributed "NPC" line) and make the chunker buffer text a barge could
	// still discard. So skip the publish AND the index reservation here; the coordinator
	// announces the sentence at release via [TTS.PublishInvoked]. Everything else — retry,
	// synthesis, drain, char metering (ADR-0045), tts_total — is byte-identical.
	if !voiceevent.IsPlaybackLookahead(ctx) {
		s.mu.Lock()
		index := s.nextIndex
		s.nextIndex++
		s.mu.Unlock()

		s.bus.Publish(voiceevent.TTSInvoked{
			At:       time.Now(),
			Sentence: sentence,
			Index:    index,
			TurnID:   voiceevent.TurnIDFrom(ctx),
		})
	}

	start := time.Now()
	// Retry a transient START failure (429/5xx/net) with backoff before first audio
	// (#124, ADR-0044); a non-retryable error fails fast and a barge cutting ctx
	// aborts at once. Wrapping ONLY the start keeps the one-TTSInvoked-per-Dispatch
	// contract (published above, never per attempt) and never re-speaks a
	// mid-stream failure. Metrics below fire on the final outcome only (#125).
	ch, err := retry.Do(ctx, s.retry, func(ctx context.Context) (<-chan tts.AudioChunk, error) {
		return s.synthesizer.Synthesize(ctx, tts.SynthesizeRequest{
			Sentence: sentence,
			Voice:    voice,
		})
	})
	if err != nil {
		// A start error at the TTS stage (#125): count the call with its outcome and
		// bump the error-only sibling ONLY on a fault (error/timeout). A barge-in that
		// cuts the ctx before Synthesize returns is OutcomeCanceled — a caller cancel,
		// not a vendor error — so it does not inflate the error ratio (#239 review).
		// No tts_total — there was no synthesis to time.
		outcome := observe.CallOutcome(ctx, err)
		s.rec.ProviderCall(observe.StageTTS, s.provider, outcome)
		if outcome.IsFault() {
			s.rec.ProviderError(observe.StageTTS, s.provider)
		}
		return fmt.Errorf("orchestrator.TTS.Dispatch: %w", err)
	}
	// Usage metering (#127, ADR-0045): Synthesize accepted the request, so bill the
	// submitted characters as utf8 runes (ElevenLabs bills submitted characters, not
	// bytes). Counted here, before the drain, so a later barge cutting the audio still
	// bills what was submitted. An atomic counter add; it never blocks or fails the turn.
	s.rec.TTSCharacters(s.provider, utf8.RuneCountInString(sentence))
	// Drain to close, capturing a terminal Err chunk (#436): an adapter that fails
	// MID-STREAM emits one final chunk with Err set before closing, so an abnormal
	// termination is distinguishable from clean completion at this seam.
	var streamErr error
	for chunk := range ch {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	// Mid-stream failure under a LIVE ctx (#436): the room heard at most a fragment
	// of the sentence, so it must NOT commit as delivered. Count the vendor fault
	// (bounded labels, the #430 discipline), announce the failed sentence so the
	// Transcript Chunker retracts it (parity with Agent history), and return an
	// error — the turn module classifies it like a start-error: the sentence is
	// skipped, ttsFailed is sticky, and the turn terminates tts_error instead of a
	// silent success. A cancelled ctx takes the barge branch below unchanged: the
	// caller cut the stream, #401's sentence-grain commit semantics own that case.
	if streamErr != nil && ctx.Err() == nil {
		s.rec.ProviderCall(observe.StageTTS, s.provider, observe.OutcomeError)
		s.rec.ProviderError(observe.StageTTS, s.provider)
		s.bus.Publish(voiceevent.TTSStreamFailed{At: time.Now(), TurnID: voiceevent.TurnIDFrom(ctx)})
		return fmt.Errorf("orchestrator.TTS.Dispatch: synthesis stream failed mid-delivery: %w", streamErr)
	}
	// The channel closed: record the DELIVER span and count the call OK. tts_total is
	// NOT synthesis time — under the lockstep TeeSynthesizer the drain is paced by the
	// playback pump, so it measures synthesis plus paced delivery of the sentence
	// (ADR-0044 amendment, #239 review); the provider-latency signal is tts_ttfb. A
	// mid-stream barge (ctx cancel closing the channel early) is NOT a provider error
	// — the vendor call itself succeeded — so it still counts OK, mirroring agenttool.
	// For a held look-ahead sentence (#375) tts_total ADDITIONALLY spans the lane HOLD
	// (release-to-playback): its Dispatch does not return until the coordinator releases
	// it and the pump drains it, so this measures synthesis + hold + paced delivery. That
	// is acceptable under the ADR-0044 amendment (tts_total is delivery, not TTFB).
	s.rec.TTSTotal(s.provider, time.Since(start))
	s.rec.ProviderCall(observe.StageTTS, s.provider, observe.OutcomeOK)
	return nil
}

// PublishInvoked announces a look-ahead sentence's [voiceevent.TTSInvoked] at the
// moment it is RELEASED to play (#375, F1), not when it was pre-rendered — so the
// relay attributes its line only AFTER the reaction's EnsembleReaction names the
// speaker, and the chunker buffers its text only once it is committed to play. It
// draws the in-turn Index from the SAME monotonic counter [Dispatch] uses.
//
// NOTE: because the look-ahead sentence is dispatched (synthesized) BEFORE the Lead's
// later sentences but ANNOUNCED here after them, its Index can be numerically greater
// than a Lead sentence that dispatched later — nothing consumes TTSInvoked ordering by
// Index within a turn (the relay/chunker coalesce by arrival), so this is accepted.
func (s *TTS) PublishInvoked(turnID, sentence string) {
	s.mu.Lock()
	index := s.nextIndex
	s.nextIndex++
	s.mu.Unlock()

	s.bus.Publish(voiceevent.TTSInvoked{
		At:       time.Now(),
		Sentence: sentence,
		Index:    index,
		TurnID:   turnID,
	})
}
