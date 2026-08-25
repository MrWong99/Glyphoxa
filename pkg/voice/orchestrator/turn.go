package orchestrator

// This file is the turn-lifecycle module (#444): the ONE owner of ADR-0012's
// deliver-then-commit protocol. Every reply path — the routed Replier (batch and
// streaming), the Ensemble Lead, both Cross-talk Reaction paths, GM /say —
// dispatches through a [turnRun] instead of hand-rolling the check-ctx →
// synthesize → map-sentinel → re-check-ctx → commit dance, so the protocol is a
// tested state machine here rather than replicated caller knowledge.

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// ErrTextDelivered is the sentinel a producer ([StreamReplyFunc], an
// [EnsembleSpeaker]'s Speak/SpeakReaction) returns to signal that it completed a
// turn by delivering the whole answer as TEXT (a Butler turn routed to its
// TextSink, #299) rather than dispatching any TTS. The turn reached no first
// audio, but it is a SUCCESS — [turnRun.finish] maps it to a
// [voiceevent.TurnEndTextDelivered] terminal instead of the provider_error a
// generic producer error would report, so the metrics subscriber does not
// miscount a delivered text answer as abandoned. It is NOT surfaced through the
// [ErrorFunc].
var ErrTextDelivered = errors.New("orchestrator: turn delivered as text")

// ErrNotDelivered is the dispatch-callback signal (#362, ADR-0012) for a TTS
// failure under a LIVE turn ctx — a start-error (the audio never started) or a
// mid-stream stream failure (#436, the provider died after a fragment): either
// way the sentence was NOT fully delivered, so the producer must NOT commit it —
// but the turn is still alive, so the producer keeps going with later sentences.
// It is distinct from a
// ctx.Err() (barge/mute) return, which cuts the turn and STOPS the producer.
// Producers classify it via [OutcomeOf] rather than errors.Is — the sentinel is
// an implementation detail of this module's dispatch contract.
var ErrNotDelivered = errors.New("orchestrator: sentence not delivered")

// SentenceOutcome is the three-class result of dispatching one sentence through
// a turn (#362, ADR-0012) — the producer-facing form of the dispatch contract.
type SentenceOutcome int

const (
	// SentenceDelivered: fully synthesized under a live turn ctx — the producer
	// may commit it to history.
	SentenceDelivered SentenceOutcome = iota
	// SentenceNotDelivered: a TTS start-error under a LIVE ctx — do NOT commit
	// this sentence, but the turn is alive, so keep producing later sentences.
	SentenceNotDelivered
	// SentenceCut: the turn was cut before or during the sentence's drain (a
	// barge/mute) — stop producing and do not commit the sentence.
	SentenceCut
)

// OutcomeOf classifies a dispatch callback's returned error into the three-class
// contract. It is how producers (the agent emit paths, [EnsembleSpeaker]
// implementations) interpret dispatch results without referencing the sentinel
// errors themselves.
func OutcomeOf(err error) SentenceOutcome {
	switch {
	case err == nil:
		return SentenceDelivered
	case errors.Is(err, ErrNotDelivered):
		return SentenceNotDelivered
	default:
		return SentenceCut
	}
}

// synthFunc is the module's seam onto the TTS stage ([TTS.Dispatch] in
// production; a scripted fake in the module's state-machine tests).
type synthFunc func(ctx context.Context, sentence string, v tts.Voice) error

// turnRun is one turn's deliver-then-commit state machine (ADR-0012). Build one
// per turn (per producer drain), hand its [turnRun.dispatch] to the producer as
// the sentence callback, then map the terminal via [turnRun.finish] (the routed
// path) or read the accumulated state (the ensemble paths, whose terminal
// publishing differs per exit).
//
// It owns, in one place: the pre-dispatch ctx check, the synth call, the
// ErrorFunc surfacing, the cancel-vs-start-error disambiguation, the post-drain
// ctx re-check (a cancel DURING the drain is ambiguous and treated as
// undelivered — ADR-0012's under-report bias: history may omit a sentence the
// room fully heard, but never includes one it did not), and the
// [Reply.OnDelivered] commit hook, fired exactly once iff the sentence was
// delivered.
type turnRun struct {
	ctx     context.Context
	synth   synthFunc
	onError ErrorFunc

	// mu guards attempted/ttsFailed. The sequential dispatch sites never contend
	// for it; the pre-synthesis pipeline (#626) runs two sentences of ONE turn
	// concurrently (sentence k+1 synthesizes while k is delivered), so both flags
	// can be written from two goroutines and read by [turnRun.finish] on a third.
	mu sync.Mutex

	// attempted: some dispatch got past the pre-check (audio may exist) — the
	// Cross-talk barge terminal keys on it (a barge before the first sentence
	// interrupted nothing).
	attempted bool
	// ttsFailed: some sentence start-errored or failed mid-stream (#436) under a
	// live ctx — sticky, so the turn terminates tts_error, never a silent success.
	ttsFailed bool
}

