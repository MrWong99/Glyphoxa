package observe

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
	srv := NewMetricsServer(addr, rec, nil, nil)

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

// TestHealthzAlwaysOK pins the liveness contract: /healthz is 200 as long as the
// process can serve a request — it never consults the readiness probe.
func TestHealthzAlwaysOK(t *testing.T) {
	// Even with a probe that always fails, liveness stays 200.
	srv := httptest.NewServer(newMux(NewPrometheusRecorder(), func(context.Context) error {
		return errors.New("db down")
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", resp.StatusCode)
	}
}

// TestReadyzProbeOK is the happy-path readiness gate: a probe returning nil means
// the dependency (a DB ping) is healthy, so /readyz is 200.
func TestReadyzProbeOK(t *testing.T) {
	srv := httptest.NewServer(newMux(NewPrometheusRecorder(), func(context.Context) error {
		return nil
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/readyz = %d, want 200", resp.StatusCode)
	}
}

// TestReadyzProbeDown is the DB-down readiness case: a probe returning an error
// means the dependency is unreachable, so /readyz is 503 and k8s holds traffic.
func TestReadyzProbeDown(t *testing.T) {
	srv := httptest.NewServer(newMux(NewPrometheusRecorder(), func(context.Context) error {
		return errors.New("dial tcp: connection refused")
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d, want 503", resp.StatusCode)
	}
}

// TestReadyzNilProbeOK pins the -hardcoded / no-DB contract: a nil probe means
// there is no dependency to gate on, so /readyz is unconditionally 200.
func TestReadyzNilProbeOK(t *testing.T) {
	srv := httptest.NewServer(newMux(NewPrometheusRecorder(), nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/readyz (nil probe) = %d, want 200", resp.StatusCode)
	}
}
