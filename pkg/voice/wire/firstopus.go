package wire

import (
	"context"
	"sync/atomic"
	"time"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// firstOpusSource decorates a playback [gxvoice.Source] to publish a single
// [voiceevent.FirstOpus] the moment the first Opus frame of a turn is pulled to
// be streamed to Discord — the audible-on-wire boundary that ends the headline
// response-latency SLO (task #7 / Luk's definition). disgo's sender calls
// NextFrame every 20ms on its own goroutine, so the publish happens off that
// goroutine; the metrics subscriber locks its per-turn state.
//
// Only the FIRST non-EOF frame publishes: a turn's reply may be several
// sentences (several Sources), but the SLO ends at the first packet on the wire,
// and the per-turn dedupe lives in the subscriber (first FirstOpus per TurnID
// wins) — so wrapping every sentence's Source is fine; the later ones are
// no-ops at the metric. A Source that yields no frame (an immediately
// barge-cancelled sentence) never publishes, which is correct: nothing was
// audible.
type firstOpusSource struct {
	inner  gxvoice.Source
	bus    *voiceevent.Bus
	turnID string
	// fired guards the single publish. One disgo-sender goroutine pulls a Source
	// serially today, so a plain bool would be safe — atomic.Bool future-proofs it
	// against a pre-buffering pump that ever pulls one Source from two goroutines.
	fired atomic.Bool
	nowFn func() time.Time
}

// newFirstOpusSource wraps inner so the first frame it yields publishes
// [voiceevent.FirstOpus] for turnID on bus. If bus is nil or turnID is empty it
// returns inner unwrapped — no signal to emit, no overhead (the keyless/test
// path and any non-correlated playback).
func newFirstOpusSource(inner gxvoice.Source, bus *voiceevent.Bus, turnID string) gxvoice.Source {
	if bus == nil || turnID == "" {
		return inner
	}
	return &firstOpusSource{inner: inner, bus: bus, turnID: turnID, nowFn: time.Now}
}

// NextFrame forwards to the inner Source and, on the first frame actually yielded
// (no error), publishes FirstOpus exactly once. The publish is stamped and sent
// before NextFrame returns, so the measured moment is when the frame was handed
// to the sender, not when the next poll comes around.
func (s *firstOpusSource) NextFrame(ctx context.Context) ([]byte, error) {
	frame, err := s.inner.NextFrame(ctx)
	if err == nil && s.fired.CompareAndSwap(false, true) {
		s.bus.Publish(voiceevent.FirstOpus{At: s.nowFn(), TurnID: s.turnID})
	}
	return frame, err
}

// tappedSource decorates a playback [gxvoice.Source] to copy every Opus frame it
// yields to a tap — the rollover tape's agent-speech capture point (#306). Agent
// audio is always on tape (ADR-0051). The tap runs inline on disgo's 20 ms sender
// goroutine and must not block; only frames actually yielded (no error) are
// tapped, so a barge-cancelled sentence taps nothing.
type tappedSource struct {
	inner gxvoice.Source
	tap   func(opus []byte)
}

// newTappedSource wraps inner so each yielded frame is passed to tap. A nil tap
// returns inner unwrapped — no overhead when the tape is not armed.
func newTappedSource(inner gxvoice.Source, tap func(opus []byte)) gxvoice.Source {
	if tap == nil {
		return inner
	}
	return &tappedSource{inner: inner, tap: tap}
}

// NextFrame forwards to the inner Source and taps each frame it yields.
func (s *tappedSource) NextFrame(ctx context.Context) ([]byte, error) {
	frame, err := s.inner.NextFrame(ctx)
	if err == nil {
		s.tap(frame)
	}
	return frame, err
}
