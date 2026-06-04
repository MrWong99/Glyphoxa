package wire

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// fakePlayback is a programmable playback: the test closes done with a chosen
// error to drive PlaySentence's block-until-Done path.
type fakePlayback struct {
	done chan struct{}
	err  error
}

func (f *fakePlayback) Done() <-chan struct{} { return f.done }
func (f *fakePlayback) Err() error            { return f.err }

func (f *fakePlayback) finish(err error) {
	f.err = err
	close(f.done)
}

// fakePlayer records each Play and publishes the created playback over a
// buffered channel, so the test goroutine observes it without racing the Play
// goroutine on a shared field.
type fakePlayer struct {
	err     error // if set, Play fails
	started chan *fakePlayback
	plays   atomic.Int64
}

func newFakePlayer() *fakePlayer {
	return &fakePlayer{started: make(chan *fakePlayback, 8)}
}

func (p *fakePlayer) Play(_ context.Context, _ gxvoice.Source) (playback, error) {
	if p.err != nil {
		return nil, p.err
	}
	p.plays.Add(1)
	pb := &fakePlayback{done: make(chan struct{})}
	p.started <- pb
	return pb, nil
}

// waitPlay returns the next playback the fake handed out, failing on timeout.
func (p *fakePlayer) waitPlay(t *testing.T) *fakePlayback {
	t.Helper()
	select {
	case pb := <-p.started:
		return pb
	case <-time.After(time.Second):
		t.Fatal("Play was never called")
		return nil
	}
}

// fakeSource is a no-op gxvoice.Source; PlaySentence never pulls it (the fake
// player does not run disgo's sender), it only needs to exist.
type fakeSource struct{}

func (fakeSource) NextFrame(context.Context) ([]byte, error) { return nil, nil }

// fakeCodec returns a fakeSource for PlaybackSource, standing in for the real
// transcoder so the pump is testable without libopus.
type fakeCodec struct{ err error }

func (c fakeCodec) DecodeInbound(gxvoice.Frame) ([]audio.Frame, error) { return nil, nil }
func (c fakeCodec) PlaybackSource(<-chan tts.AudioChunk) (gxvoice.Source, error) {
	if c.err != nil {
		return nil, c.err
	}
	return fakeSource{}, nil
}

func chunkChan() <-chan tts.AudioChunk {
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch
}

// TestPlaySentence_BlocksUntilDone is the load-bearing test: PlaySentence must
// not return until the playback's Done closes, or sequential sentences would
// auto-interrupt each other (Session.Play stops the current playback).
func TestPlaySentence_BlocksUntilDone(t *testing.T) {
	p := newFakePlayer()
	errc := make(chan error, 1)
	go func() {
		errc <- playSentence(context.Background(), p, fakeCodec{}, chunkChan())
	}()
	pb := p.waitPlay(t)

	// While Done is open, PlaySentence must still be blocked.
	select {
	case <-errc:
		t.Fatal("PlaySentence returned before playback Done closed")
	case <-time.After(50 * time.Millisecond):
	}

	// Finish the playback cleanly; PlaySentence must now return nil.
	pb.finish(nil)
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("clean sentence: got %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("PlaySentence did not return after Done closed")
	}
	if p.plays.Load() != 1 {
		t.Fatalf("Play called %d times, want 1", p.plays.Load())
	}
}

func TestPlaySentence_InterruptedReturnsErrInterrupted(t *testing.T) {
	p := newFakePlayer()
	errc := make(chan error, 1)
	go func() {
		errc <- playSentence(context.Background(), p, fakeCodec{}, chunkChan())
	}()
	// Wait for Play to register, then finish as interrupted (barge-in).
	p.waitPlay(t).finish(gxvoice.ErrInterrupted)
	select {
	case err := <-errc:
		if !errors.Is(err, gxvoice.ErrInterrupted) {
			t.Fatalf("got %v, want ErrInterrupted (unwrapped)", err)
		}
	case <-time.After(time.Second):
		t.Fatal("PlaySentence did not return")
	}
}

func TestPlaySentence_UnderlyingErrorWrapped(t *testing.T) {
	sentinel := errors.New("encoder blew up")
	p := newFakePlayer()
	errc := make(chan error, 1)
	go func() {
		errc <- playSentence(context.Background(), p, fakeCodec{}, chunkChan())
	}()
	p.waitPlay(t).finish(sentinel)
	select {
	case err := <-errc:
		if !errors.Is(err, sentinel) {
			t.Fatalf("got %v, want wrapped sentinel", err)
		}
		if errors.Is(err, gxvoice.ErrInterrupted) {
			t.Fatal("a real error must not read as ErrInterrupted")
		}
	case <-time.After(time.Second):
		t.Fatal("PlaySentence did not return")
	}
}

func TestPlaySentence_CodecUnavailableSurfaces(t *testing.T) {
	// The default-build stub Codec reports ErrCodecUnavailable; PlaySentence must
	// surface it (fail-fast) rather than silently play nothing.
	err := playSentence(context.Background(), newFakePlayer(), UnavailableCodec(), chunkChan())
	if !errors.Is(err, ErrCodecUnavailable) {
		t.Fatalf("got %v, want ErrCodecUnavailable", err)
	}
}

func TestPlaySentence_PlayErrorSurfaces(t *testing.T) {
	boom := errors.New("session closed")
	p := newFakePlayer()
	p.err = boom
	err := playSentence(context.Background(), p, fakeCodec{}, chunkChan())
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want wrapped play error", err)
	}
}

func TestPlaySentence_NilChunksRejected(t *testing.T) {
	if err := playSentence(context.Background(), newFakePlayer(), fakeCodec{}, nil); err == nil {
		t.Fatal("nil chunks should be rejected")
	}
}
