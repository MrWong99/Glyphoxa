package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Reactor is one self-contained bus interaction in the voice pipeline: it turns
// events the call-driven stages publish (VAD, STT, TTS) into the next stage's
// call. The address detector (STTFinal → AddressRouted), the [Segmenter]
// (speech transitions → STT), and the [Replier] (AddressRouted → TTS) are the
// reactors that wire slice 1 (ADR-0019, ADR-0026).
//
// Bind installs the reactor's subscriptions on bus and returns a function that
// removes them; nothing happens until Bind is called. The ctx governs the
// reactions' lifetime and is the context handed to any STT/TTS call the reactor
// triggers — cancelling it lets an in-flight provider call unwind — but
// teardown stays explicit: cancelling ctx does not unsubscribe, only the
// returned cancel does. This mirrors [voiceevent.Bus.Subscribe], which also
// hands back its own unsubscribe func.
type Reactor interface {
	Bind(ctx context.Context, bus *voiceevent.Bus) (cancel func())
}

// Bind installs every reactor on bus and returns a single function that tears
// them all down, in reverse order. It is the "in parts" entry point: compose
// any hand-picked subset of reactors without the [Conversation] facade.
func Bind(ctx context.Context, bus *voiceevent.Bus, reactors ...Reactor) (cancel func()) {
	if bus == nil {
		panic("orchestrator.Bind: bus must not be nil")
	}
	cancels := make([]func(), len(reactors))
	for i, r := range reactors {
		cancels[i] = r.Bind(ctx, bus)
	}
	return func() {
		for i := len(cancels) - 1; i >= 0; i-- {
			cancels[i]()
		}
	}
}

// ErrorFunc reports an error from a stage call a reactor fires from inside a
// bus callback. Bus callbacks cannot return an error, so a reactor whose
// triggered call fails — the [Replier]'s [TTS.Dispatch] — surfaces it here
// instead. A nil ErrorFunc drops the error silently. (The [Segmenter]'s
// [STT.Transcribe] runs inside [Segmenter.Process] and returns its error to the
// caller directly, so it needs no ErrorFunc.)
type ErrorFunc func(error)

// Segmenter turns the VAD stage's frame-level transitions into utterance-sized
// batches for STT. It is both a frame sink and a [Reactor]: callers feed PCM
// via [Segmenter.Process], which drives the wrapped VAD stage, and
// [Segmenter.Bind] subscribes to the VADSpeechStart / VADSpeechEnd events that
// stage publishes. Frames that arrive while speech is active are buffered; the
// completed batch is handed to [STT.Transcribe] when speech ends.
//
// This is the bus-driven form of the accumulate-between-VAD-events loop the
// slice-1 pipeline test used to spell out inline (ADR-0026).
type Segmenter struct {
	vad *VAD
	stt *STT

	mu sync.Mutex
	// speechEndAt is the [voiceevent.VADSpeechEnd.At] of the most recent
	// speech-end transition, captured so the flushed utterance's STTFinal can
	// carry it forward (A3): it anchors the headline response-latency span at the
	// turn's true speech-end without the metrics subscriber guessing. Zero until
	// the first speech-end, and for a Flush with no preceding transition.
	speechEndAt time.Time
	// ctx is the context handed to STT.Transcribe when a segment flushes. It is
	// the conversation's lifetime context, captured at Bind and cleared by the
	// returned cancel; storing it lets Process stay frame-only (ctx-free) so the
	// audio loop reads as a plain range over frames.
	ctx       context.Context
	listening bool
	buf       []audio.Frame
}

// NewSegmenter wires vad and stt together. Both must be non-nil; passing nil
// for either panics. The caller owns the wrapped stages.
func NewSegmenter(vad *VAD, stt *STT) *Segmenter {
	if vad == nil {
		panic("orchestrator.NewSegmenter: vad must not be nil")
	}
	if stt == nil {
		panic("orchestrator.NewSegmenter: stt must not be nil")
	}
	return &Segmenter{vad: vad, stt: stt}
}

