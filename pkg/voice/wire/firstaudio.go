package wire

import (
	"context"
	"sync/atomic"
	"time"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// firstAudioSource decorates a playback [gxvoice.Source] to publish a single
// [voiceevent.FirstAudio] the moment the first frame of a HELD look-ahead sentence
// is pulled to be streamed (#375). For an ordinary sentence the tee publishes
// FirstAudio at the "available at the pump" boundary; a look-ahead sentence's audio
// is held in the pump lane until release, so the tee cannot know when it becomes
// delivery-real. This wrapper moves that signal to the wire: it fires on the first
// frame actually pulled, so a sentence that is never played (discarded on a barge)
// STRUCTURALLY never publishes — the drain paths never build or pull a source.
//
// It is a verbatim sibling of [firstOpusSource]; the two wrap the same Source in
// order (FirstAudio inner, FirstOpus outer) so both fire on the same first frame with
// FirstAudio strictly first on the return path.
type firstAudioSource struct {
	inner  gxvoice.Source
	bus    *voiceevent.Bus
	turnID string
	// fired guards the single publish. One disgo-sender goroutine pulls a Source
	// serially today, so a plain bool would be safe — atomic.Bool future-proofs it
	// against a pump that ever pulls one Source from two goroutines.
	fired atomic.Bool
	nowFn func() time.Time
}

// newFirstAudioSource wraps inner so the first frame it yields publishes
// [voiceevent.FirstAudio] for turnID on bus — but ONLY for a look-ahead sentence
// (lookahead true). For an ordinary sentence, or when bus is nil / turnID is empty,
// it returns inner unwrapped (the tee owns the ordinary FirstAudio, and the
// keyless/test path emits no signal).
func newFirstAudioSource(inner gxvoice.Source, bus *voiceevent.Bus, turnID string, lookahead bool) gxvoice.Source {
	if bus == nil || turnID == "" || !lookahead {
		return inner
	}
	return &firstAudioSource{inner: inner, bus: bus, turnID: turnID, nowFn: time.Now}
}

// NextFrame forwards to the inner Source and, on the first frame actually yielded
// (no error), publishes FirstAudio exactly once. The publish is stamped and sent
// before NextFrame returns, so it precedes the outer [firstOpusSource]'s FirstOpus
// for the same first frame.
func (s *firstAudioSource) NextFrame(ctx context.Context) ([]byte, error) {
	frame, err := s.inner.NextFrame(ctx)
	if err == nil && s.fired.CompareAndSwap(false, true) {
		s.bus.Publish(voiceevent.FirstAudio{At: s.nowFn(), TurnID: s.turnID})
	}
	return frame, err
}
