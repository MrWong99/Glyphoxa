package wire

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
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
// back-pressures until N finishes — no pre-synthesis pipelining, so an ordinary
// inter-sentence gap is N+1's TTS startup latency. Correct speech over gapless is
// the right v1 tradeoff for the ordinary queue.
//
// The ONE exception is the turn-keyed look-ahead lane (#375, ADR-0025): a queued
// Cross-talk Reaction's FIRST sentence, marked [voiceevent.WithPlaybackLookahead],
// is HELD in the lane — synthesized eagerly (its first chunk pre-paid at the tee,
// so its TTFB is spent DURING the Lead's playback) but never drained nor played —
// until the coordinator calls [PlaybackPump.ReleaseLookahead]. The hold is pure
// readiness with ZERO chunk buffering: the tee stays blocked on its play<-chunk
// send, so its forward goroutine never closes the sentence and its Dispatch never
// returns and nothing commits (ADR-0012 deliver-then-commit unbroken) until the
// lane is released and the sentence plays. On a barge/yield the coordinator calls
// [PlaybackPump.DiscardLookahead] to drain-and-drop the held-but-unplayed audio.
// The worker NEVER reads the lane — only Release/Discard/Close move a held job.
type PlaybackPump struct {
	player sessionPlayer
	codec  Codec
	logger *slog.Logger
	bus    *voiceevent.Bus // optional; when set, the playback Source publishes FirstOpus (task #7)

	queue chan playJob
	stop  chan struct{} // closed by Close to tell the worker to exit
	done  chan struct{} // closed by the worker when it has exited
	once  sync.Once

	// laneMu guards the turn-keyed look-ahead lane (#375). laneJob is the single
	// held job (depth 1, ADR-0025) and laneTurn its owning turn id; released latches
	// a ReleaseLookahead that arrived BEFORE the sentence was primed (release-before-
	// prime race), so the imminent prime bypasses the lane straight to the queue. The
	// lane is turn-keyed — NOT a bare "held/not-held" flag — so a stale defer discard
	// or a superseded reaction from an older turn can never drain the CURRENT turn's
	// held job (cross-turn supersede race). The worker never touches these fields.
	laneMu   sync.Mutex
	laneJob  *playJob
	laneTurn string
	released string

	// outboundTap, when set, is called with every Opus frame pulled to the wire
	// (the rollover tape's agent-speech capture point, #306). Agent audio is always
	// on tape (ADR-0051). Nil by default — no tap means unchanged playback; the tap
	// MUST NOT block, it runs on disgo's 20 ms sender goroutine.
	outboundTap func(opus []byte)
}

// PumpOption configures a [PlaybackPump] at construction.
type PumpOption func(*PlaybackPump)

// WithOutboundOpusTap installs a tap called with each Opus frame the pump pulls to
// the wire (#306's agent-speech capture). The tap MUST NOT block. Without it,
// playback is unchanged.
func WithOutboundOpusTap(tap func(opus []byte)) PumpOption {
	return func(p *PlaybackPump) { p.outboundTap = tap }
}

type playJob struct {
	ctx    context.Context
	chunks <-chan tts.AudioChunk
}

// NewPlaybackPump builds a pump speaking on sess via codec and starts its worker.
// Call [PlaybackPump.Close] at teardown to stop the worker. sess and codec must
// be non-nil. logger receives a warning per failed sentence playback (a mute
// NPC must be diagnosable, not silent); nil discards them. bus is optional: when
// non-nil, the first Opus frame of each turn publishes [voiceevent.FirstOpus] —
// the audible-on-wire SLO boundary (task #7); nil disables it. opts add optional
// taps (see [WithOutboundOpusTap]).
func NewPlaybackPump(sess *gxvoice.Session, codec Codec, logger *slog.Logger, bus *voiceevent.Bus, opts ...PumpOption) *PlaybackPump {
	if sess == nil {
		panic("wire.NewPlaybackPump: session must not be nil")
	}
	return newPump(realPlayer{sess}, codec, logger, bus, opts...)
}