// Bind subscribes the segmenter to the VAD speech transitions on bus and records
// ctx as the context handed to STT.Transcribe on flush. It implements
// [Reactor]; bus must be non-nil. The subscriptions only flip the speech-active
// flag — the actual buffering and flush happen in [Segmenter.Process] so a
// recognizer error can be returned to the audio loop rather than swallowed in a
// callback.
func (s *Segmenter) Bind(ctx context.Context, bus *voiceevent.Bus) (cancel func()) {
	if bus == nil {
		panic("orchestrator.Segmenter.Bind: bus must not be nil")
	}
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()

	unsubStart := voiceevent.On(bus, func(voiceevent.VADSpeechStart) {
		s.mu.Lock()
		s.listening = true
		s.mu.Unlock()
	})
	unsubEnd := voiceevent.On(bus, func(end voiceevent.VADSpeechEnd) {
		s.mu.Lock()
		s.listening = false
		// Remember this turn's true speech-end so the flushed utterance's STTFinal
		// can carry it (A3); the next Process call after speech ends flushes.
		s.speechEndAt = end.At
		s.mu.Unlock()
	})
	return func() {
		unsubStart()
		unsubEnd()
		s.mu.Lock()
		s.ctx = nil
		s.mu.Unlock()
	}
}

// Process feeds one PCM frame through the wrapped VAD stage and accumulates
// utterance audio. The VAD stage publishes the speech transitions synchronously,
// so by the time it returns the speech-active flag is up to date: while active
// the frame is buffered; on the first frame after speech ends the buffered
// utterance is flushed to [STT.Transcribe]. The frame that ends speech is not
// part of the utterance and is not buffered.
//
// A recognizer error is returned to the caller; the buffer is cleared either
// way so a failed utterance does not bleed into the next one.
func (s *Segmenter) Process(frame audio.Frame) error {
	if err := s.vad.Process(frame); err != nil {
		return err
	}

	s.mu.Lock()
	if s.listening {
		s.buf = append(s.buf, frame)
		s.mu.Unlock()
		return nil
	}
	seg := s.buf
	s.buf = nil
	ctx := s.ctx
	speechEndAt := s.speechEndAt
	s.mu.Unlock()

	return s.transcribe(ctx, seg, speechEndAt)
}

// Flush transcribes any buffered utterance audio immediately, regardless of
// whether speech is still active, and clears the buffer. It is the
// end-of-stream counterpart to [Segmenter.Process]: when the audio loop stops
// while the speaker is still mid-utterance (the call ends, a clip is cut off
// before its trailing silence), the wrapped VAD never observes a speech-end
// transition, so the buffered final utterance would otherwise be dropped. Call
// Flush once after the last [Segmenter.Process]. With no buffered audio it is a
// no-op; the recognizer error, if any, is returned.
func (s *Segmenter) Flush() error {
	s.mu.Lock()
	seg := s.buf
	s.buf = nil
	s.listening = false
	ctx := s.ctx
	// A Flush has no speech-end transition (end-of-stream), so it carries the
	// zero time — the STTFinal's SpeechEndAt is unset for a flushed final turn.
	s.mu.Unlock()

	return s.transcribe(ctx, seg, time.Time{})
}

// transcribe hands a flushed segment to STT under ctx, carrying the turn's
// speech-end time so STT can stamp it on the published STTFinal (A3). An empty
// segment is a no-op (a speech-end with nothing buffered, or a redundant Flush).
// A nil ctx — the segmenter was never bound, or was already torn down — falls
// back to a background context so a late flush still completes rather than
// panicking.
func (s *Segmenter) transcribe(ctx context.Context, seg []audio.Frame, speechEndAt time.Time) error {
	if len(seg) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.stt.Transcribe(withSpeechEndAt(ctx, speechEndAt), seg)
}

// Reply is one thing an addressed Agent should say: a single sentence and the
// Voice to render it with. A [ReplyFunc] returns zero or more Replies per
// routing decision.
type Reply struct {
	Sentence string
	Voice    tts.Voice
}

// ReplyFunc decides what an addressed Agent says in response to one
// [voiceevent.AddressRouted] decision. Returning nil (or an empty slice) says
// nothing — the right answer when the route is not for this Agent or the turn
// has already been answered. Swapping the ReplyFunc swaps the pipeline's entire
// "what do we say back" behaviour without touching any other stage: it is the
// strategy seam of the reply reactor.
//
// ctx is the turn's context: with barge-in wired ([WithBargeIn]) it is the
// per-turn floor context, so a human reclaiming the floor cancels any LLM call
// in flight, not just the TTS/playback downstream of it. Implementations must
// honor it (and should derive their own deadline — a hung provider must not
// hold the turn forever).
//
// In v1.0 the production ReplyFunc is the Agent loop (Hot Context assembly +
// Persona injection + LLM dispatch, ADR-0019 slice 1); tests supply a closure
// returning a canned line. Per ADR-0025 a multi-Agent address can yield an
// Ensemble Turn — the slice return type leaves room for that to grow behind the
// same seam.
type ReplyFunc func(ctx context.Context, e voiceevent.AddressRouted) []Reply

