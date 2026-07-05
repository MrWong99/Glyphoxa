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

// TestOpenStream_Unauthorized_MapsToAuthError pins that an HTTP 401/403 on the
// websocket handshake surfaces as a *StreamError with the auth_error code (not the
// generic transport code), so the stream manager backs a revoked/forbidden key off
// straight to the cap instead of ~6 fast redials.
func TestOpenStream_Unauthorized_MapsToAuthError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"unauthorized", http.StatusUnauthorized},
		{"forbidden", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status) // reject the upgrade
			}))
			defer srv.Close()

			c := New("k", WithBaseURL(srv.URL))
			_, err := c.OpenStream(context.Background(), stt.StreamConfig{SampleRate: 16000})
			if err == nil {
				t.Fatal("OpenStream returned nil on a rejected handshake; want an error")
			}
			var se *stt.StreamError
			if !errors.As(err, &se) {
				t.Fatalf("error %v is not a *stt.StreamError", err)
			}
			if se.Code != "auth_error" {
				t.Errorf("dial error code = %q, want auth_error for HTTP %d", se.Code, tc.status)
			}
			if !se.Fatal {
				t.Error("a rejected handshake must be Fatal (the session never opened)")
			}
		})
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

// TestCommit_WriteQueueFull_RestoresRemainder pins the R1 fix: when Commit cannot
// enqueue the swapped-out aggregation remainder (write queue full), it restores
// that remainder into the aggregation buffer — mirroring the Send-path restore —
// so a retried Commit does not lose the utterance's tail. Without it the tail
// bytes vanished with the failed enqueue and the retried commit was short.
func TestCommit_WriteQueueFull_RestoresRemainder(t *testing.T) {
	s := &stream{
		cfg:       stt.StreamConfig{SampleRate: 16000},
		writeCh:   make(chan wsWrite, 1),
		stopCh:    make(chan struct{}),
		threshold: 16000 * minChunkMs / 1000 * 2,
	}
	s.writeCh <- wsWrite{} // occupy the only slot; the remainder enqueue must fail

	// A sub-threshold remainder sits in the aggregation buffer (never flushed by
	// Send because it is under the 100 ms threshold).
	remainder := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	s.agg = append([]byte(nil), remainder...)

	ch, err := s.Commit()
	if err != nil {
		t.Fatalf("Commit returned a call-time error %v; want a resolving channel", err)
	}

	select {
	case res := <-ch:
		var se *stt.StreamError
		if !errors.As(res.Err, &se) || se.Code != stt.CodeQueueFull {
			t.Fatalf("commit resolved with %v; want a *StreamError code %q", res.Err, stt.CodeQueueFull)
		}
	case <-time.After(time.Second):
		t.Fatal("commit channel never resolved on a queue-full remainder enqueue")
	}

	if string(s.agg) != string(remainder) {
		t.Errorf("aggregation buffer = %v, want the restored remainder %v (utterance tail must not be lost)", s.agg, remainder)
	}
}
