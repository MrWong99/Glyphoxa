package voice

import (
	"context"
	"errors"
	"io"
	"sync/atomic"

	"github.com/disgoorg/disgo/voice"
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

	// closed flips once, in Close, after [Session.Close] has torn the transport
	// down. From then on ProvideOpusFrame stops reporting idle and hands the
	// sender real silence frames instead — the poke that makes disgo's sender
	// goroutine touch the dead transport, hit its terminal error, and shut
	// itself down (issue #579); see ProvideOpusFrame.
	closed atomic.Bool
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
// non-nil error to "stop" the sender (disgo only logs it and keeps polling);
// the sender lives as long as the Session.
//
// On EOF, cancellation, or a source error the playback is finished (idempotent)
// and its slot is cleared, after which we report silence until the next swap.
//
// After Close the answer changes: the provider hands back a REAL Opus silence
// frame every poll instead of the idle (nil, nil). disgo's conn.Close tears
// down the gateway/UDP but never closes its AudioSender, and a quiescent
// sender (silent frames exhausted, speaking-stop sent) that keeps reading
// len==0 never touches the network again — so it can never observe the closed
// transport and never exits: one goroutine waking 50×/s per closed Session,
// forever (issue #579). A non-empty frame forces the sender to write, the
// write on the torn-down transport fails with net.ErrClosed (or SetSpeaking
// with ErrGatewayNotConnected), and the sender's own handleErr shuts it down —
// the only teardown path disgo gives us from this side of the API.
func (p *switchingProvider) ProvideOpusFrame() ([]byte, error) {
	if p.closed.Load() {
		return voice.SilenceAudioFrame, nil
	}

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

// Close is called by [Session.Close] after the connection is torn down. It
// retires any in-flight playback as interrupted so [Playback.Done] always
// closes, and flips the provider into its sender-reaping state: every
// subsequent ProvideOpusFrame poll returns a real silence frame so disgo's
// sender goroutine — which conn.Close does NOT stop — writes to the dead
// transport, errors terminally, and exits (issue #579). Callers must therefore
// only Close AFTER the transport is down, or the poke frames would be audible.
func (p *switchingProvider) Close() {
	p.closed.Store(true)
	if slot := p.slot.Swap(nil); slot != nil {
		slot.pb.finish(ErrInterrupted)
	}
}
