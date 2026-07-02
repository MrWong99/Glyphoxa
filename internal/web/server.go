// Package web is the single-operator web tier (ADR-0039): one cleartext HTTP
// listener that serves ONLY the Connect RPC handlers. The observability
// endpoints (/metrics, /healthz, /readyz) live on a SEPARATE internal port (the
// standalone observe.MetricsServer) so Prometheus and the kubelet can reach them
// while they stay off the public API surface — the API listener never exposes
// /readyz or /metrics to the web. The server is deliberately decoupled from the
// campaign/storage specifics — callers hand it a set of [Mount]s — so it
// unit-tests keyless with a fake Connect handler.
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

// APIMount adapts a generated Connect handler pair — (mountPath, handler) as
// returned by a service's Handler() method — into a [Mount] under the public
// /api prefix. The browser dials Connect at baseUrl "/api"
// (web/src/lib/transport.ts), so the handler is wrapped in
// http.StripPrefix("/api", …) and registered at "/api"+mountPath; StripPrefix
// removes the /api segment before the Connect handler matches its method path.
// The stacked #68/#71 PRs reuse this to mount their services identically to
// AuthService/CampaignService.
func APIMount(mountPath string, handler http.Handler) Mount {
	return Mount{Path: "/api" + mountPath, Handler: http.StripPrefix("/api", handler)}
}

// Config configures a [Server]. Logger defaults to slog.Default when nil. The
// observability endpoints are NOT served here — they live on the separate
// metrics port (see the package doc) — so this carries no recorder or probe.
type Config struct {
	Addr   string
	Mounts []Mount
	Logger *slog.Logger

	// Root, when non-nil, is the catch-all handler mounted at "/" — the embedded
	// SPA (internal/spa) in web/all Mode. http.ServeMux always prefers the more
	// specific [Mount] prefixes (e.g. the Connect API under /api/) over the "/"
	// root, so requests that match no API prefix fall through to Root, which
	// serves the SPA shell with client-side-routing fallback. Nil leaves "/"
	// unmounted (the API-only server tests).
	Root http.Handler
}

// Server is the web-tier HTTP listener. Build it with [NewServer] and run it
// with [Server.Start]; the resolved listen address is available from
// [Server.Addr] after Start returns.
type Server struct {
	srv *http.Server
	log *slog.Logger

	// done is closed once the server has fully stopped: after Serve returned
	// AND — when the stop was the ctx-triggered graceful Shutdown — that
	// Shutdown has returned (drain finished, or abandoned at the grace
	// deadline). Serve alone returns the instant Shutdown closes the listener,
	// BEFORE the drain (issue #138), so Serve returning must not signal done by
	// itself. [Server.Wait] blocks on it so callers can hold resources (e.g.
	// the DB pool) open until in-flight handlers finish.
	done chan struct{}

	mu   sync.Mutex // guards addr against the Start writer / Addr readers
	addr string
}

// NewServer builds the server's mux from the configured [Mount]s, enables
// cleartext HTTP/2 (h2c) alongside HTTP/1.1 via [http.Server.Protocols] so gRPC
// and Connect share one cleartext port, and returns a Server that has not yet
// bound a listener (see [Server.Start]).
func NewServer(cfg Config) *Server {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	mux := http.NewServeMux()
	// Fence the API namespace (#153): an /api/... path no mount claims is a
	// plain 404, NOT the SPA catch-all below — 200+index.html sends a malformed
	// EventSource URL into an auto-reconnect loop and turns version-skewed
	// Connect calls into misleading errors. ServeMux prefers the more specific
	// pattern, so every real /api mount (Connect prefixes, the {id}-wildcard
	// SSE route) still wins over this guard.
	mux.Handle("/api/", http.NotFoundHandler())
	for _, m := range cfg.Mounts {
		mux.Handle(m.Path, m.Handler)
	}
	// The SPA catch-all is registered last at "/"; ServeMux's longest-prefix
	// match keeps the API mounts (e.g. /api/) ahead of it, so only unmatched
	// paths reach the SPA fallback.
	if cfg.Root != nil {
		mux.Handle("/", cfg.Root)
	}

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
		done: make(chan struct{}),
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

	drained := observe.ShutdownOnCancel(ctx, s.srv)
	go func() {
		defer close(s.done)
		s.log.Info("web server listening", "addr", s.addr)
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("web server failed", "err", err)
		}
		// Serve returns the moment Shutdown closes the listener; the graceful
		// drain of in-flight requests happens AFTERWARDS, inside Shutdown itself
		// (issue #138). Hold done open until that drain completes, but only when
		// a shutdown was actually triggered — a spontaneous Serve failure with a
		// live ctx has no Shutdown to wait for, and blocking here would hang
		// Wait forever.
		select {
		case <-ctx.Done():
			<-drained
		default:
		}
	}()
	return nil
}

// RegisterOnShutdown registers f to run when the ctx-triggered graceful
// Shutdown begins (net/http calls it before waiting out the drain). Long-lived
// streaming handlers — the transcript SSE tail — never go idle on their own, so
// without a release hook they stall every shutdown for the full grace period;
// f is how their owner gets told to let go. Register before [Server.Start]:
// net/http only guarantees callbacks registered before Shutdown is called, and
// Start wires the cancel-to-Shutdown path.
func (s *Server) RegisterOnShutdown(f func()) {
	s.srv.RegisterOnShutdown(f)
}

// Wait blocks until the server has fully stopped — i.e. after the ctx passed to
// [Server.Start] is cancelled, Serve has returned, and the graceful Shutdown
// has returned. Callers use it to keep dependencies (the DB pool) alive until
// handlers finish, instead of racing teardown against drain. The guarantee is
// bounded by [observe.ShutdownGrace]: at the deadline Shutdown gives up and
// returns WITHOUT closing active connections, so a handler slower than the
// grace may still be running when Wait returns — teardown after Wait is safe
// against drained handlers, not against ones that outlive the grace. Call it
// only after a successful [Server.Start]; on a bind failure Start returns the
// error and Wait must not be called (done never closes).
func (s *Server) Wait() {
	<-s.done
}

// Addr returns the resolved listen address — meaningful only after [Server.Start]
// has bound the listener (it returns the configured address before that).
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}
