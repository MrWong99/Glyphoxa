package wire

import (
	"context"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// SentencePlayer speaks one synthesized sentence to completion, blocking until
// it finishes or is interrupted. [PlaySentence] is the production implementation
// (codec → Opus → Session.Play); the seam exists so [SequentialSink] can be
// tested without a live Session.
type SentencePlayer func(ctx context.Context, chunks <-chan tts.AudioChunk) error

// NewSessionPlayer binds a [SentencePlayer] to a concrete Session and Codec — the
// production wiring that turns each sentence's chunk stream into Opus frames on
// the voice channel via [PlaySentence].
func NewSessionPlayer(sess *gxvoice.Session, codec Codec) SentencePlayer {
	return func(ctx context.Context, chunks <-chan tts.AudioChunk) error {
		return PlaySentence(ctx, sess, codec, chunks)
	}
}

// SequentialSink is the bridge between the per-sentence channels the
// [TeeSynthesizer] emits and the [PlaySentence] discipline that speaks them. It
// is a [PlaybackSink]: each HandleSentence enqueues one sentence's chunk channel,
// and a single worker goroutine plays the queue strictly one at a time.
//
// Serialization is the whole point. [gxvoice.Session.Play] auto-interrupts the
// current playback, so two overlapping Play calls would cut off all but the last
// sentence; [PlaySentence] blocks until each sentence's playback is Done, and the
// single worker guarantees the next sentence's Play starts only after the
// previous one returns. HandleSentence itself returns promptly (it only enqueues,
// honouring the tee's "must not block the caller" contract) — back-pressure
// instead flows through the tee's lockstep forward: while the worker is blocked
// speaking sentence N, the tee stalls writing sentence N+1's chunks, which stalls
// the Replier's next Dispatch. That is the desired pacing, not a stall to fix.
type SequentialSink struct {
	play  SentencePlayer
	queue chan (<-chan tts.AudioChunk)
	onErr func(error)
}

// NewSequentialSink starts the worker and returns a sink ready to hand to
// [NewTeeSynthesizer]. play speaks one sentence (use [NewSessionPlayer] in
// production); onErr, if non-nil, receives a non-interrupt playback error per
// sentence (an interrupt/barge-in is normal turn flow, not reported). The worker
// runs until ctx is cancelled, then drains: it stops accepting work and exits, so
// a cancelled Run leaks no goroutine.
func NewSequentialSink(ctx context.Context, play SentencePlayer, onErr func(error)) *SequentialSink {
	s := &SequentialSink{
		play:  play,
		queue: make(chan (<-chan tts.AudioChunk), 1), // buffer 1 so HandleSentence rarely blocks
		onErr: onErr,
	}
	go s.run(ctx)
	return s
}

// run is the single playback worker: it speaks one queued sentence at a time, in
// order, until ctx is cancelled. Because there is exactly one worker and
// [PlaySentence] blocks until each sentence is Done, no two Play calls overlap.
func (s *SequentialSink) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case chunks := <-s.queue:
			if err := s.play(ctx, chunks); err != nil && s.onErr != nil {
				s.onErr(err)
			}
		}
	}
}

// HandleSentence implements [PlaybackSink]: it enqueues the sentence for the
// serial worker and returns promptly. If the worker is still speaking the
// previous sentence and the one-deep buffer is full, it blocks until there is
// room (applying the intended back-pressure) or ctx is cancelled (barge-in /
// shutdown), in which case the sentence is dropped — its chunk channel is still
// drained by the tee, so no goroutine leaks.
func (s *SequentialSink) HandleSentence(ctx context.Context, chunks <-chan tts.AudioChunk) {
	select {
	case s.queue <- chunks:
	case <-ctx.Done():
	}
}
