package wire

import (
	"context"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// PlaybackSink receives the audio chunks of one synthesized sentence so the
// outbound path (codec → Opus → Session.Play) can speak it. The wire layer hands
// it a fresh channel per sentence — see [TeeSynthesizer] and the per-Dispatch
// granularity the codec's PlaybackSource consumes.
//
// HandleSentence is called once at the start of each sentence's synthesis, on
// the goroutine that invoked Synthesize (the orchestrator's reply reactor). The
// supplied channel delivers that sentence's [tts.AudioChunk]s in order and is
// closed when the sentence is fully synthesized or its context is cancelled
// (barge-in). The handler must not block the caller: it should hand the channel
// to its own consumer goroutine and return promptly, then drain the channel to
// completion (draining is required — see [TeeSynthesizer] on back-pressure).
type PlaybackSink interface {
	HandleSentence(ctx context.Context, chunks <-chan tts.AudioChunk)
}

// PlaybackSinkFunc adapts a function to a [PlaybackSink].
type PlaybackSinkFunc func(ctx context.Context, chunks <-chan tts.AudioChunk)

// HandleSentence implements [PlaybackSink].
func (f PlaybackSinkFunc) HandleSentence(ctx context.Context, chunks <-chan tts.AudioChunk) {
	f(ctx, chunks)
}

// TeeSynthesizer decorates a [tts.Synthesizer] so the orchestrator's TTS stage
// can keep draining-and-dropping its audio (ADR-0021: audio is not observable to
// the orchestrator or its tests) while the wire layer simultaneously receives a
// copy of every chunk for playback. It is a true decorator: the wrapped
// Synthesizer and the orchestrator's drain loop are unchanged; the tee lives
// entirely here.
//
// Per [tts.Synthesizer]'s ADR-0022 lifecycle each Synthesize call renders one
// sentence, so the tee opens one playback channel per call (the per-Dispatch
// granularity the codec's PlaybackSource expects) and hands it to the
// [PlaybackSink] before any chunk flows. Each chunk read from the wrapped stream
// is forwarded to BOTH the orchestrator (via the returned channel) and the sink
// channel; the sink channel is closed only after the wrapped stream is fully
// drained or ctx is cancelled, so ADR-0012's deliver-then-commit boundary — "the
// utterance may commit once the last frame has been forwarded" — stays aligned
// with the close.
//
// Back-pressure: the forward goroutine writes to the sink channel and the
// orchestrator's channel in lockstep, so a slow playback consumer would stall
// the orchestrator's drain. The sink (the playback pump) MUST drain promptly to
// avoid throttling synthesis; the pump's real-time 20 ms pacing is the intended
// rate, which matches the synthesizer's own streaming cadence.
type TeeSynthesizer struct {
	inner tts.Synthesizer
	sink  PlaybackSink
}

// NewTeeSynthesizer wraps inner so that every synthesized chunk is also
// delivered to sink, one fresh channel per sentence. inner is the real audio
// Synthesizer handed to [orchestrator.NewTTS]; sink is the playback path. Both
// must be non-nil.
//
// AudioMarkupPrompt and every other part of the Synthesizer contract pass
// through to inner unchanged — only Synthesize is decorated — so the wrapper is
// safe to use anywhere a [tts.Synthesizer] is expected.
func NewTeeSynthesizer(inner tts.Synthesizer, sink PlaybackSink) *TeeSynthesizer {
	if inner == nil {
		panic("wire.NewTeeSynthesizer: inner Synthesizer must not be nil")
	}
	if sink == nil {
		panic("wire.NewTeeSynthesizer: sink must not be nil")
	}
	return &TeeSynthesizer{inner: inner, sink: sink}
}

// Synthesize delegates to the wrapped Synthesizer and tees the resulting chunk
// stream: it returns a channel to the orchestrator (which drains and drops it,
// unchanged) while forwarding a copy of every chunk to a fresh per-sentence
// playback channel handed to the [PlaybackSink].
//
// If the wrapped Synthesize fails to start, the error is returned directly and
// no playback channel is opened (the sentence never speaks). The returned
// channel closes when the wrapped stream ends or ctx is cancelled; the sink
// channel closes at the same moment.
func (t *TeeSynthesizer) Synthesize(ctx context.Context, req tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	src, err := t.inner.Synthesize(ctx, req)
	if err != nil {
		return nil, err
	}

	// One fresh playback channel per sentence (ADR-0022 lifecycle). Hand it to
	// the sink before any chunk flows so the pump is ready; HandleSentence must
	// return promptly (it spawns its own consumer).
	play := make(chan tts.AudioChunk)
	t.sink.HandleSentence(ctx, play)

	out := make(chan tts.AudioChunk)
	go func() {
		defer close(out)
		defer close(play) // ADR-0012: close marks the sentence delivered/committable.
		for chunk := range src {
			// Forward to the orchestrator's drain. If the orchestrator stops
			// reading (only on its own teardown), honour ctx so we don't leak.
			select {
			case out <- chunk:
			case <-ctx.Done():
				return
			}
			// Forward the same chunk to playback. A cancelled ctx (barge-in) ends
			// the sentence; the deferred closes unwind both channels.
			select {
			case play <- chunk:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// AudioMarkupPrompt passes through to the wrapped Synthesizer unchanged.
func (t *TeeSynthesizer) AudioMarkupPrompt(voice tts.Voice) string {
	return t.inner.AudioMarkupPrompt(voice)
}
