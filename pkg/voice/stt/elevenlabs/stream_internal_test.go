package elevenlabs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"

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
