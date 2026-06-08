package wire

import (
	"context"
	"sync"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// PlaybackPump is the outbound playback half of the live loop: a [PlaybackSink]
// that speaks each synthesized sentence on a voice Session via the [Codec].
//
// It serializes playback across sentences. [TeeSynthesizer] calls
// [PlaybackPump.HandleSentence] once per sentence, on the orchestrator's reply
// goroutine, and that call must return promptly — but the sentences must still
// be spoken one after another, because [gxvoice.Session.Play] auto-interrupts
// the current playback (playing sentence N+1 concurrently would cut off N's
// tail). So HandleSentence only enqueues; a single worker goroutine plays each
// sentence to completion (via [PlaySentence], which blocks on the playback's
// Done) before taking the next. That preserves both order and full tails.
//
// Consequence of strict serialization with the lockstep tee: while sentence N
// plays, sentence N+1's chunk channel is not drained, so its synthesis
// back-pressures until N finishes — no pre-synthesis pipelining, so an
// inter-sentence gap is N+1's TTS startup latency. Correct speech over gapless
// is the right v1 tradeoff; eager pre-buffering is deferred.
type PlaybackPump struct {
	player sessionPlayer
	codec  Codec

	queue chan playJob
	stop  chan struct{} // closed by Close to tell the worker to exit
	done  chan struct{} // closed by the worker when it has exited
	once  sync.Once
}

type playJob struct {
	ctx    context.Context
	chunks <-chan tts.AudioChunk
}

// NewPlaybackPump builds a pump speaking on sess via codec and starts its worker.
// Call [PlaybackPump.Close] at teardown to stop the worker. sess and codec must
// be non-nil.
func NewPlaybackPump(sess *gxvoice.Session, codec Codec) *PlaybackPump {
	if sess == nil {
		panic("wire.NewPlaybackPump: session must not be nil")
	}
	return newPump(realPlayer{sess}, codec)
}

// newPump is the testable core over the sessionPlayer seam, so the cross-
// sentence serialization can be exercised with a fake player and no live Session.
func newPump(player sessionPlayer, codec Codec) *PlaybackPump {
	if codec == nil {
		panic("wire.NewPlaybackPump: codec must not be nil")
	}
	p := &PlaybackPump{
		player: player,
		codec:  codec,
		// Cap 1 is provably sufficient: the orchestrator's TTS.Dispatch does not
		// return (and so the Replier does not Dispatch the next sentence, which is
		// what triggers the next HandleSentence) until the tee's forward goroutine
		// has drained that sentence's channel — which only happens once the worker
		// has dequeued the job and PlaySentence is consuming it. So the queue is
		// empty at every enqueue; the buffer only decouples the enqueue from the
		// worker's in-flight PlaySentence so HandleSentence returns at once.
		queue: make(chan playJob, 1),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go p.run()
	return p
}

// HandleSentence implements [PlaybackSink]: it enqueues the sentence for the
// worker and returns immediately, never blocking the synthesis goroutine. The
// worker drains chunks; a sentence whose ctx is already cancelled is played as a
// no-op ([PlaySentence] returns promptly on a cancelled context). After [Close]
// it drains chunks so the tee's lockstep forward goroutine never blocks.
func (p *PlaybackPump) HandleSentence(ctx context.Context, chunks <-chan tts.AudioChunk) {
	// Priority-check stop first: after Close the queue is empty, so a plain
	// select could randomly enqueue a job no worker will ever play or drain,
	// orphaning the tee's lockstep producer. Once stopped we always drain.
	select {
	case <-p.stop:
		go drain(chunks)
		return
	default:
	}
	select {
	case p.queue <- playJob{ctx: ctx, chunks: chunks}:
	case <-p.stop:
		go drain(chunks)
	}
}

// run is the single serial worker: it plays one sentence to completion before
// taking the next, which is what serializes playback and preserves order. It
// exits when Close signals stop, finishing any in-flight sentence first.
func (p *PlaybackPump) run() {
	defer close(p.done)
	for {
		select {
		case job := <-p.queue:
			// playSentence blocks until this sentence's playback finishes or is
			// interrupted; only then do we take the next, so Session.Play never
			// auto-interrupts a still-playing sentence. A playback error is not
			// fatal to the loop — one bad sentence must not silence the rest.
			_ = playSentence(job.ctx, p.player, p.codec, job.chunks)
		case <-p.stop:
			return
		}
	}
}

// Close stops the worker (after any in-flight sentence) and blocks until it has
// exited. It is idempotent and safe to call once at teardown; the pump must not
// be used afterwards.
func (p *PlaybackPump) Close() {
	p.once.Do(func() { close(p.stop) })
	<-p.done
}

// drain discards a chunk channel so a lockstep producer never blocks on it.
func drain(chunks <-chan tts.AudioChunk) {
	for range chunks {
	}
}

// PlaybackPump is a PlaybackSink.
var _ PlaybackSink = (*PlaybackPump)(nil)
