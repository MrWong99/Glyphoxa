package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// STT is the orchestrator stage that hands utterance audio to a
// [stt.Recognizer] and republishes the authoritative transcript as
// [voiceevent.STTFinal] on the shared bus.
//
// In the full slice-1 wiring the VAD stage segments audio into utterances
// and calls Transcribe with each segment's frames; tracer-bullet tests
// short-circuit that and feed pre-segmented clips directly.
type STT struct {
	bus        *voiceevent.Bus
	recognizer stt.Recognizer

	// rec/provider carry the A3 instrumentation: one stt_request span per
	// [stt.Recognizer.Transcribe] (the provider POST round-trip), the SAME seam
	// the agenttool adapter records llm_round on. Both default to no-ops
	// (observe.Discard, empty provider label) so the keyless path and any caller
	// that did not opt in stay silent. This is the STT half of the response_latency
	// span — speechEnd→STTFinal — and the live bench's latency localisation showed
	// it is the dominant fixed cost (~0.7s) inside the headline, so making it a
	// real series lets prod alert on a scribe slowdown, not just the nightly.
	rec      observe.StageRecorder
	provider observe.Provider

	// timeout bounds ONE [stt.Recognizer.Transcribe] call. It is the STT analogue
	// of the Replier's per-turn LLM deadline (agent.DefaultTurnTimeout): the single
	// serial transcription worker (orchestrator.Segmenter) runs Transcribe under the
	// CONVERSATION-lifetime context, which has no per-request deadline — so a hung
	// recognizer POST (e.g. a black-holed ElevenLabs scribe call) would block the
	// worker forever, and every later utterance would queue behind it and never be
	// transcribed: the NPC goes permanently silent (observed live). Bounding each
	// call means a hung provider degrades ONE turn and the worker recovers, instead
	// of wedging the whole pipeline. A barge/supersede cancels only the per-TURN
	// context, never this one, so without this bound nothing interrupts the POST.
	// Zero disables the bound (defaults to [defaultSTTRequestTimeout]).
	timeout time.Duration

	// retry bounds transient-failure retries around the recognizer call (#124,
	// ADR-0044): a single 429/5xx or net.Error is retried with backoff INSIDE the
	// existing per-request timeout, which stays the TOTAL budget wrapping the whole
	// loop — never per-attempt (a 3×timeout budget would recreate the #91 serial-
	// worker wedge). The zero value is a valid policy with retries on by defaults;
	// [WithSTTRetry] threads the shared policy (its injected Sleep keeps cassette
	// runs deterministic, ADR-0021).
	retry retry.Policy
}

// defaultSTTRequestTimeout bounds one recognizer call when [WithSTTTimeout] is
// not set. Generous against a real call (scribe transcribes a VAD-segmented
// utterance in ~1–2s) yet tight enough that a hung provider recovers in seconds
// rather than wedging the serial worker until session shutdown.
const defaultSTTRequestTimeout = 15 * time.Second

// STTOption configures an [STT] at construction: [WithSTTMetrics] opts the
// stt_request instrumentation in, [WithSTTTimeout] overrides the per-request
// deadline.
type STTOption func(*STT)

// WithSTTTimeout overrides the per-[stt.Recognizer.Transcribe] deadline (see
// [STT.timeout]). A non-positive value disables the bound; the default is
// [defaultSTTRequestTimeout]. Tests use a short value to exercise the hung-
// recognizer recovery without waiting the full default.
func WithSTTTimeout(d time.Duration) STTOption {
	return func(s *STT) { s.timeout = d }
}

// WithSTTRetry sets the [retry.Policy] wrapping the recognizer call (#124): a
// transient 429/5xx or net.Error is retried with backoff, a non-retryable error
// (4xx auth) fails fast, and the retry loop lives INSIDE the per-request timeout
// (the total budget). The policy's injected Sleep/Rand keep cassette runs
// deterministic (ADR-0021). Unset leaves the zero-value policy (retries on with
// defaults).
func WithSTTRetry(p retry.Policy) STTOption {
	return func(s *STT) { s.retry = p }
}

// WithSTTMetrics injects the stt_request instrumentation: rec receives one
// [observe.StageRecorder.STTRequest] span per [stt.Recognizer.Transcribe],
// labelled with provider (the bounded provider enum for the wired recognizer). A
// nil rec leaves the no-op default in place.
func WithSTTMetrics(rec observe.StageRecorder, provider observe.Provider) STTOption {
	return func(s *STT) {
		if rec != nil {
			s.rec = rec
		}
		s.provider = provider
	}
}

