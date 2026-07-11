package orchestrator

import (
	"context"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// replayFloorKey is the floor holder id the [ClipReplay] reactor takes the shared
// barge-in floor under (#310). It is a constant sentinel — NOT an Agent id — so a
// Highlight replay is one floor-holding unit a human barge can yield, and the
// coalesce window (which keys on holder id) never folds a replay into an Agent's
// live turn.
const replayFloorKey = "highlight-replay"

// ClipLoader fetches + decodes a Session Highlight clip by its blob key into
// playable audio chunks (ADR-0005: the [voiceevent.ReplayRequested] carries the
// KEY, never audio; this resolves it). Production binds it to blob.Get +
// mixdown.DecodeWAV in wirenpc; tests inject a closure. An error (a missing or
// corrupt clip) ends the replay turn without playing anything.
type ClipLoader func(ctx context.Context, clipKey string) ([]tts.AudioChunk, error)

// ClipSink consumes one replay's decoded chunks, mirroring wire.PlaybackSink's
// HandleSentence: it enqueues the chunk channel for the outbound playback path and
// returns promptly, draining the channel on its own goroutine. Production binds it
// to the live PlaybackPump; tests inject a fake.
type ClipSink func(ctx context.Context, chunks <-chan tts.AudioChunk)

// ClipReplay is the [Reactor] that plays a promoted Session Highlight's clip into
// the live voice channel on a [voiceevent.ReplayRequested] (#310, ADR-0051 GM-only
// sharing). It mirrors [DirectSpeech]'s floor mechanics VERBATIM: the turn runs on
// its own goroutine under the SAME shared [Floor], so a human barge yields that
// floor and cancels the clip mid-playback exactly as it cancels a /say or an LLM
// turn.
//
// Two deliberate divergences from DirectSpeech:
//   - There is NO TurnGate: a replay spends zero provider money (the audio is
//     pre-recorded), so the spend soft cap never refuses it.
//   - It runs no TTS: the clip's decoded chunks are pushed straight to the playback
//     sink, so it publishes no [voiceevent.TTSInvoked] and produces NO transcript
//     line (the relay projects lines off TTSInvoked and ignores [voiceevent.FirstOpus]).
//     The ReplayRequested TurnID IS threaded into the sink ctx — but only so the
//     pump publishes FirstOpus → the floor marks the turn speaking → a human barge
//     can fire (barge-in only fights an AUDIBLE holder, ADR-0027); the resulting
//     orphan FirstOpus (a turn the metrics subscriber never opened) is safely
//     ignored there.
type ClipReplay struct {
	load    ClipLoader
	sink    ClipSink
	onError ErrorFunc

	// floor, when non-nil, is the SHARED barge-in floor the replay runs under so a
	// human interruption cancels it (ADR-0027). Set by [Conversation.Register] from
	// the same floor the barge path uses; nil dispatches synchronously (no barge).
	floor *Floor
}

// NewClipReplay wires load + sink together. Both must be non-nil; passing nil for
// either panics. onError may be nil (a [ClipLoader] failure is then dropped
// silently, mirroring [NewDirectSpeech]).
func NewClipReplay(load ClipLoader, sink ClipSink, onError ErrorFunc) *ClipReplay {
	if load == nil {
		panic("orchestrator.NewClipReplay: load must not be nil")
	}
	if sink == nil {
		panic("orchestrator.NewClipReplay: sink must not be nil")
	}
	return &ClipReplay{load: load, sink: sink, onError: onError}
}

// Bind subscribes the reactor to [voiceevent.ReplayRequested] on bus and returns a
// function that removes the subscription. It implements [Reactor]; bus must be
// non-nil.
func (r *ClipReplay) Bind(ctx context.Context, bus *voiceevent.Bus) (cancel func()) {
	if bus == nil {
		panic("orchestrator.ClipReplay.Bind: bus must not be nil")
	}
	return voiceevent.On(bus, func(e voiceevent.ReplayRequested) {
		// Thread the turn correlation id so the pump's FirstOpus fires for this turn
		// (floor marks speaking → barge-able), installed before the floor is taken so
		// both the sync and barge branches inherit it — exactly like DirectSpeech.
		turnCtx := voiceevent.WithTurnID(ctx, e.TurnID)

		// No floor wired (voice standalone / bench): dispatch synchronously on the bus
		// goroutine, mirroring the DirectSpeech no-floor branch.
		if r.floor == nil {
			r.dispatch(turnCtx, e.ClipKey)
			return
		}

		// Barge-in: take the shared floor and run the replay on its own goroutine so
		// the inbound loop keeps feeding VAD during playback, and a barge yielding the
		// floor cancels floorCtx (stopping the clip). Every replay takes the floor under
		// the SAME constant key, so a GM double-click within the floor's coalesce window
		// folds the second replay: it is dropped silently (release + return) while its
		// ShareHighlight RPC still returns success — an accepted de-dupe (a rapid repeat
		// replays once, not twice), not an error path.
		floorCtx, release, coalesced := r.floor.Take(turnCtx, replayFloorKey)
		if coalesced {
			release()
			return
		}
		go func() {
			defer release()
			r.dispatch(floorCtx, e.ClipKey)
		}()
	})
}

// dispatch loads the clip and streams its chunks to the playback sink under ctx.
// A load error ends the turn without touching the sink (the floor is released by
// the caller's defer). Chunks are produced on THIS goroutine — the sink pulls on
// its own — respecting a barge that cancels ctx mid-clip.
func (r *ClipReplay) dispatch(ctx context.Context, clipKey string) {
	if ctx.Err() != nil {
		return
	}
	chunks, err := r.load(ctx, clipKey)
	if err != nil {
		if r.onError != nil {
			r.onError(err)
		}
		return
	}
	if len(chunks) == 0 {
		return
	}

	ch := make(chan tts.AudioChunk)
	// Hand the channel to the sink first (it enqueues + drains asynchronously), then
	// feed it: the sends block until the sink pulls, so this call holds the floor for
	// the clip's duration exactly as TTS.Dispatch does for a /say.
	r.sink(ctx, ch)
	for _, c := range chunks {
		select {
		case <-ctx.Done():
			close(ch) // barge mid-clip: stop writing, let the sink drain-and-stop
			return
		case ch <- c:
		}
	}
	close(ch)
}
