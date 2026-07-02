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
