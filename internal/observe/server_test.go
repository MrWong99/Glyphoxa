package observe

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestMetricsServerServesAndShutsDown is the /metrics DONE-gate for voice mode:
// a real listener serves the series, and cancelling the context stops it.
func TestMetricsServerServesAndShutsDown(t *testing.T) {
	// Grab a free port so the test never collides with a real :9090.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	rec := NewPrometheusRecorder()
	rec.SessionOpened("g")
	srv := NewMetricsServer(addr, rec, nil)

	ctx, cancel := context.WithCancel(context.Background())
	srv.Start(ctx)

	// Poll until the listener accepts (Start is async).
	var body string
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/metrics")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(b)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("metrics server never came up: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(body, "glyphoxa_voice_sessions 1") {
		t.Fatalf("/metrics did not serve the series:\n%s", filterGlyphoxa(body))
	}

	// Cancelling shuts the listener down; a subsequent request should fail.
	cancel()
	down := time.Now().Add(2 * time.Second)
	for {
		if _, err := http.Get("http://" + addr + "/metrics"); err != nil {
			break // listener closed
		}
		if time.Now().After(down) {
			t.Fatal("metrics server did not shut down on context cancel")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
