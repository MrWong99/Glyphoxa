package transcript

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
)

// defaultWriteTimeout bounds each SSE frame write/flush (#148 Defect B). Big
// enough for any live client on a bad link to drain one frame; small enough
// that a stalled reader releases its handler well within an operator's
// patience and does not pin the graceful shutdown drain.
const defaultWriteTimeout = 5 * time.Second

// subscriber is one live SSE connection's fan-out channel. id is the session it
// subscribed to: only frames for the matching active session are delivered, so a
// stale connection from a previous session never receives the next one's frames.
type subscriber struct {
	id     string
	ch     chan Frame
	lagged chan struct{} // closed when ch overflows; the handler then exits → reconnect
}

// push fans a frame out to every subscriber on the active session. A full
// channel means a slow reader: it is signalled lagged (once) and from then on
// receives NOTHING more (#148 Defect A) — delivering any frame past the
// dropped one would let the client reconnect with a Last-Event-ID beyond the
// hole and skip it forever. The connection sees a strict prefix of the
// sequence, so its EventSource reconnect replays the dropped frame from the
// ring losslessly. Caller holds r.mu, and the bus contract forbids blocking,
// so the send is non-blocking.
func (r *Relay) push(f Frame) {
	for s := range r.subs {
		if s.id != r.activeID {
			continue
		}
		select {
		case <-s.lagged:
			// Already dropped a frame: never send another. Only push closes
			// lagged, always under r.mu, so this check-then-close is race-free.
			continue
		default:
		}
		select {
		case s.ch <- f:
		default:
			close(s.lagged)
		}
	}
}

// attach registers a subscriber and atomically returns the replay frames after
// lastID, all under one lock so no frame is lost or duplicated across the
// snapshot/subscribe boundary.
func (r *Relay) attach(id string, lastID uint64) (*subscriber, []Frame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var replay []Frame
	if id == r.activeID {
		for _, f := range r.buf {
			if f.Seq > lastID {
				replay = append(replay, f)
			}
		}
	}
	s := &subscriber{id: id, ch: make(chan Frame, subBuffer), lagged: make(chan struct{})}
	r.subs[s] = struct{}{}
	return s, replay
}

// detach removes a subscriber when its connection closes.
func (r *Relay) detach(s *subscriber) {
	r.mu.Lock()
	delete(r.subs, s)
	r.mu.Unlock()
}

// ServeEvents is the SSE live tail: GET /api/v1/sessions/{id}/events. It replays
// the ring buffer after Last-Event-ID, then streams live frames until the client
// disconnects, falls too far behind, or the process begins its graceful shutdown
// (CloseStreams — issue #138).
//
// An id that does not parse as a UUID is 404 (#169), same as ServeSnapshot (see
// its doc comment for the id-class argument): answering 200 would pin a
// subscriber slot + handler goroutine forever on a stream no session can feed.
func (r *Relay) ServeEvents(w http.ResponseWriter, req *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	id := req.PathValue("id")
	sid, err := uuid.Parse(id)
	if err != nil {
		http.NotFound(w, req)
		return
	}
	// Tenant scoping (#439) runs BEFORE the stream opens: a foreign-tenant
	// session is a plain 404, never a half-opened event stream the browser's
	// EventSource would retry against forever.
	if !r.authorizeTenant(w, req, sid) {
		return
	}
	fw := r.newFrameWriter(w)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	// No "Connection: keep-alive": it is a hop-by-hop header the web tier's h2c
	// listener rejects, and SSE needs no help from it. Disable proxy buffering so
	// frames flush promptly.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := fw.flush(); err != nil {
		return
	}

	s, replay := r.attach(id, lastEventID(req))
	defer r.detach(s)

	for _, f := range replay {
		if err := fw.write(f); err != nil {
			return
		}
	}

	ctx := req.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.closing:
			return
		case <-s.lagged:
			return
		case f := <-s.ch:
			if err := fw.write(f); err != nil {
				return
			}
		}
	}
}

// ServeSnapshot is the initial-state read: GET /api/v1/sessions/{id}. For the live
// active session it returns the in-memory coalesced lines + derived status/typing;
// for any other (ended) session it replays the persisted history from the DB with
// status "idle" (#74, ADR-0040), so a reconnect / reload sees the transcript even
// after the in-memory ring is gone.
//
// An id that does not parse as a UUID is 404, not the empty idle view (#153): it
// can never name a session (live ids are uuid.UUID.String(), persisted ids are
// UUID FKs) — it is a routing artifact, e.g. ServeMux path-cleaning the malformed
// EventSource URL /api/v1/sessions//events into /api/v1/sessions/events, which
// this route then claims with id="events". A 200 there would mask the broken URL.
func (r *Relay) ServeSnapshot(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	sid, err := uuid.Parse(id)
	if err != nil {
		http.NotFound(w, req)
		return
	}
	if !r.authorizeTenant(w, req, sid) {
		return
	}
	view := r.snapshot(req.Context(), id)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	if err := json.NewEncoder(w).Encode(view); err != nil {
		r.log.Warn("transcript: encode snapshot", "err", err)
	}
}

