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
// start-error under a LIVE turn ctx: the sentence was NOT delivered (its audio
// never started), so the producer must NOT commit it — but the turn is still
// alive, so the producer keeps going with later sentences. It is distinct from a
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

	// attempted: some dispatch got past the pre-check (audio may exist) — the
	// Cross-talk barge terminal keys on it (a barge before the first sentence
	// interrupted nothing).
	attempted bool
	// ttsFailed: some sentence start-errored under a live ctx — sticky, so a
	// turn that never recovers with audio terminates tts_error.
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
func (t *turnRun) run(dctx context.Context, rep Reply, notDelivered error, checkCtx context.Context) error {
	if checkCtx == nil {
		checkCtx = dctx
	}
	if err := checkCtx.Err(); err != nil {
		return err // cut before the sentence: never reaches the synthesizer
	}
	t.attempted = true
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
		// Start-error under a LIVE ctx (#362): the sentence never produced audio,
		// so it was NOT delivered — do not fire the hook, keep the turn alive.
		t.ttsFailed = true
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
	if t.ttsFailed && t.ctx.Err() == nil {
		return voiceevent.TurnEndTTSError
	}
	return ""
}
