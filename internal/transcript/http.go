package transcript

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

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
func (r *Relay) ServeEvents(w http.ResponseWriter, req *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	id := req.PathValue("id")

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	// No "Connection: keep-alive": it is a hop-by-hop header the web tier's h2c
	// listener rejects, and SSE needs no help from it. Disable proxy buffering so
	// frames flush promptly.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	s, replay := r.attach(id, lastEventID(req))
	defer r.detach(s)

	for _, f := range replay {
		writeFrame(w, f)
	}
	flusher.Flush()

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
			writeFrame(w, f)
			flusher.Flush()
		}
	}
}

// ServeSnapshot is the initial-state read: GET /api/v1/sessions/{id}. For the live
// active session it returns the in-memory coalesced lines + derived status/typing;
// for any other (ended) session it replays the persisted history from the DB with
// status "idle" (#74, ADR-0040), so a reconnect / reload sees the transcript even
// after the in-memory ring is gone.
func (r *Relay) ServeSnapshot(w http.ResponseWriter, req *http.Request) {
	view := r.snapshot(req.Context(), req.PathValue("id"))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	if err := json.NewEncoder(w).Encode(view); err != nil {
		r.log.Warn("transcript: encode snapshot", "err", err)
	}
}

// writeFrame serializes one SSE frame: an id/event/data triple terminated by a
// blank line. Data is single-line JSON (json.Marshal never emits a raw newline),
// so no multi-line data folding is needed.
func writeFrame(w http.ResponseWriter, f Frame) {
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", f.Seq, f.Event, f.Data)
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
