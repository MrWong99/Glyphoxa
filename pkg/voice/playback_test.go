package voice

import (
	"context"
	"errors"
	"testing"
)

func TestPlayback(t *testing.T) {
	t.Run("clean finish reports nil err", func(t *testing.T) {
		pb := newPlayback(func() {})
		pb.finish(nil)
		<-pb.Done()
		if err := pb.Err(); err != nil {
			t.Fatalf("got %v want nil", err)
		}
	})

	t.Run("finish is first-wins and idempotent", func(t *testing.T) {
		pb := newPlayback(func() {})
		pb.finish(ErrInterrupted)
		pb.finish(errors.New("later, ignored"))
		if err := pb.Err(); !errors.Is(err, ErrInterrupted) {
			t.Fatalf("got %v want ErrInterrupted", err)
		}
	})

	t.Run("Stop cancels, blocks until done, idempotent", func(t *testing.T) {
		// Mirror production: the cancel func is a real (idempotent) context
		// CancelFunc, and a provider-like goroutine finishes once it fires.
		ctx, cancel := context.WithCancel(context.Background())
		pb := newPlayback(cancel)
		go func() {
			<-ctx.Done()
			pb.finish(ErrInterrupted)
		}()
		pb.Stop()
		pb.Stop() // second call must not panic or block
		select {
		case <-pb.Done():
		default:
			t.Fatal("Done not closed after Stop")
		}
		if err := pb.Err(); !errors.Is(err, ErrInterrupted) {
			t.Fatalf("got %v want ErrInterrupted", err)
		}
	})

	t.Run("Err is nil before done", func(t *testing.T) {
		pb := newPlayback(func() {})
		if err := pb.Err(); err != nil {
			t.Fatalf("got %v want nil before finish", err)
		}
	})
}
