package transcript

// SSE hardening tests (#148): Defect A — once a subscription has dropped a
// frame, no later frame may be delivered on it, so Last-Event-ID replay is
// genuinely lossless. Defect B — a stalled reader must not park the handler in
// a write forever (per-write deadline).

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// subscriberCount is a test-only probe for the live subscriber set.
func (r *Relay) subscriberCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.subs)
}

// waitFor polls cond until it holds or the bound expires.
func waitFor(t *testing.T, bound time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(bound)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// eventsMux mounts ServeEvents the way production does (cmd/glyphoxa/main.go).
func eventsMux(r *Relay) *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/sessions/{id}/events", r.ServeEvents)
	return m
}

// floodBigFrames publishes enough oversized line frames to overrun any socket
// buffer between the handler and a non-reading client, guaranteeing the
// handler ends up parked inside a frame write. ~19MB total, far past loopback
// send+receive buffering.
func floodBigFrames(bus *voiceevent.Bus) {
	big := strings.Repeat("x", 64<<10)
	for i := 0; i < 300; i++ {
		bus.Publish(voiceevent.STTFinal{At: at(i), Text: big, TurnID: fmt.Sprintf("t%d", i)})
	}
}

// TestLaggedDrop_StrictPrefixAndLosslessReplay (#148 Defect A): after frame X
// is dropped for a slow subscriber, the connection must deliver a strict
// prefix of the sequence — never a frame past X — so the reconnect replay from
// the last delivered seq contains X instead of skipping the hole forever.
//
// Choreography of the bug: the reader stalls long enough to overflow its
// channel (X dropped, lagged signalled), then briefly resumes — freeing
// channel capacity — while new frames keep arriving. The buggy push slips
// frames > X into the freed capacity and the handler writes them to the wire
// before it notices the lag, so the browser reconnects with Last-Event-ID > X.
func TestLaggedDrop_StrictPrefixAndLosslessReplay(t *testing.T) {
	bus, r, _, id := liveRelay(t)
	// Warm publish sets the active session (frames: status seq 1, line seq 2).
	bus.Publish(voiceevent.STTFinal{At: at(0), Text: "warm", TurnID: "w"})

	s, _ := r.attach(id, 0)
	defer r.detach(s)

	// Stalled reader: subBuffer line frames fill s.ch, one more overflows — the
	// dropped frame X — and the subscription is signalled lagged.
	for i := 0; i <= subBuffer; i++ {
		bus.Publish(voiceevent.STTFinal{At: at(i), Text: "flood", TurnID: fmt.Sprintf("f%d", i)})
	}
	select {
	case <-s.lagged:
	default:
		t.Fatal("expected the overflowing subscriber to be signalled lagged")
	}
	dropped := r.nextSeq // seq of frame X: the last emitted frame overflowed

	// Reader briefly resumes: drain a little capacity, then more frames arrive.
	last := uint64(2) // the warm frames (seq 1, 2) predate attach; live starts at 3
	for i := 0; i < 8; i++ {
		last = (<-s.ch).Seq
	}
	for i := 0; i < 8; i++ {
		bus.Publish(voiceevent.STTFinal{At: at(i), Text: "after-drop", TurnID: fmt.Sprintf("g%d", i)})
	}

	// Drain everything this connection would deliver: it must be a strict
	// prefix — contiguous seqs, none past the dropped frame X.
	for {
		select {
		case f := <-s.ch:
			if f.Seq != last+1 {
				t.Fatalf("delivered seq %d after %d: not a strict prefix", f.Seq, last)
			}
			if f.Seq >= dropped {
				t.Fatalf("delivered frame seq %d on a lagged connection; nothing >= dropped frame %d may be delivered", f.Seq, dropped)
			}
			last = f.Seq
		default:
			// Channel drained.
			if last >= dropped {
				t.Fatalf("last delivered seq %d, dropped frame was %d", last, dropped)
			}
			// Reconnect replay from the last delivered seq must contain X, gap-free.
			replay := r.Frames(id, last)
			if len(replay) == 0 || replay[0].Seq != last+1 {
				t.Fatalf("replay after seq %d starts at %+v; want contiguous from %d", last, replay, last+1)
			}
			for _, f := range replay {
				if f.Seq == dropped {
					return // lossless: the hole is replayed
				}
			}
			t.Fatalf("replay after seq %d does not contain the dropped frame %d", last, dropped)
		}
	}
}

// TestServeEvents_StalledClientReleasedWithinWriteDeadline (#148 Defect B):
// a client that stops reading (laptop sleep, TCP zero-window) must not park
// the SSE handler inside a write forever. With a per-write deadline the
// blocked write fails, the handler exits and its subscriber entry is cleaned
// up within a bounded time. Real server + raw TCP client that never reads.
func TestServeEvents_StalledClientReleasedWithinWriteDeadline(t *testing.T) {
	bus, r, _, id := liveRelay(t)
	r.writeTimeout = 250 * time.Millisecond
	srv := httptest.NewServer(eventsMux(r))
	defer srv.Close()

	bus.Publish(voiceevent.STTFinal{At: at(0), Text: "warm", TurnID: "w"})

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /api/v1/sessions/%s/events HTTP/1.1\r\nHost: stalled\r\n\r\n", id)
	// Deliberately never read from conn: OS buffers fill, server writes block.

	waitFor(t, 2*time.Second, "SSE handler never attached", func() bool {
		return r.subscriberCount() == 1
	})

	floodBigFrames(bus)

	waitFor(t, 5*time.Second,
		"stalled client still holds its subscriber: frame write never timed out",
		func() bool { return r.subscriberCount() == 0 })
}
