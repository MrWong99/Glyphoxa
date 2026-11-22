package elevenlabs

import (
	"context"
	"errors"
	"testing"
)

// TestWSConn_NextRejectsAFrameReadUnderAnEarlierLease covers the half of the
// stray-frame guard the scripted server cannot time: a frame the read loop
// picked up while the previous sentence still held the socket (audio after that
// sentence's final marker) sits in the hand-off until the next leaseholder asks
// for its first frame. It must come back as a stray, not as that sentence's
// audio; a frame stamped with the current lease is passed through.
func TestWSConn_NextRejectsAFrameReadUnderAnEarlierLease(t *testing.T) {
	c := &wsConn{frames: make(chan wsFrame, 1), done: make(chan struct{})}
	c.leases.Store(2)

	c.frames <- wsFrame{pcm: []byte("STRAY!"), lease: 1}
	if f, err := c.next(context.Background(), nil); !errors.Is(err, errWSStray) {
		t.Fatalf("next = (%q, %v), want the stray-frame error", f.pcm, err)
	}

	c.frames <- wsFrame{pcm: []byte("mine"), lease: 2}
	if f, err := c.next(context.Background(), nil); err != nil || string(f.pcm) != "mine" {
		t.Fatalf("next = (%q, %v), want the current lease's frame", f.pcm, err)
	}
}