// newPump is the testable core over the sessionPlayer seam, so the cross-
// sentence serialization can be exercised with a fake player and no live Session.
func newPump(player sessionPlayer, codec Codec, logger *slog.Logger, bus *voiceevent.Bus, opts ...PumpOption) *PlaybackPump {
	if codec == nil {
		panic("wire.NewPlaybackPump: codec must not be nil")
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	p := &PlaybackPump{
		player: player,
		codec:  codec,
		logger: logger,
		bus:    bus,
		// Cap 1 is provably sufficient: the orchestrator's TTS.Dispatch does not
		// return (and so the Replier does not Dispatch the next sentence, which is
		// what triggers the next HandleSentence) until the tee's forward goroutine
		// has drained that sentence's channel — which only happens once the worker
		// has dequeued the job and PlaySentence is consuming it. So the queue is
		// empty at every enqueue; the buffer only decouples the enqueue from the
		// worker's in-flight PlaySentence so HandleSentence returns at once.
		//
		// The #375 look-ahead lane preserves this invariant: a held Reaction s1 sits
		// in the LANE, never the queue, so it adds no queue depth. ReleaseLookahead
		// only enqueues s1 after the Lead's last job has been dequeued (the release
		// fires after the Lead's Speak returned), and the Reaction's s2+ dispatch only
		// after s1's own hand-off drains — so the queue is still empty at every enqueue.
		queue: make(chan playJob, 1),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	for _, o := range opts {
		o(p)
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
	// orphaning the tee's lockstep producer. Once stopped we always drain. The
	// stop check precedes the lane routing too, so a look-ahead sentence handed
	// after Close is drained, never parked in a lane no Release will ever reach.
	select {
	case <-p.stop:
		go drain(chunks)
		return
	default:
	}
	job := playJob{ctx: ctx, chunks: chunks}
	// A look-ahead sentence (#375) is HELD in the turn-keyed lane, not queued: the
	// worker never plays nor drains it until ReleaseLookahead moves it to the queue.
	if voiceevent.IsPlaybackLookahead(ctx) {
		p.prime(job, voiceevent.TurnIDFrom(ctx))
		return
	}
	p.enqueue(job)
}

// enqueue hands a job to the worker's queue, or drains it if Close has already
// stopped the worker (so the tee's lockstep producer is never orphaned). It is the
// shared tail of the ordinary HandleSentence path and the ReleaseLookahead move.
func (p *PlaybackPump) enqueue(job playJob) {
	select {
	case <-p.stop:
		go drain(job.chunks)
		return
	default:
	}
	select {
	case p.queue <- job:
	case <-p.stop:
		go drain(job.chunks)
	}
}

// prime installs a look-ahead sentence in the lane (#375). If a matching Release
// already latched (release-before-prime), the sentence bypasses the lane straight
// to the queue. If the lane already holds an OLDER turn's job (a superseded
// reaction, stale by construction), that job is drained before the new one is held
// — the turn key makes the supersede unambiguous.
func (p *PlaybackPump) prime(job playJob, id string) {
	// A sentence whose ctx already cancelled (a barge that landed before the tee
	// primed it) must NEVER be held or enqueued — it produced no committable audio.
	// Drain it and return (checked BEFORE the release latch, so a latch-bypass of an
	// already-dead sentence is caught too). Its tee forward goroutine is unblocked.
	if job.ctx.Err() != nil {
		go drain(job.chunks)
		return
	}
	p.laneMu.Lock()
	if p.released == id {
		// Release arrived first: consume the latch and enqueue now.
		p.released = ""
		p.laneMu.Unlock()
		p.enqueue(job)
		return
	}
	var stale *playJob
	if p.laneJob != nil {
		stale = p.laneJob
		p.logger.Warn("wire: look-ahead lane superseded", "stale", p.laneTurn, "new", id)
	}
	j := job
	p.laneJob = &j
	p.laneTurn = id
	p.laneMu.Unlock()
	if stale != nil {
		go drain(stale.chunks)
	}
}

// ReleaseLookahead moves the held look-ahead sentence for turnID into the play
// queue so it plays after the in-flight sentence (order preserved). If the sentence
// has not been primed yet, it latches so the imminent prime enqueues directly
// (release-before-prime). It is non-blocking and never waits for a prime. A turnID
// that owns no held job and no pending prime is a no-op.
func (p *PlaybackPump) ReleaseLookahead(turnID string) {
	p.laneMu.Lock()
	if p.laneJob != nil && p.laneTurn == turnID {
		job := *p.laneJob
		p.laneJob = nil
		p.laneTurn = ""
		p.laneMu.Unlock()
		p.enqueue(job)
		return
	}
	p.released = turnID
	p.laneMu.Unlock()
}

// DiscardLookahead drains-and-drops the held look-ahead sentence for turnID (a
// barge/yield tore the unit down, ADR-0027): the pre-rendered-but-unplayed audio
// is dropped, unblocking the tee's held forward goroutine, and nothing commits
// (ADR-0012). It clears a pending release latch for turnID too. A turnID that owns
// no held job and no latch is a no-op — a stale keyed discard is harmless, so the
// coordinator can defer it unconditionally.
func (p *PlaybackPump) DiscardLookahead(turnID string) {
	p.laneMu.Lock()
	if p.laneJob != nil && p.laneTurn == turnID {
		job := *p.laneJob
		p.laneJob = nil
		p.laneTurn = ""
		p.laneMu.Unlock()
		go drain(job.chunks)
		return
	}
	if p.released == turnID {
		p.released = ""
	}
	p.laneMu.Unlock()
}

// run is the single serial worker: it plays one sentence to completion before
// taking the next, which is what serializes playback and preserves order. It
// exits when Close signals stop, finishing any in-flight sentence first and
// draining any job still queued (its tee forwarder must never be orphaned).
func (p *PlaybackPump) run() {
	defer close(p.done)
	for {
		select {
		case job := <-p.queue:
			// Stop wins over a dequeued job: a select with both arms ready picks
			// randomly, and a sentence enqueued just before Close must be dropped
			// (drained), not spoken into the teardown — Close's contract is
			// "finish the IN-FLIGHT sentence", and a queued one hasn't started.
			select {
			case <-p.stop:
				go drain(job.chunks)
				continue
			default:
			}
			// playSentence blocks until this sentence's playback finishes or is
			// interrupted; only then do we take the next, so Session.Play never
			// auto-interrupts a still-playing sentence. A playback error is not
			// fatal to the loop — one bad sentence must not silence the rest —
			// but it must not be invisible either: a persistent failure here is
			// "the NPC went mute", so warn on everything except the expected
			// barge-in interrupt and ctx cancellation.
			if err := playSentenceBus(job.ctx, p.player, p.codec, job.chunks, p.bus, p.outboundTap); err != nil &&
				!errors.Is(err, gxvoice.ErrInterrupted) && !errors.Is(err, context.Canceled) {
				p.logger.Warn("wire: sentence playback failed", "err", err)
			}
		case <-p.stop:
			// A job can already sit in queue when stop closes (enqueued before
			// Close, never dequeued because this select picked the stop arm).
			// Abandoning it undrained would block the tee's lockstep forwarder
			// on its chunk channel — drain everything left before exiting.
			for {
				select {
				case job := <-p.queue:
					go drain(job.chunks)
				default:
					return
				}
			}
		}
	}
}

// Close stops the worker (after any in-flight sentence) and blocks until it has
// exited. It is idempotent and safe to call once at teardown; the pump must not
// be used afterwards.
func (p *PlaybackPump) Close() {
	p.once.Do(func() { close(p.stop) })
	<-p.done
	// The worker never touches the lane, so a look-ahead sentence primed but never
	// released/discarded would wedge its tee's lockstep forward goroutine forever.
	// Drain it UNCONDITIONALLY after the worker has exited (go drain, matching the
	// worker's queue-drain path: the tee's deferred close ends the range).
	p.laneMu.Lock()
	job := p.laneJob
	p.laneJob = nil
	p.laneTurn = ""
	p.released = ""
	p.laneMu.Unlock()
	if job != nil {
		go drain(job.chunks)
	}
}

// drain discards a chunk channel so a lockstep producer never blocks on it.
func drain(chunks <-chan tts.AudioChunk) {
	for range chunks {
	}
}

// PlaybackPump is a PlaybackSink.
var _ PlaybackSink = (*PlaybackPump)(nil)
