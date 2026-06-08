package voice

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// ErrInterrupted is reported by [Playback.Err] when a playback was swapped out
// by a later [Session.Play] or ended by [Playback.Stop], as opposed to reaching
// the end of its [Source]. A clean end-of-source playback reports a nil error.
var ErrInterrupted = errors.New("voice: playback interrupted")

// Source yields Opus frames for outbound playback, one 20ms frame per call.
// NextFrame returns [io.EOF] once the source is exhausted; the returned slice
// is consumed before the next call and need not stay valid afterwards.
//
// Implementations should honour ctx: the [switchingProvider] passes the
// playback's context, which is cancelled on interrupt or [Playback.Stop].
type Source interface {
	// NextFrame returns the next Opus frame, or io.EOF when the source is done.
	NextFrame(ctx context.Context) ([]byte, error)
}

// OpusReader adapts an io.Reader of length-prefixed Opus frames into a [Source].
// Each frame is a big-endian uint16 byte length followed by that many bytes of
// Opus payload — the framing disgo's dca-style streams and our TTS encoder
// emit. A clean EOF at a frame boundary ends the source; EOF mid-frame is a
// truncation error.
func OpusReader(r io.Reader) Source {
	return &opusReader{r: r}
}

type opusReader struct {
	r      io.Reader
	header [2]byte
}

func (o *opusReader) NextFrame(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A clean EOF reading the length prefix means the stream ended at a frame
	// boundary; surface it verbatim so the provider treats it as done.
	if _, err := io.ReadFull(o.r, o.header[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("voice: truncated Opus length prefix: %w", io.ErrUnexpectedEOF)
		}
		return nil, err
	}
	n := binary.BigEndian.Uint16(o.header[:])
	frame := make([]byte, n)
	if _, err := io.ReadFull(o.r, frame); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("voice: truncated Opus frame (want %d bytes): %w", n, io.ErrUnexpectedEOF)
		}
		return nil, err
	}
	return frame, nil
}

// Playback is a handle to one outbound stream on a [Session]. It is created by
// [Session.Play] and becomes done when its [Source] is exhausted, it is
// [Playback.Stop]ped, or a later Play interrupts it. All methods are safe for
// concurrent use.
type Playback struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	err    atomic.Pointer[error]
}

func newPlayback(cancel context.CancelFunc) *Playback {
	return &Playback{cancel: cancel, done: make(chan struct{})}
}

// Stop interrupts the playback and blocks until it has fully finished. It is
// idempotent and safe to call after the playback has already ended. After Stop
// returns, [Playback.Err] reports [ErrInterrupted] unless the source had
// already reached a clean EOF.
//
// Stop both cancels the playback's context (so an actively-streaming source
// unblocks mid-frame) and finishes the playback itself: a playback that has
// been swapped out of the provider is no longer polled, so nothing else would
// ever close its done channel. finish is first-wins, so a clean EOF that landed
// first still reports nil.
func (p *Playback) Stop() {
	p.cancel()
	p.finish(ErrInterrupted)
	<-p.done
}

// Done returns a channel closed when the playback has finished for any reason.
func (p *Playback) Done() <-chan struct{} { return p.done }

// Err returns why the playback ended: nil for a clean end of [Source],
// [ErrInterrupted] when swapped out or stopped, or the underlying [Source]
// error otherwise. It returns nil until the playback is done.
func (p *Playback) Err() error {
	if e := p.err.Load(); e != nil {
		return *e
	}
	return nil
}

// finish records the terminal error and closes done exactly once. Subsequent
// calls (e.g. a Stop racing the source's own EOF) are no-ops, so the first
// outcome wins. A nil err means clean EOF.
func (p *Playback) finish(err error) {
	p.once.Do(func() {
		if err != nil {
			p.err.Store(&err)
		}
		close(p.done)
	})
}
