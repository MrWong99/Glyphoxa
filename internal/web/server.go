// Package web is the single-operator web tier (ADR-0039): one cleartext HTTP
// listener that serves the Connect RPC handlers AND the observability endpoints
// (/metrics, /healthz, /readyz) over the same port. It is deliberately
// decoupled from the campaign/storage specifics — callers hand it a set of
// [Mount]s — so it unit-tests keyless with a fake Connect handler.
//
// The listener speaks both Connect-over-HTTP/1.1 (JSON or binary) and gRPC over
// h2c (cleartext HTTP/2). Rather than the deprecated x/net/http2/h2c wrapper, it
// enables unencrypted HTTP/2 via the Go 1.24+ [http.Server.Protocols] field
// (SetHTTP1 + SetUnencryptedHTTP2): a gRPC client gets prior-knowledge HTTP/2
// without TLS and a browser/Connect client uses HTTP/1.1, both on the one
// address. TLS termination is the reverse-proxy's job in the self-host topology
// (ADR-0039).
package web

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// Mount is one path-prefixed handler to register on the server's mux — e.g. the
// Connect CampaignService handler at its generated path. Path and Handler are
// the two arguments a (*rpc.CampaignServer).Handler() pair maps onto.
type Mount struct {
	Path    string
	Handler http.Handler
}

// Config configures a [Server]. Recorder is required (it backs /metrics); Ready
// gates /readyz (nil means always-ready — see [observe.ReadinessProbe]); Logger
// defaults to slog.Default when nil.
type Config struct {
	Addr     string
	Mounts   []Mount
	Recorder *observe.PrometheusRecorder
	Ready    observe.ReadinessProbe
	Logger   *slog.Logger
}

// Server is the web-tier HTTP listener. Build it with [NewServer] and run it
// with [Server.Start]; the resolved listen address is available from
// [Server.Addr] after Start returns.
type Server struct {
	srv *http.Server
	log *slog.Logger

	mu   sync.Mutex // guards addr against the Start writer / Addr readers
	addr string
}

// NewServer builds the server's mux — the configured [Mount]s plus the
// observability endpoints (ADR-0032) — enables cleartext HTTP/2 (h2c) alongside
// HTTP/1.1 via [http.Server.Protocols] so gRPC and Connect share one cleartext
// port, and returns a Server that has not yet bound a listener (see [Server.Start]).
func NewServer(cfg Config) *Server {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	mux := http.NewServeMux()
	for _, m := range cfg.Mounts {
		mux.Handle(m.Path, m.Handler)
	}
	observe.MountObservability(mux, cfg.Recorder, cfg.Ready)

	// Serve HTTP/1.1 and cleartext HTTP/2 (h2c) on the one port via the Go 1.24+
	// Protocols field, replacing the deprecated x/net/http2/h2c wrapper. Connect
	// clients use HTTP/1.1; gRPC clients use prior-knowledge h2c.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	return &Server{
		srv: &http.Server{
			Addr:              cfg.Addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			Protocols:         protocols,
		},
		addr: cfg.Addr,
		log:  log,
	}
}

// Start binds the listener synchronously (so [Server.Addr] resolves an
// ephemeral :0 port before serving) and then serves in a background goroutine,
// shutting down gracefully when ctx is cancelled. It returns a non-nil error
// only if the bind fails; a serve error after a successful bind is logged at
// Error (except the clean http.ErrServerClosed on shutdown), mirroring
// [observe.MetricsServer.Start].
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.addr = ln.Addr().String()
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutCtx)
	}()
	go func() {
		s.log.Info("web server listening", "addr", s.addr)
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("web server failed", "err", err)
		}
	}()
	return nil
}

// Addr returns the resolved listen address — meaningful only after [Server.Start]
// has bound the listener (it returns the configured address before that).
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}
