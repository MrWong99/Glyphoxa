package observe

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestShutdownOnCancelStopsServe is the core contract: once the wired context is
// cancelled, the helper's goroutine calls srv.Shutdown and the blocking
// srv.Serve returns the clean http.ErrServerClosed within the grace window — the
// behaviour both the MetricsServer and the web server depend on.
func TestShutdownOnCancelStopsServe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	srv := &http.Server{Handler: http.NewServeMux()}
	ctx, cancel := context.WithCancel(context.Background())
	ShutdownOnCancel(ctx, srv)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	// Cancelling triggers the helper's graceful Shutdown; Serve must then return
	// the clean ErrServerClosed well inside the grace period.
	cancel()
	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Serve returned %v, want http.ErrServerClosed", err)
		}
	case <-time.After(ShutdownGrace + time.Second):
		t.Fatalf("Serve did not return within %v after cancel", ShutdownGrace+time.Second)
	}
}

// TestShutdownOnCancelWaitsForCancel pins the other half: the helper does NOT
// shut the server down until the context is actually cancelled, so a running
// server keeps serving for its context's lifetime.
func TestShutdownOnCancelWaitsForCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	srv := &http.Server{Handler: http.NewServeMux()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ShutdownOnCancel(ctx, srv)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	// Without a cancel, Serve must stay blocked (no premature shutdown).
	select {
	case err := <-serveErr:
		t.Fatalf("Serve returned %v before context cancel; helper shut down early", err)
	case <-time.After(150 * time.Millisecond):
		// still serving, as required
	}

	cancel()
	select {
	case <-serveErr:
	case <-time.After(ShutdownGrace + time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

// TestShutdownOnCancelSignalsDrainCompletion pins the completion signal (issue
// #138): the returned channel must stay open while an in-flight request is still
// draining — Serve returning is NOT the end of the drain — and close once
// srv.Shutdown finishes, i.e. after the parked handler completes.
func TestShutdownOnCancelSignalsDrainCompletion(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	handlerEntered := make(chan struct{})
	releaseHandler := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		close(handlerEntered)
		<-releaseHandler
	})

	srv := &http.Server{Handler: mux}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drained := ShutdownOnCancel(ctx, srv)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/slow")
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-handlerEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler never entered")
	}

	cancel()

	// Serve returns as soon as Shutdown closes the listener; the drain is still
	// in progress because the handler is parked, so the completion channel must
	// still be open.
	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Serve returned %v, want http.ErrServerClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
	select {
	case <-drained:
		t.Fatal("completion channel closed while the in-flight handler was still draining")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseHandler)
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("completion channel never closed after the drain finished")
	}
}