// StreamReplyFunc is the streaming counterpart to [ReplyFunc] (B1): instead of
// returning all of a turn's [Reply]s up front, it produces them incrementally —
// calling dispatch with each sentence the moment it is ready — so the first
// sentence reaches TTS (and audio begins) before the whole completion is
// generated. dispatch sends one sentence through the TTS stage and blocks until
// that sentence is synthesized (the serial, one-at-a-time contract the
// [PlaybackPump] depends on); it returns ctx.Err() if the turn was cancelled, so
// the producer can stop generating and emitting pending sentences (a mid-stream
// barge-in now cancels generation itself, not just post-hoc dispatch).
//
// ctx is the per-turn context (the barge-in floor's, under [WithBargeIn]); the
// producer must thread it into its LLM call so a cancel tears generation down.
// Returning an error reports a turn-level failure via the [ErrorFunc]; a nil
// error with no dispatch calls says nothing.
type StreamReplyFunc func(ctx context.Context, e voiceevent.AddressRouted, dispatch func(Reply) error) error

// Replier is the [Reactor] that runs a reply strategy on every
// [voiceevent.AddressRouted] and dispatches each resulting [Reply] through the
// TTS stage. It drives the streaming strategy ([StreamReplyFunc]) when one is
// set, else the batch [ReplyFunc].
type Replier struct {
	tts         *TTS
	reply       ReplyFunc
	replyStream StreamReplyFunc
	onError     ErrorFunc

	// floor, when non-nil, makes each turn run on its own goroutine under a
	// cancelable per-turn context taken from the floor — so the inbound loop is
	// not blocked for the turn's real-time playback and a [BargeIn] can cancel it
	// mid-sentence (ADR-0027). Nil keeps the default synchronous dispatch. Set
	// only via the orchestrator wiring ([WithBargeIn]); not part of [NewReplier].
	floor *Floor
}

// NewReplier wires ttsStage and reply together. Both must be non-nil; passing
// nil for either panics. onError may be nil, in which case a [TTS.Dispatch]
// failure is dropped silently.
func NewReplier(ttsStage *TTS, reply ReplyFunc, onError ErrorFunc) *Replier {
	if ttsStage == nil {
		panic("orchestrator.NewReplier: tts must not be nil")
	}
	if reply == nil {
		panic("orchestrator.NewReplier: reply must not be nil")
	}
	return &Replier{tts: ttsStage, reply: reply, onError: onError}
}

// NewStreamReplier wires ttsStage and a streaming reply strategy together (B1).
// Both must be non-nil; onError may be nil. It is the streaming twin of
// [NewReplier]: the strategy dispatches sentences as they are produced rather
// than returning them all at once.
func NewStreamReplier(ttsStage *TTS, reply StreamReplyFunc, onError ErrorFunc) *Replier {
	if ttsStage == nil {
		panic("orchestrator.NewStreamReplier: tts must not be nil")
	}
	if reply == nil {
		panic("orchestrator.NewStreamReplier: reply must not be nil")
	}
	return &Replier{tts: ttsStage, replyStream: reply, onError: onError}
}

