package highlight

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// ClipStore is the read surface the clip serve needs; *storage.Store satisfies
// it. GetHighlight is tenant-scoped (a foreign-tenant id reads as absent).
type ClipStore interface {
	GetHighlight(ctx context.Context, tenantID, id uuid.UUID) (storage.Highlight, error)
}

// ClipServer serves a Highlight's audio clip over plain HTTP (#308/#309): GET
// /api/v1/highlights/{id}/clip, mounted behind auth.RequireSession beside the SSE
// relay (ADR-0015 — a byte stream, not a Connect unary). It loads the row
// tenant-scoped, fetches the clip through the blob seam (ADR-0048), and streams
// it with http.ServeContent so the browser <audio> scrubber's Range requests
// resolve to partial content.
type ClipServer struct {
	store ClipStore
	blobs blob.Store
	log   *slog.Logger
}

// NewClipServer wraps the highlight store + blob seam in a ClipServer.
func NewClipServer(store ClipStore, blobs blob.Store, log *slog.Logger) *ClipServer {
	if log == nil {
		log = slog.Default()
	}
	return &ClipServer{store: store, blobs: blobs, log: log}
}

// ServeClip streams one Highlight's clip. The auth.RequireSession wrapper has
// already authenticated the operator and injected the tenant; this handler
// re-reads the tenant to scope the row load, so a foreign-tenant id (or an
// unparsable one) is 404 — existence is never leaked. A missing blob is also 404
// (the row and clip should agree, but a purge race must not 500). http.ServeContent
// handles Range (scrub) and conditional requests off the row's created_at.
func (c *ClipServer) ServeClip(w http.ResponseWriter, req *http.Request) {
	tenantID, ok := auth.TenantID(req.Context())
	if !ok {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	id, err := uuid.Parse(req.PathValue("id"))
	if err != nil {
		http.NotFound(w, req)
		return
	}

	h, err := c.store.GetHighlight(req.Context(), tenantID, id)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, req)
		return
	}
	if err != nil {
		c.log.Error("highlight clip: load row", "err", err, "highlight", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rc, _, err := c.blobs.Get(req.Context(), h.ClipKey)
	if errors.Is(err, blob.ErrNotFound) {
		http.NotFound(w, req)
		return
	}
	if err != nil {
		c.log.Error("highlight clip: fetch blob", "err", err, "highlight", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		c.log.Error("highlight clip: read blob", "err", err, "highlight", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", h.ClipContentType)
	// ServeContent honors Range + If-Modified-Since; the name only informs a
	// fallback content-type sniff, which our explicit header pre-empts.
	http.ServeContent(w, req, "clip.wav", h.CreatedAt, bytes.NewReader(data))
}