// authorizeTenant enforces the tenant-ownership gate (#439) on a session
// endpoint. With no scope installed it is a no-op (unscoped, pre-#439
// behavior). Otherwise the request must carry the tenant auth.RequireTenant
// injected — a miss is a miswired mount (the #408 class) and rejects 401,
// fail-closed. A session outside the tenant — including one that does not
// exist at all, so absence and foreignness are indistinguishable — is 404,
// matching the Highlight mounts' don't-reveal-existence posture. A check
// failure is 500: an infra error must neither open the door nor masquerade as
// absence. Returns false when it wrote a response and the handler must stop.
func (r *Relay) authorizeTenant(w http.ResponseWriter, req *http.Request, sid uuid.UUID) bool {
	scope := r.tenantScope()
	if scope == nil {
		return true
	}
	tenantID, ok := auth.TenantID(req.Context())
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return false
	}
	owns, err := scope(req.Context(), tenantID, sid)
	if err != nil {
		r.log.Error("transcript: tenant scope check", "err", err, "session", sid)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if !owns {
		http.NotFound(w, req)
		return false
	}
	return true
}

// frameWriter writes SSE frames bounded by a per-write deadline (#148 Defect
// B): each write/flush is armed with writeTimeout via
// http.ResponseController.SetWriteDeadline, so a client that stops reading
// (laptop sleep, TCP zero-window, exhausted h2c flow-control window) makes the
// blocked write fail — the handler exits and its subscriber is cleaned up —
// instead of parking the goroutine in Write forever, where the lagged escape
// can never fire. Both production paths honor the deadline (HTTP/1.1 conn
// deadline; the bundled HTTP/2 server's per-stream write deadline under the
// web tier's h2c Protocols setup — pinned by the h2c hardening test).
type frameWriter struct {
	w         http.ResponseWriter
	rc        *http.ResponseController
	timeout   time.Duration
	deadlines bool // writer supports SetWriteDeadline
}

// newFrameWriter probes deadline support once so an exotic wrapped writer
// degrades LOUDLY (a warning, unbounded writes) rather than silently killing
// the stream on its first frame.
func (r *Relay) newFrameWriter(w http.ResponseWriter) *frameWriter {
	fw := &frameWriter{w: w, rc: http.NewResponseController(w), timeout: r.writeTimeout, deadlines: true}
	if err := fw.rc.SetWriteDeadline(time.Time{}); err != nil {
		fw.deadlines = false
		r.log.Warn("transcript: response writer does not support write deadlines; stalled-client protection disabled", "err", err)
	}
	return fw
}

// write serializes one SSE frame — an id/event/data triple terminated by a
// blank line — and flushes it, all under one armed write deadline. Data is
// single-line JSON (json.Marshal never emits a raw newline), so no multi-line
// data folding is needed. The deadline is disarmed after a successful flush:
// the tail can sit idle between frames indefinitely, and under h2c an armed
// deadline is a stream-reset timer that fires even with no write pending.
func (fw *frameWriter) write(f Frame) error {
	if err := fw.arm(time.Now().Add(fw.timeout)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(fw.w, "id: %d\nevent: %s\ndata: %s\n\n", f.Seq, f.Event, f.Data); err != nil {
		return err
	}
	if err := fw.rc.Flush(); err != nil {
		return err
	}
	return fw.arm(time.Time{})
}

// flush pushes buffered bytes (the response headers) under a write deadline.
func (fw *frameWriter) flush() error {
	if err := fw.arm(time.Now().Add(fw.timeout)); err != nil {
		return err
	}
	if err := fw.rc.Flush(); err != nil {
		return err
	}
	return fw.arm(time.Time{})
}

// arm sets (or clears, for the zero time) the write deadline when supported.
func (fw *frameWriter) arm(t time.Time) error {
	if !fw.deadlines {
		return nil
	}
	return fw.rc.SetWriteDeadline(t)
}

// lastEventID reads the browser's resume cursor from the Last-Event-ID header
// (sent automatically on EventSource reconnect), falling back to the
// last_event_id query param for manual replay. 0 (or unparseable) replays the
// whole buffer.
func lastEventID(req *http.Request) uint64 {
	v := req.Header.Get("Last-Event-ID")
	if v == "" {
		v = req.URL.Query().Get("last_event_id")
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