// newTurnRun builds the state machine for one turn under ctx.
func newTurnRun(ctx context.Context, synth synthFunc, onError ErrorFunc) *turnRun {
	return &turnRun{ctx: ctx, synth: synth, onError: onError}
}

// newTurn is the [Replier]'s constructor: one turn over its TTS stage.
func (r *Replier) newTurn(ctx context.Context) *turnRun {
	return newTurnRun(ctx, r.tts.Dispatch, r.onError)
}

// dispatch sends one sentence through the turn: it is the func(Reply) error
// callback handed to producers. The returned error carries the three-class
// contract (classify with [OutcomeOf]): nil = delivered (the OnDelivered hook
// has fired), [ErrNotDelivered] = start-error under a live turn (skip, keep
// going), anything else = the turn was cut (stop).
func (t *turnRun) dispatch(rep Reply) error {
	return t.run(t.ctx, rep, ErrNotDelivered, nil)
}

// dispatchHeld is the pump look-ahead variant (#375): the turn's FIRST sentence
// is synthesized under a [voiceevent.WithPlaybackLookahead]-marked ctx so the
// pump HOLDS its audio, and onHeld hands the sentence text to the coordinator
// BEFORE the synth call blocks on the pump lane. A start-error returns abort
// instead of [ErrNotDelivered]: skip-and-continue would let the second sentence
// enqueue on the normal path and leapfrog the still-playing Lead, so the caller
// supplies a non-sentinel error that stops the producer's drain as a unit.
func (t *turnRun) dispatchHeld(rep Reply, onHeld func(sentence string), abort error) error {
	if err := t.ctx.Err(); err != nil {
		return err
	}
	onHeld(rep.Sentence)
	return t.run(voiceevent.WithPlaybackLookahead(t.ctx), rep, abort, t.ctx)
}

// run is the shared dispatch body: dctx is the ctx the synth call runs under
// (lookahead-marked for a held first sentence), notDelivered is the start-error
// mapping (the sentinel, or a look-ahead abort), and checkCtx — when non-nil —
// is the ctx liveness is judged by (dctx otherwise; they only differ by carried
// values, never by cancellation).
func (t *turnRun) run(dctx context.Context, rep Reply, notDelivered error, checkCtx context.Context) (err error) {
	// The sentence's outcome is final at EVERY return below, so the resolution
	// hook (#626) is fired by one exit wrapper rather than per branch: a producer
	// waiting on it (the agent's history commit) would deadlock on a leaked exit.
	if rep.OnResolved != nil {
		defer func() { rep.OnResolved(OutcomeOf(err)) }()
	}
	if checkCtx == nil {
		checkCtx = dctx
	}
	if err := checkCtx.Err(); err != nil {
		return err // cut before the sentence: never reaches the synthesizer
	}
	t.mark(func() { t.attempted = true })
	if err := t.synth(dctx, rep.Sentence, rep.Voice); err != nil {
		if t.onError != nil {
			t.onError(err)
		}
		// A cancelled ctx surfaced as a synth error is a CUT, not a synth fault:
		// the cutter (barge/mute/supersede) owns the terminal, so ttsFailed stays
		// unset and the producer is stopped with the ctx error.
		if cerr := checkCtx.Err(); cerr != nil {
			return cerr
		}
		// Start-error (#362) or mid-stream stream failure (#436) under a LIVE ctx:
		// the sentence was not (fully) delivered — do not fire the hook, keep the
		// turn alive. Either way the room did not hear this sentence's whole text,
		// so per ADR-0012's under-report bias it must not commit.
		t.mark(func() { t.ttsFailed = true })
		return notDelivered
	}
	// Deliver-then-commit re-check (ADR-0012): the synth returns nil even when a
	// barge/mute cancelled the turn DURING the drain. The forward boundary is
	// unobservable here, so a cancel-during-drain is AMBIGUOUS — treated as
	// undelivered (under-report bias) and the hook stays uninvoked.
	if err := checkCtx.Err(); err != nil {
		return err
	}
	// Delivered: fire the producer's per-Reply commit hook at the ADR-0012
	// commit point (synth nil AND post-drain ctx live). Nil hook is a no-op.
	if rep.OnDelivered != nil {
		rep.OnDelivered()
	}
	return nil
}

