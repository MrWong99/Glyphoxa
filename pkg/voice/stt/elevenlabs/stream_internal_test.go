package elevenlabs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
)

// TestWritePump_SendsKeepalivePing pins that the write pump emits websocket
// keepalive pings on its interval. It drives the unexported openStream knob so
// the interval is milliseconds instead of the 20s production default; this is
// an internal test because that knob is deliberately not exported.
func TestWritePump_SendsKeepalivePing(t *testing.T) {
	pinged := make(chan struct{}, 1)
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetPingHandler(func(string) error {
			select {
			case pinged <- struct{}{}:
			default:
			}
			return nil
		})
		// ReadMessage processes incoming control frames (invoking the ping
		// handler) and blocks until the client closes the connection.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c := New("k", WithBaseURL(srv.URL))
	s, err := c.openStream(context.Background(), stt.StreamConfig{SampleRate: 16000}, 30*time.Millisecond)
	if err != nil {
		t.Fatalf("openStream: %v", err)
	}
	defer s.Close()

	select {
	case <-pinged:
	case <-time.After(2 * time.Second):
		t.Fatal("no keepalive ping observed within 2s (ping interval was 30ms)")
	}
}

// TestSend_WriteQueueFull_RestoresAggregatedAudio pins Finding 4: when a flush
// cannot be enqueued (write queue full), Send reports a recoverable queue-full
// *StreamError AND keeps the flushed bytes in the aggregation buffer so no
// audio is dropped. It builds a stream with a one-slot, undrained write queue
// (no pumps started), which is why it is an internal test.
func TestSend_WriteQueueFull_RestoresAggregatedAudio(t *testing.T) {
	s := &stream{
		cfg:       stt.StreamConfig{SampleRate: 16000},
		writeCh:   make(chan wsWrite, 1),
		stopCh:    make(chan struct{}),
		threshold: 16000 * minChunkMs / 1000 * 2, // 100 ms in bytes
	}
	s.writeCh <- wsWrite{} // occupy the only slot; the next enqueue must fail

	samples := make([]int16, 512)
	for i := range samples {
		samples[i] = int16(i + 1)
	}

	// Four 32 ms frames cross the 100 ms threshold on the fourth, triggering a
	// flush that cannot enqueue.
	var lastErr error
	for i := 0; i < 4; i++ {
		f, err := audio.NewFrame(samples, 16000, 32)
		if err != nil {
			t.Fatalf("NewFrame: %v", err)
		}
		lastErr = s.Send(f)
	}

	if lastErr == nil {
		t.Fatal("Send on a full write queue returned nil error")
	}
	var se *stt.StreamError
	if !errors.As(lastErr, &se) || se.Code != stt.CodeQueueFull {
		t.Fatalf("error = %v, want a *StreamError with code %q", lastErr, stt.CodeQueueFull)
	}
	if se.Fatal {
		t.Error("queue-full should be recoverable, not Fatal")
	}

	// All four frames' bytes must still be buffered for a later flush.
	wantBytes := 4 * 512 * 2
	if len(s.agg) != wantBytes {
		t.Errorf("aggregation buffer = %d bytes, want %d (flushed audio must not be lost)", len(s.agg), wantBytes)
	}
}
