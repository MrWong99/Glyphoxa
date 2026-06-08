package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// bytesReader is a tiny alias so test call sites read clearly.
func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// framed encodes frames as the big-endian-uint16-length-prefixed stream
// OpusReader expects.
func framed(frames ...[]byte) []byte {
	var buf bytes.Buffer
	for _, f := range frames {
		var hdr [2]byte
		binary.BigEndian.PutUint16(hdr[:], uint16(len(f)))
		buf.Write(hdr[:])
		buf.Write(f)
	}
	return buf.Bytes()
}

func TestOpusReader(t *testing.T) {
	t.Run("reads frames then EOF", func(t *testing.T) {
		want := [][]byte{{0x01, 0x02, 0x03}, {0xAA}, {}}
		src := OpusReader(bytes.NewReader(framed(want...)))
		for i, w := range want {
			got, err := src.NextFrame(context.Background())
			if err != nil {
				t.Fatalf("frame %d: unexpected err: %v", i, err)
			}
			if !bytes.Equal(got, w) {
				t.Fatalf("frame %d: got %x want %x", i, got, w)
			}
		}
		if _, err := src.NextFrame(context.Background()); !errors.Is(err, io.EOF) {
			t.Fatalf("after last frame: got %v want io.EOF", err)
		}
	})

	t.Run("empty stream is immediate EOF", func(t *testing.T) {
		src := OpusReader(bytes.NewReader(nil))
		if _, err := src.NextFrame(context.Background()); !errors.Is(err, io.EOF) {
			t.Fatalf("got %v want io.EOF", err)
		}
	})

	t.Run("truncated length prefix is an error", func(t *testing.T) {
		src := OpusReader(bytes.NewReader([]byte{0x00})) // 1 of 2 header bytes
		_, err := src.NextFrame(context.Background())
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("got %v want ErrUnexpectedEOF", err)
		}
	})

	t.Run("truncated payload is an error", func(t *testing.T) {
		data := append([]byte{0x00, 0x04}, 0x01, 0x02) // claims 4 bytes, supplies 2
		src := OpusReader(bytes.NewReader(data))
		_, err := src.NextFrame(context.Background())
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("got %v want ErrUnexpectedEOF", err)
		}
	})

	t.Run("cancelled ctx short-circuits before read", func(t *testing.T) {
		src := OpusReader(bytes.NewReader(framed([]byte{0x01})))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := src.NextFrame(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v want context.Canceled", err)
		}
	})
}

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