// NewSTT wires recognizer into bus. Both must be non-nil; passing nil panics.
// Pass [WithSTTMetrics] to record the stt_request POST round-trip; without it
// the stage records nothing (the keyless default).
func NewSTT(bus *voiceevent.Bus, recognizer stt.Recognizer, opts ...STTOption) *STT {
	if bus == nil {
		panic("orchestrator.NewSTT: bus must not be nil")
	}
	if recognizer == nil {
		panic("orchestrator.NewSTT: recognizer must not be nil")
	}
	s := &STT{bus: bus, recognizer: recognizer, rec: observe.Discard{}, timeout: defaultSTTRequestTimeout}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Transcribe forwards frames to the recognizer and, on success, publishes
// the resulting transcript as a [voiceevent.STTFinal]. Errors from the
// recognizer are wrapped and returned without publishing.
//
// An empty transcript (Text == "") is still published. Downstream consumers
// — not this stage — decide what to do with a "the recognizer heard nothing"
// signal; the orchestrator's job is to faithfully relay whatever the
// recognizer authoritatively returns.
func (s *STT) Transcribe(ctx context.Context, frames []audio.Frame) error {
	// Bound the recognizer call so a hung provider cannot wedge the serial
	// transcription worker forever (see [STT.timeout]). context.WithTimeout honours
	// any earlier parent deadline, so this only ever tightens the bound. The
	// recognizer's request ctx is cancelled when the deadline fires, aborting the
	// in-flight POST and freeing the worker for the next utterance.
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}

	start := time.Now()
	// Retry a transient failure (429/5xx/net) with backoff INSIDE the timeout above
	// — which stays the TOTAL budget wrapping the whole loop, never per-attempt
	// (#124, ADR-0044). A non-retryable error (4xx auth) fails fast; a barge/timeout
	// cutting ctx aborts the loop at once. Metrics below fire on the FINAL outcome
	// only (#125), so a recovered retry records one ok span, not one per attempt.
	t, err := retry.Do(ctx, s.retry, func(ctx context.Context) (stt.Transcript, error) {
		return s.recognizer.Transcribe(ctx, frames)
	})
	// Record the POST round-trip whether it succeeded or failed — both consumed
	// real wall-clock inside the response_latency span, and a failed/slow scribe
	// call is exactly what this series exists to surface.
	s.rec.STTRequest(s.provider, time.Since(start))
	// Provider health (#125): count the call with its final outcome and bump the
	// error-only sibling ONLY on a fault (error/timeout), never on a caller-driven
	// cancel — a barge-in that cuts the recognizer ctx is OutcomeCanceled and must
	// not inflate the error ratio (#239 review). The shared [observe.CallOutcome]
	// rule keeps STT/TTS/LLM in agreement (a fired per-request timeout is timeout).
	outcome := observe.CallOutcome(ctx, err)
	s.rec.ProviderCall(observe.StageSTT, s.provider, outcome)
	if err != nil {
		if outcome.IsFault() {
			s.rec.ProviderError(observe.StageSTT, s.provider)
		}
		return fmt.Errorf("orchestrator.STT.Transcribe: %w", err)
	}
	// Usage metering (#127, ADR-0045/0042): on success only, bill the submitted audio
	// length — a failed/hung call above billed nothing. An atomic counter add; it
	// never blocks or fails the turn.
	s.rec.STTAudioSeconds(s.provider, framesDuration(frames))
	s.PublishFinal(ctx, t)
	return nil
}

// framesDuration sums the wall-clock span of frames from each Frame's FrameMs — the
// audio length submitted to the recognizer, which the STT usage meter bills (#127,
// ADR-0045/0042).
func framesDuration(frames []audio.Frame) time.Duration {
	var d time.Duration
	for _, f := range frames {
		d += frameDuration(f)
	}
	return d
}

// frameDuration is one Frame's wall-clock span (FrameMs milliseconds).
func frameDuration(f audio.Frame) time.Duration {
	return time.Duration(f.FrameMs()) * time.Millisecond
}

// PublishFinal fans an authoritative [stt.Transcript] out as a
// [voiceevent.STTFinal]. A turn is born here (A3): its TurnID is minted fresh,
// and the per-segment correlation the ctx carries — the [Segmenter]'s speech-end
// time (so the headline response-latency span is anchored at true end-of-speech)
// and, on the streaming path, the utterance id (ADR-0042, joining this final to
// its partials) — is stamped on the event.
//
// It is the shared publish tail of both transcription paths: the batch
// [STT.Transcribe] above, and the [StreamManager] commit path, which resolves a
// streamed commit and publishes the committed text directly (skipping the batch
// POST) while keeping TurnID/SpeechEndAt minted exactly as today.
func (s *STT) PublishFinal(ctx context.Context, t stt.Transcript) {
	s.bus.Publish(voiceevent.STTFinal{
		At:          time.Now(),
		Text:        t.Text,
		TurnID:      voiceevent.NewTurnID(),
		SpeechEndAt: speechEndAtFrom(ctx),
		UtteranceID: utteranceIDFrom(ctx),
	})
}

// speechEndAtKey carries the segment's [voiceevent.VADSpeechEnd.At] from the
// [Segmenter] to [STT.Transcribe] without widening the Transcribe signature
// (which tracer-bullet tests and the cassette path call directly). Unexported
// and orchestrator-internal.
type speechEndAtKey struct{}

// withSpeechEndAt returns ctx carrying the turn's speech-end time; a zero time
// (a Flush with no speech-end transition) is carried as-is.
func withSpeechEndAt(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, speechEndAtKey{}, at)
}

// speechEndAtFrom recovers the speech-end time, or the zero time if the segment
// was transcribed without one (a direct Transcribe call, or an end-of-stream
// Flush).
func speechEndAtFrom(ctx context.Context) time.Time {
	at, _ := ctx.Value(speechEndAtKey{}).(time.Time)
	return at
}

// utteranceIDKey carries the streaming utterance id (ADR-0042) from the
// [Segmenter] to [STT.PublishFinal] without widening the publish signature —
// pattern-copied from [speechEndAtKey]. Unexported and orchestrator-internal.
type utteranceIDKey struct{}

// withUtteranceID returns ctx carrying the utterance's correlation id; an empty
// id (the batch path, which has no stream and no partials) leaves ctx unchanged,
// so [utteranceIDFrom] yields "" and STTFinal.UtteranceID stays empty.
func withUtteranceID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, utteranceIDKey{}, id)
}

// utteranceIDFrom recovers the streaming utterance id, or "" when the segment was
// transcribed without one (the batch path, or a direct Transcribe call).
func utteranceIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(utteranceIDKey{}).(string)
	return id
}
