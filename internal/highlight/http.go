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

// CampaignResolver resolves the request's Active Campaign server-side (#308): the
// live Voice Session's campaign first, else the operator's profile-first durable
// selection. ok is false when neither resolves (a never-run state). The web tier
// wires *rpc.SessionServer.ResolveActiveCampaign; nil disables campaign scoping
// (tenant-only, e.g. a voice-standalone build with no web resolver).
type CampaignResolver func(ctx context.Context) (uuid.UUID, bool, error)

// ClipServer serves a Highlight's audio clip over plain HTTP (#308/#309): GET
// /api/v1/highlights/{id}/clip, mounted behind auth.RequireSession +
// auth.RequireTenant (#408 — session AND tenant) beside the SSE relay (ADR-0015 —
// a byte stream, not a Connect unary). It loads the row
// tenant-scoped, fetches the clip through the blob seam (ADR-0048), and streams
// it with http.ServeContent so the browser <audio> scrubber's Range requests
// resolve to partial content.
type ClipServer struct {
	store   ClipStore
	blobs   blob.Store
	resolve CampaignResolver
	log     *slog.Logger
}

// NewClipServer wraps the highlight store + blob seam + Active-Campaign resolver in
// a ClipServer. A nil resolver disables campaign scoping (tenant-only).
func NewClipServer(store ClipStore, blobs blob.Store, resolve CampaignResolver, log *slog.Logger) *ClipServer {
	if log == nil {
		log = slog.Default()
	}
	return &ClipServer{store: store, blobs: blobs, resolve: resolve, log: log}
}

// ServeClip streams one Highlight's clip. CONTRACT (#408): the mount must be
// "session AND tenant", not just session — auth.RequireSession authenticates the
// operator, and auth.RequireTenant (composed inside it) resolves and injects the
// tenant server-side. RequireSession ALONE injects only the user, so TenantID
// misses and this handler 401s every request (the production bug #408 fixed). This
// handler re-reads that injected tenant to scope the row load, so a foreign-tenant
// id (or an unparsable one) is 404 — existence is never leaked. A missing blob is also 404
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

	// Active-Campaign scoping (#308): a clip whose highlight belongs to another
	// campaign than the resolved Active Campaign is 404 — existence never leaked, the
	// same posture the Highlight RPCs adopt. A nil resolver leaves scoping tenant-only.
	if c.resolve != nil {
		campaignID, ok, rerr := c.resolve(req.Context())
		if rerr != nil {
			c.log.Error("highlight clip: resolve active campaign", "err", rerr, "highlight", id)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !ok || h.CampaignID != campaignID {
			http.NotFound(w, req)
			return
		}
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

// ServeImage streams one Highlight's AI-generated image (#311), mirroring
// ServeClip: GET /api/v1/highlights/{id}/image behind auth.RequireSession +
// auth.RequireTenant (session AND tenant, per #408 — see ServeClip). It
// applies the same tenant + Active-Campaign 404 posture (existence never leaked)
// and serves through http.ServeContent (Range/conditional). A Highlight with no
// image yet (image_key == "") is 404 — the enrichment has not run, is not
// configured, or failed, and there is nothing to serve. A missing blob is also
// 404 (a purge race must not 500).
func (c *ClipServer) ServeImage(w http.ResponseWriter, req *http.Request) {
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
		c.log.Error("highlight image: load row", "err", err, "highlight", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Active-Campaign scoping (#308 posture): an image whose highlight belongs to a
	// campaign other than the resolved Active Campaign is 404. A nil resolver leaves
	// scoping tenant-only.
	if c.resolve != nil {
		campaignID, ok, rerr := c.resolve(req.Context())
		if rerr != nil {
			c.log.Error("highlight image: resolve active campaign", "err", rerr, "highlight", id)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !ok || h.CampaignID != campaignID {
			http.NotFound(w, req)
			return
		}
	}

	// No image yet: nothing to serve. 404 keeps the posture uniform with a foreign id.
	if h.ImageKey == "" {
		http.NotFound(w, req)
		return
	}

	rc, _, err := c.blobs.Get(req.Context(), h.ImageKey)
	if errors.Is(err, blob.ErrNotFound) {
		http.NotFound(w, req)
		return
	}
	if err != nil {
		c.log.Error("highlight image: fetch blob", "err", err, "highlight", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		c.log.Error("highlight image: read blob", "err", err, "highlight", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", h.ImageContentType)
	http.ServeContent(w, req, "image", h.CreatedAt, bytes.NewReader(data))
}