// Bind subscribes the replier to [voiceevent.AddressRouted] on bus and returns a
// function that removes the subscription. It implements [Reactor]; bus must be
// non-nil. Each [Reply] the [ReplyFunc] returns is dispatched in order under
// ctx; a dispatch failure is reported through the ErrorFunc (callbacks cannot
// return errors) and does not stop the remaining replies.
func (r *Replier) Bind(ctx context.Context, bus *voiceevent.Bus) (cancel func()) {
	if bus == nil {
		panic("orchestrator.Replier.Bind: bus must not be nil")
	}
	return voiceevent.On(bus, func(e voiceevent.AddressRouted) {
		// Carry the turn correlation id (A3) into the dispatch context so the TTS
		// stage and the wire tee stamp the same id on TTSInvoked / FirstAudio.
		// Installed before the floor is taken so both the sync and barge-in
		// branches inherit it.
		ctx := voiceevent.WithTurnID(ctx, e.TurnID)

		// Default (no floor): dispatch synchronously on the bus goroutine — the
		// behaviour every non-barge-in caller relies on. Announce a turn that died of
		// its own error (a real TTS/provider failure) before producing audio so the
		// metrics subscriber records the precise reason, not the coarse no-first-audio
		// TTL reap (#20) — mirroring the floor branch below. The sync path has no
		// barge/supersede, so a non-cancelled ctx is the only guard needed.
		if r.floor == nil {
			if reason := r.dispatchAll(ctx, e); reason != "" && ctx.Err() == nil {
				bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: reason})
			}
			return
		}
		// Barge-in: take the floor and run the turn on its own goroutine so the
		// inbound loop keeps feeding VAD during playback. A barge cancels turnCtx,
		// which unwinds TTS synthesis and playback and breaks the dispatch loop.
		turnCtx, release, coalesced := r.floor.Take(ctx)
		if coalesced {
			// The floor's same-utterance grace window folded this late segment into
			// the turn already speaking (one utterance VAD-split into two). The
			// segment is NOT spoken; announce it so the metrics subscriber records a
			// distinct `yielded` outcome (not `abandoned`) and logs the dropped
			// transcript — the known residual until real utterance coalescing routes
			// this text into the surviving turn. No goroutine is spawned.
			release() // no-op on the floor, but keeps the take/release pairing honest
			bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: voiceevent.TurnEndSupersedeCoalesced, Text: e.Text})
			return
		}
		go func() {
			defer release()
			reason := r.dispatchAll(turnCtx, e)
			// Announce a turn that died of its own error (a real TTS/provider
			// failure) before producing audio — so the metrics subscriber records
			// the precise reason, not the coarse no-first-audio catch-all. A turn
			// cancelled by a barge or a supersede is NOT reported here (ctx.Err() !=
			// nil): the barge publishes its own TurnEnded, and the subscriber's
			// first-audio/TTL guards handle the rest. A clean turn reports nothing.
			if reason != "" && turnCtx.Err() == nil {
				bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: e.TurnID, Reason: reason})
			}
		}()
	})
}

// dispatchAll renders one routing decision under ctx, returning the turn-end
// reason if the turn failed of its own error (empty on a clean turn or a
// ctx-cancel). With a streaming strategy it drives the producer, dispatching each
// sentence as it arrives; otherwise it renders every [Reply] the batch
// [ReplyFunc] returns, in order. Both stop early if ctx is cancelled (a barge-in
// yielded the floor mid-turn). A dispatch failure is reported through the
// ErrorFunc and does not stop the rest.
func (r *Replier) dispatchAll(ctx context.Context, e voiceevent.AddressRouted) voiceevent.TurnEndReason {
	if r.replyStream != nil {
		return r.dispatchStream(ctx, e)
	}
	var reason voiceevent.TurnEndReason
	for _, rep := range r.reply(ctx, e) {
		if ctx.Err() != nil {
			return ""
		}
		if err := r.tts.Dispatch(ctx, rep.Sentence, rep.Voice); err != nil {
			if r.onError != nil {
				r.onError(err)
			}
			// A synth failure on one sentence is non-fatal to the turn, but record
			// it as the turn-end reason if no later sentence recovers with audio.
			if ctx.Err() == nil {
				reason = voiceevent.TurnEndTTSError
			}
		}
	}
	return reason
}

// dispatchStream drives the streaming reply strategy for one routing decision
// (B1), returning the turn-end reason if the turn failed of its own error (empty
// on a clean turn or a ctx-cancel). It hands the producer a dispatch callback
// that synthesizes one sentence at a time under ctx — serially, so the
// [PlaybackPump]'s single-in-flight contract and the per-sentence FirstAudio
// ordering both hold — and returns ctx.Err() once the turn is cancelled so the
// producer stops generating. A dispatch failure is reported via the ErrorFunc but
// does not abort the turn (one bad sentence must not silence the rest); a
// producer-level error is likewise surfaced.
func (r *Replier) dispatchStream(ctx context.Context, e voiceevent.AddressRouted) voiceevent.TurnEndReason {
	var ttsFailed bool
	dispatch := func(rep Reply) error {
		if err := ctx.Err(); err != nil {
			return err // barge-in cancelled the turn: stop the producer
		}
		if err := r.tts.Dispatch(ctx, rep.Sentence, rep.Voice); err != nil {
			if r.onError != nil {
				r.onError(err)
			}
			// A synth failure on one sentence is non-fatal to the turn, but a
			// cancelled ctx surfaced as a Dispatch error must still stop the producer.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			ttsFailed = true
		}
		return nil
	}
	if err := r.replyStream(ctx, e, dispatch); err != nil && ctx.Err() == nil {
		if r.onError != nil {
			r.onError(err)
		}
		// The producer (LLM round / tool loop) failed before the turn finished.
		return voiceevent.TurnEndProviderError
	}
	if ttsFailed && ctx.Err() == nil {
		return voiceevent.TurnEndTTSError
	}
	return ""
}
