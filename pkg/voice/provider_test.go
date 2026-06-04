package voice

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingSource yields n frames then io.EOF, honouring ctx cancellation.
type countingSource struct {
	n    int
	sent atomic.Int64
}

func (s *countingSource) NextFrame(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.sent.Add(1) > int64(s.n) {
		return nil, io.EOF
	}
	return []byte{0x01}, nil
}

// erroringSource fails on the first frame with a sentinel error.
type erroringSource struct{ err error }

func (s erroringSource) NextFrame(context.Context) ([]byte, error) { return nil, s.err }

func newSlot(src Source) *playSlot {
	ctx, cancel := context.WithCancel(context.Background())
	return newTestPlaySlot(newPlayback(cancel), src, ctx)
}

func TestSwitchingProvider(t *testing.T) {
	t.Run("idle returns silence not error", func(t *testing.T) {
		p := newTestSwitchingProvider()
		frame, err := p.ProvideOpusFrame()
		if err != nil || frame != nil {
			t.Fatalf("idle: got (%x, %v) want (nil, nil)", frame, err)
		}
	})

	t.Run("clean EOF finishes playback with nil err", func(t *testing.T) {
		p := newTestSwitchingProvider()
		slot := newSlot(&countingSource{n: 2})
		p.testSwap(slot)

		for range 2 {
			if frame, err := p.ProvideOpusFrame(); err != nil || frame == nil {
				t.Fatalf("expected a frame, got (%x, %v)", frame, err)
			}
		}
		// Next poll hits EOF, retires the slot, returns silence.
		if frame, err := p.ProvideOpusFrame(); err != nil || frame != nil {
			t.Fatalf("post-EOF: got (%x, %v) want (nil, nil)", frame, err)
		}
		<-slot.pb.Done()
		if err := slot.pb.Err(); err != nil {
			t.Fatalf("clean EOF: got %v want nil", err)
		}
		// Slot cleared: further polls stay silent.
		if frame, err := p.ProvideOpusFrame(); err != nil || frame != nil {
			t.Fatalf("after clear: got (%x, %v) want (nil, nil)", frame, err)
		}
	})

	t.Run("cancelled slot finishes as interrupted", func(t *testing.T) {
		p := newTestSwitchingProvider()
		slot := newSlot(&countingSource{n: 100})
		p.testSwap(slot)
		slot.pb.cancel() // simulate Stop/ctx cancellation
		if frame, err := p.ProvideOpusFrame(); err != nil || frame != nil {
			t.Fatalf("got (%x, %v) want (nil, nil)", frame, err)
		}
		<-slot.pb.Done()
		if err := slot.pb.Err(); !errors.Is(err, ErrInterrupted) {
			t.Fatalf("got %v want ErrInterrupted", err)
		}
	})

	t.Run("source error propagates to Err", func(t *testing.T) {
		sentinel := errors.New("boom")
		p := newTestSwitchingProvider()
		slot := newSlot(erroringSource{err: sentinel})
		p.testSwap(slot)
		_, _ = p.ProvideOpusFrame()
		<-slot.pb.Done()
		if err := slot.pb.Err(); !errors.Is(err, sentinel) {
			t.Fatalf("got %v want sentinel", err)
		}
	})

	t.Run("swap returns displaced slot for caller to retire", func(t *testing.T) {
		p := newTestSwitchingProvider()
		first := newSlot(&countingSource{n: 100})
		p.testSwap(first)

		second := newSlot(&countingSource{n: 100})
		prev := p.testSwap(second)
		if prev != first {
			t.Fatal("swap did not return the displaced slot")
		}
		// Session.Play retires the displaced playback; emulate that and assert
		// the now-active second slot still produces frames.
		prev.pb.finish(ErrInterrupted)
		<-first.pb.Done()
		if err := first.pb.Err(); !errors.Is(err, ErrInterrupted) {
			t.Fatalf("displaced: got %v want ErrInterrupted", err)
		}
		if frame, err := p.ProvideOpusFrame(); err != nil || frame == nil {
			t.Fatalf("active slot after swap: got (%x, %v) want a frame", frame, err)
		}
	})

	t.Run("Close retires in-flight playback", func(t *testing.T) {
		p := newTestSwitchingProvider()
		slot := newSlot(&countingSource{n: 100})
		p.testSwap(slot)
		p.Close()
		<-slot.pb.Done()
		if err := slot.pb.Err(); !errors.Is(err, ErrInterrupted) {
			t.Fatalf("got %v want ErrInterrupted", err)
		}
	})
}

// TestSwitchingProviderRace hammers concurrent swaps against a tight
// ProvideOpusFrame loop (run with -race). Every Playback's Done must close and
// every Err must be either nil (clean EOF) or ErrInterrupted (swapped out) —
// never a leaked, never-finished playback.
func TestSwitchingProviderRace(t *testing.T) {
	p := newTestSwitchingProvider()

	const swaps = 200
	slots := make([]*playSlot, swaps)
	for i := range slots {
		slots[i] = newSlot(&countingSource{n: 3})
	}

	stop := make(chan struct{})
	var loop sync.WaitGroup
	loop.Add(1)
	go func() {
		defer loop.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = p.ProvideOpusFrame()
			}
		}
	}()

	for _, slot := range slots {
		if prev := p.swap(slot); prev != nil {
			// Same contract as Session.Play: the displacer interrupts the displaced.
			prev.pb.Stop()
		}
		time.Sleep(time.Microsecond)
	}
	// Let the final slot run to EOF, then tear down.
	time.Sleep(5 * time.Millisecond)
	close(stop)
	loop.Wait()
	p.Close()

	for i, slot := range slots {
		select {
		case <-slot.pb.Done():
		case <-time.After(time.Second):
			t.Fatalf("slot %d: Done never closed (leaked playback)", i)
		}
		if err := slot.pb.Err(); err != nil && !errors.Is(err, ErrInterrupted) {
			t.Fatalf("slot %d: unexpected err %v", i, err)
		}
	}
}