// finish maps the producer's returned error plus the turn's accumulated state to
// the terminal [voiceevent.TurnEndReason] for a turn that failed OF ITS OWN
// ERROR — empty for a clean turn and for a cut one (the cutter publishes its own
// terminal). Ordering: cut first, then the two sentinels, then the generic
// provider_error, so neither sentinel masks the other nor a cancel.
func (t *turnRun) finish(producerErr error) voiceevent.TurnEndReason {
	t.mu.Lock()
	ttsFailed := t.ttsFailed
	t.mu.Unlock()
	if producerErr != nil && t.ctx.Err() == nil {
		// A text-delivered turn (#299) is a SUCCESS that dispatched no TTS: report
		// its terminal reason so the subscriber records text_delivered, not an
		// abandoned/no_first_audio TTL reap, and do NOT surface it as an error.
		if errors.Is(producerErr, ErrTextDelivered) {
			return voiceevent.TurnEndTextDelivered
		}
		// A producer that leaks ErrNotDelivered as its own return (#362) must NOT
		// be misclassified as a provider failure: ttsFailed is already set, so the
		// tts_error branch below owns the reason. Only a genuine producer error
		// under a live ctx is provider_error.
		if !errors.Is(producerErr, ErrNotDelivered) {
			if t.onError != nil {
				t.onError(producerErr)
			}
			return voiceevent.TurnEndProviderError
		}
	}
	if ttsFailed && t.ctx.Err() == nil {
		return voiceevent.TurnEndTTSError
	}
	return ""
}

// mark applies a state mutation to the turn's accumulated flags under the lock.
// Every write goes through it so the pipeline's concurrent sentences (#626) and
// the terminal read in [turnRun.finish] cannot race.
func (t *turnRun) mark(fn func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fn()
}

// pipelinedTurn is the depth-1 pre-synthesis pipeline over a [turnRun] (#626): it
// keeps the NEXT sentence synthesizing — held in the pump's look-ahead lane
// (ADR-0025) — while the current one is delivered, so an ordinary inter-sentence
// gap collapses from one cold TTS TTFB (~1.5 s live) to pacing overhead.
//
// One sentence is in flight and at most ONE more is held, so the structural cap is
// a single unplayed synthesized sentence per session at any instant — the TTS
// spend bound: a discarded look-ahead is metered exactly like the one-sentence
// ensemble pre-render it reuses (ADR-0045/0054), and so stays visible in the
// Usage Ledger.
//
// The protocol per [pipelinedTurn.dispatch] of sentence k (k >= 2):
//  1. spawn k's [turnRun.run] under a look-ahead-marked ctx keyed to k — the pump
//     HOLDS it (zero buffering: the tee stays blocked on its send), so k's TTFB
//     burns during k-1's playback;
//  2. wait for k-1 to resolve — that is what makes the release points sequential
//     and therefore deterministic (ADR-0021/0033);
//  3. announce k ([TTS.PublishInvoked]) and release the lane, so a discarded
//     sentence was never announced (ADR-0040: no Line for audio the room never
//     heard) and k plays with the queue still empty at enqueue (lane depth 1).
//
// The return value is CONTROL FLOW for the producer, never a commit signal: it
// carries k-1's outcome under the unchanged three-class contract ([OutcomeOf]), so
// nil / [ErrNotDelivered] mean "keep producing" and anything else means "stop".
// Committing stays hook-only ([Reply.OnDelivered], fired at the ADR-0012 commit
// point, possibly on a pipeline goroutine); a producer that must read what was
// delivered waits for [Reply.OnResolved].
type pipelinedTurn struct {
	t      *turnRun
	turnID string
	lane   LookaheadPump
	invoke func(turnID, sentence string)

	// seq numbers the per-sentence lane keys within the turn. The lane is keyed per
	// SENTENCE (#626): a turn-keyed lane would let sentence k+1 consume k's release
	// latch and leapfrog the sentence still playing.
	seq int
	// pending is the sentence dispatched but not yet resolved (the one being
	// delivered). Owned by the producer goroutine, the only caller of
	// dispatch/flush.
	pending *pipelinedSentence
}

// pipelinedSentence is one in-flight sentence of a pipelined turn: its lane key,
// its dispatch goroutine's result, and the channel closed when that result is
// final. key is empty for the turn's FIRST sentence, which is never held.
type pipelinedSentence struct {
	key  string
	err  error
	done chan struct{}
}

