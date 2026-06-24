package observe

import (
	"context"
	"net/http"
	"time"
)

// ShutdownGrace is the deadline a graceful HTTP shutdown gets to drain in-flight
// requests before the listener is force-closed. It lives here so every server
// that calls [ShutdownOnCancel] — the standalone voice [MetricsServer] and the
// web-tier server (ADR-0039) — uses the SAME grace period and they stay in
// lockstep when it changes.
const ShutdownGrace = 5 * time.Second

// ShutdownOnCancel spawns a goroutine that waits for ctx to be cancelled and
// then gracefully shuts srv down within [ShutdownGrace], so a caller wires one
// server's lifetime to a context without re-implementing the
// "<-ctx.Done(); Shutdown(grace)" goroutine. It returns immediately; the
// goroutine outlives the call and ends once the shutdown completes (or its grace
// deadline expires). After cancel, srv.Serve / srv.ListenAndServe returns
// [http.ErrServerClosed], which callers treat as a clean stop.
//
// The Shutdown error is intentionally discarded: a graceful shutdown error
// (deadline exceeded) is not actionable here — the listener is closing either
// way — and the serve goroutine already logs the terminal Serve error.
func ShutdownOnCancel(ctx context.Context, srv *http.Server) {
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), ShutdownGrace)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
}
