package wire

import (
	"context"
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
	inner    gxvoice.Source
	bus      *voiceevent.Bus
	turnID   string
	fired    bool
	nowFn    func() time.Time
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
	if err == nil && !s.fired {
		s.fired = true
		s.bus.Publish(voiceevent.FirstOpus{At: s.nowFn(), TurnID: s.turnID})
	}
	return frame, err
}
