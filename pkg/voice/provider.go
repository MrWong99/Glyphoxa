package voice

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
)

// switchingProvider implements disgo's voice.OpusFrameProvider for the lifetime
// of one [Session]. disgo's audio sender polls ProvideOpusFrame every 20ms on
// its own goroutine; we hand back frames from the currently-active playback.
//
// A single instance lives for the whole Session. [Session.Play] swaps the
// active slot atomically, so the sender goroutine never has to be torn down
// between playbacks — only the slot pointer changes.
type switchingProvider struct {
	slot atomic.Pointer[playSlot]
}

// playSlot binds a [Playback] to its [Source] and the context whose
// cancellation ends it. The src field is owned by the provider goroutine once
// installed: only ProvideOpusFrame reads it.
type playSlot struct {
	pb  *Playback
	src Source
	ctx context.Context
}

// swap installs next as the active slot and returns the slot it displaced (nil
// if none). The caller is responsible for interrupting the displaced playback;
// keeping that out of swap keeps the CAS-free store a single atomic op.
func (p *switchingProvider) swap(next *playSlot) *playSlot {
	return p.slot.Swap(next)
}

// clear removes want from the active slot if it is still installed, returning
// true when it did. Used by ProvideOpusFrame to retire a finished playback
// without clobbering a concurrent swap that already replaced it.
func (p *switchingProvider) clear(want *playSlot) bool {
	return p.slot.CompareAndSwap(want, nil)
}

// ProvideOpusFrame returns the next frame from the active playback, or an empty
// slice when idle. disgo treats len==0 as silence and keeps polling, so the
// idle and post-EOF paths simply return (nil, nil) — we must never return a
// non-nil error to "stop" the sender; the sender lives as long as the Session.
//
// On EOF, cancellation, or a source error the playback is finished (idempotent)
// and its slot is cleared, after which we report silence until the next swap.
func (p *switchingProvider) ProvideOpusFrame() ([]byte, error) {
	slot := p.slot.Load()
	if slot == nil {
		return nil, nil
	}

	// A swapped-out or stopped slot's ctx is cancelled; retire it as interrupted
	// before touching the source so a cancelled NextFrame can't masquerade as a
	// source error.
	if err := slot.ctx.Err(); err != nil {
		slot.pb.finish(ErrInterrupted)
		p.clear(slot)
		return nil, nil
	}

	frame, err := slot.src.NextFrame(slot.ctx)
	if err != nil {
		switch {
		case errors.Is(err, io.EOF):
			slot.pb.finish(nil) // clean end of source
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			slot.pb.finish(ErrInterrupted)
		default:
			slot.pb.finish(err)
		}
		p.clear(slot)
		return nil, nil
	}
	return frame, nil
}

// Close is called by disgo when the connection's sender tears down. It retires
// any in-flight playback as interrupted so [Playback.Done] always closes.
func (p *switchingProvider) Close() {
	if slot := p.slot.Swap(nil); slot != nil {
		slot.pb.finish(ErrInterrupted)
	}
}
