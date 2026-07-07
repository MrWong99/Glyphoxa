package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
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

	start := time.Now()
	ch, err := s.synthesizer.Synthesize(ctx, tts.SynthesizeRequest{
		Sentence: sentence,
		Voice:    voice,
	})
	if err != nil {
		// A start error is a provider error at the TTS stage (#125): count the call
		// with its outcome (a cancelled ctx is timeout-shaped) and bump the
		// error-only sibling. No tts_total — there was no synthesis to time.
		s.rec.ProviderCall(observe.StageTTS, s.provider, observe.CallOutcome(ctx, err))
		s.rec.ProviderError(observe.StageTTS, s.provider)
		return fmt.Errorf("orchestrator.TTS.Dispatch: %w", err)
	}
	for range ch {
	}
	// The synthesis completed (the channel closed): record the full-synthesis span
	// and count the call OK. A mid-stream barge (ctx cancel closing the channel
	// early) is NOT a provider error — the vendor call itself succeeded — so it
	// still counts OK, mirroring the agenttool adapter.
	s.rec.TTSTotal(s.provider, time.Since(start))
	s.rec.ProviderCall(observe.StageTTS, s.provider, observe.OutcomeOK)
	return nil
}