// newPipelinedTurn builds the [Replier]'s pipelined turn for turnID: one turn over
// its TTS stage, its look-ahead pump, and its deferred TTSInvoked publisher. It
// requires a non-nil [Replier.lookahead] — without a pump there is no lane to hold
// a pre-synthesized sentence in, and the legacy [turnRun] path applies.
func (r *Replier) newPipelinedTurn(ctx context.Context, turnID string) *pipelinedTurn {
	return newPipelined(r.newTurn(ctx), turnID, r.lookahead, r.tts.PublishInvoked)
}

// newPipelined is the testable core: the turn state machine, the lane, and the
// release-time announce, with no [Replier] around them.
func newPipelined(t *turnRun, turnID string, lane LookaheadPump, invoke func(turnID, sentence string)) *pipelinedTurn {
	return &pipelinedTurn{t: t, turnID: turnID, lane: lane, invoke: invoke}
}

// dispatch is the func(Reply) error callback handed to producers, pipelined. See
// [pipelinedTurn] for the protocol; the returned error is the PREVIOUS sentence's
// three-class outcome (the first sentence returns nil — nothing has resolved yet,
// so the producer simply keeps going).
func (p *pipelinedTurn) dispatch(rep Reply) error {
	// The FIRST sentence takes the ordinary queue: nothing is playing for it to be
	// held behind, and holding it would delay first audio (the response-latency
	// SLO) for no gain.
	if p.pending == nil {
		p.pending = p.spawn(p.t.ctx, rep, "")
		return nil
	}
	p.seq++
	key := p.turnID + "#" + strconv.Itoa(p.seq+1)
	next := p.spawn(voiceevent.WithPlaybackLookahead(voiceevent.WithLookaheadKey(p.t.ctx, key)), rep, key)

	prev := p.pending
	<-prev.done
	p.pending = next
	switch OutcomeOf(prev.err) {
	case SentenceCut:
		// The turn was cut (barge/mute, ADR-0027) while the newly held sentence was
		// pre-rendering: drop its audio unplayed and unannounced, and stop the
		// producer. Its own run unwinds on the same cancelled ctx.
		p.lane.DiscardLookahead(key)
		return prev.err
	case SentenceNotDelivered:
		// The previous sentence never reached the lane (a TTS start-error), so the
		// release issued for it LATCHED in the pump. Clear that latch by its key
		// before releasing this one — a stale latch would let a later sentence
		// bypass the lane and leapfrog the sentence being played.
		if prev.key != "" {
			p.lane.DiscardLookahead(prev.key)
		}
		p.release(rep.Sentence, key)
		return ErrNotDelivered
	default:
		p.release(rep.Sentence, key)
		return nil
	}
}

// spawn starts one sentence's dispatch on its own goroutine under dctx, judging
// liveness by the turn ctx (they differ only by carried values). The lane key is
// empty for an unheld sentence.
func (p *pipelinedTurn) spawn(dctx context.Context, rep Reply, key string) *pipelinedSentence {
	s := &pipelinedSentence{key: key, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		s.err = p.t.run(dctx, rep, ErrNotDelivered, p.t.ctx)
	}()
	return s
}

// release announces the held sentence and hands it to the play queue, in that
// order: TTSInvoked is deferred to the release point (#375, ADR-0040) so a
// sentence a barge discards is never announced and no Line is persisted for audio
// the room never heard.
func (p *pipelinedTurn) release(sentence, key string) {
	if p.invoke != nil {
		p.invoke(p.turnID, sentence)
	}
	p.lane.ReleaseLookahead(key)
}

// flush ends the pipeline: it awaits the tail sentence's resolution and drops any
// lane entry still keyed to it. The tail needs no release — a sentence is released
// by its OWN dispatch call — so the discard here is a keyed no-op on every clean
// path and the safety net on a cut one. It MUST run on every exit path and BEFORE
// [pipelinedTurn.finish], whose terminal reason depends on state the tail may
// still be setting. Its return carries the tail's outcome.
func (p *pipelinedTurn) flush() error {
	if p.pending == nil {
		return nil
	}
	<-p.pending.done
	if p.pending.key != "" {
		p.lane.DiscardLookahead(p.pending.key)
	}
	return p.pending.err
}

// finish maps the producer's return to the turn's terminal reason, exactly as the
// unpipelined [turnRun.finish] does.
func (p *pipelinedTurn) finish(producerErr error) voiceevent.TurnEndReason {
	return p.t.finish(producerErr)
}
