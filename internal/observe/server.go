package observe

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// MetricsServer is the minimal metrics-only HTTP listener for voice mode
// (ADR-0032 §2.3): no SPA, no SSE, just /metrics so a Prometheus can scrape a
// headless voice node. In web/all mode the adapter's [PrometheusRecorder.Handler]
// is mounted on the existing server instead (control-plane, issue #6) — this
// type is only for the standalone voice node.
type MetricsServer struct {
	srv *http.Server
	log *slog.Logger
}

// NewMetricsServer builds a metrics-only server serving rec's registry at
// /metrics on addr (e.g. ":9090"). It does not listen until [MetricsServer.Start].
func NewMetricsServer(addr string, rec *PrometheusRecorder, log *slog.Logger) *MetricsServer {
	if log == nil {
		log = slog.Default()
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", rec.Handler())
	return &MetricsServer{
		srv: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		log: log,
	}
}

// Start serves in a background goroutine and shuts the listener down when ctx is
// cancelled, so the caller wires it next to the voice loop's lifetime. A real
// listen error (not the clean ErrServerClosed on shutdown) is logged at Error —
// a missing metrics endpoint is operationally visible but must not crash the
// voice node.
func (m *MetricsServer) Start(ctx context.Context) {
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.srv.Shutdown(shutCtx)
	}()
	go func() {
		m.log.Info("metrics server listening", "addr", m.srv.Addr, "path", "/metrics")
		if err := m.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.log.Error("metrics server failed", "err", err)
		}
	}()
}
