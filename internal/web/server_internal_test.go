package web

import (
	"context"
	"testing"
	"time"
)

// TestWaitReturnsOnSpontaneousServeFailure pins the non-shutdown exit path: when
// Serve fails on its own — here forced by closing the http.Server directly, with
// the Start ctx still live — Wait must still return instead of blocking forever
// on a graceful shutdown that was never triggered. Internal test: only the
// package can reach s.srv to kill it out-of-band.
func TestWaitReturnsOnSpontaneousServeFailure(t *testing.T) {
	srv := NewServer(Config{Addr: "127.0.0.1:0"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Close (not Shutdown) makes Serve return http.ErrServerClosed while ctx is
	// NOT cancelled — the shape of a spontaneous serve/listener failure as far
	// as the done-signalling goroutine is concerned.
	if err := srv.srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	waitReturned := make(chan struct{})
	go func() {
		srv.Wait()
		close(waitReturned)
	}()

	select {
	case <-waitReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait() blocked forever after a spontaneous Serve failure with no ctx cancel")
	}
}

// TestServerTimeouts pins the #662 connection-level posture: idle keep-alive
// connections expire, while the two process-wide wall clocks stay OFF — a
// WriteTimeout would cut the SSE transcript tail and the streaming chat
// exchange, and one ReadTimeout cannot fit both a 1 MiB RPC and a 192 MiB
// upload. Slow request bodies are bounded per mount instead (see the
// body-deadline tests).
func TestServerTimeouts(t *testing.T) {
	srv := NewServer(Config{Addr: "127.0.0.1:0"})
	if srv.srv.IdleTimeout <= 0 {
		t.Error("IdleTimeout is unset — idle keep-alive connections never expire")
	}
	if srv.srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout is unset")
	}
	if srv.srv.ReadTimeout != 0 || srv.srv.WriteTimeout != 0 {
		t.Errorf("ReadTimeout=%v WriteTimeout=%v, want both 0 — bodies are bounded per mount, and a WriteTimeout breaks the SSE and streaming-chat endpoints",
			srv.srv.ReadTimeout, srv.srv.WriteTimeout)
	}
}
