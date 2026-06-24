package observe

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// ReadinessProbe reports whether the voice node's external dependencies are
// healthy enough to serve traffic — in practice a DB ping (issue #33). It backs
// /readyz: a nil error means ready (200), any error means not-ready (503). A nil
// ReadinessProbe means "no dependency to gate on" and /readyz is always 200,
// which is the correct answer for a -hardcoded no-DB run.
type ReadinessProbe func(context.Context) error

// MetricsServer is the minimal metrics-only HTTP listener for voice mode
// (ADR-0032 §2.3): no SPA, no SSE, just /metrics so a Prometheus can scrape a
// headless voice node — plus the /healthz and /readyz k8s probes (issue #33). In
// web/all mode the adapter's [PrometheusRecorder.Handler] is mounted on the
// existing server instead (control-plane, issue #6) — this type is only for the
// standalone voice node.
type MetricsServer struct {
	srv *http.Server
	log *slog.Logger
}

// NewMetricsServer builds a metrics-only server serving rec's registry at
// /metrics on addr (e.g. ":9090"), plus the /healthz liveness and /readyz
// readiness probes for Kubernetes (issue #33). ready gates /readyz; pass nil for
// a no-DB run (see [ReadinessProbe]). It does not listen until
// [MetricsServer.Start].
func NewMetricsServer(addr string, rec *PrometheusRecorder, ready ReadinessProbe, log *slog.Logger) *MetricsServer {
	if log == nil {
		log = slog.Default()
	}
	return &MetricsServer{
		srv: &http.Server{
			Addr:              addr,
			Handler:           newMux(rec, ready),
			ReadHeaderTimeout: 5 * time.Second,
		},
		log: log,
	}
}

// MountObservability mounts the observability endpoints — /metrics (rec's
// registry), /healthz (liveness — always 200 while the process serves), and
// /readyz (readiness — 200 when ready returns nil, 503 otherwise; always 200 if
// ready is nil) — onto a caller-supplied mux. The standalone voice
// [MetricsServer] and the web/all-mode server (ADR-0039) share this one wiring
// so the probe semantics stay identical across modes (ADR-0032).
func MountObservability(mux *http.ServeMux, rec *PrometheusRecorder, ready ReadinessProbe) {
	mux.Handle("/metrics", rec.Handler())
	// Liveness: the process is up and able to serve a request. It deliberately
	// ignores dependency health — a failing DB must not make k8s kill the pod
	// (that's readiness's job).
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	// Readiness: gate traffic on the dependency probe (a DB ping).
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if ready == nil {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "ok")
			return
		}
		if err := ready(r.Context()); err != nil {
			// Log the cause server-side and keep the response body generic: /readyz
			// is unauthenticated and, on the web tier, publicly reachable, so the
			// probe error (which wraps DB DSN fragments — user, database, host)
			// must not reach the wire. The 503 status is all a k8s readiness gate
			// needs; the cause belongs in the logs.
			slog.Default().Warn("readiness probe failed", "err", err)
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
}

// newMux wires the voice-node endpoints onto a fresh mux via
// [MountObservability]. Factored out so the handlers are testable without a real
// listener.
func newMux(rec *PrometheusRecorder, ready ReadinessProbe) *http.ServeMux {
	mux := http.NewServeMux()
	MountObservability(mux, rec, ready)
	return mux
}

// Start serves in a background goroutine and shuts the listener down when ctx is
// cancelled, so the caller wires it next to the voice loop's lifetime. A real
// listen error (not the clean ErrServerClosed on shutdown) is logged at Error —
// a missing metrics endpoint is operationally visible but must not crash the
// voice node.
func (m *MetricsServer) Start(ctx context.Context) {
	ShutdownOnCancel(ctx, m.srv)
	go func() {
		m.log.Info("metrics server listening", "addr", m.srv.Addr, "path", "/metrics")
		if err := m.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.log.Error("metrics server failed", "err", err)
		}
	}()
}
