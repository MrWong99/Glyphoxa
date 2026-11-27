package highlight

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

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

// ClipServer serves a Highlight's byte assets over plain HTTP: the audio clip
// (#308/#309), the AI-generated image (#311) and the generated sound (#312), at
// GET /api/v1/highlights/{id}/{clip,image,sound}. Each is a TenantRequired row
// in the guarded mount table (#408/#446 — session AND tenant) beside the SSE
// relay (ADR-0015 — a byte stream, not a Connect unary). It loads the row
// tenant-scoped and hands the bytes to [blob.Serve] (ADR-0048), which streams
// them with Range support (the browser <audio> scrubber), answers conditional
// requests without fetching the blob, and stamps the nosniff/CSP hardening
// every byte mount shares.
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

// highlightAsset picks one byte asset off a Highlight row: its blob key (""
// when the asset does not exist yet), the content type the row declares, and
// the timestamp that versions it.
type highlightAsset func(h storage.Highlight) (key, contentType string, modTime time.Time)

// ServeClip streams one Highlight's clip. A clip is written once and never
// replaced, so the row's created_at versions it.
func (c *ClipServer) ServeClip(w http.ResponseWriter, req *http.Request) {
	c.serve(w, req, "clip", func(h storage.Highlight) (string, string, time.Time) {
		return h.ClipKey, h.ClipContentType, h.CreatedAt
	})
}

// ServeImage streams one Highlight's AI-generated image (#311). A Highlight
// with no image yet (image_key == "") is 404 — the enrichment has not run, is
// not configured, or failed, and there is nothing to serve.
func (c *ClipServer) ServeImage(w http.ResponseWriter, req *http.Request) {
	c.serve(w, req, "image", func(h storage.Highlight) (string, string, time.Time) {
		return h.ImageKey, h.ImageContentType, h.CreatedAt
	})
}

// ServeSound streams one Highlight's generated sound asset (#312). A Highlight
// with no landed sound (sound_key == "") is 404 — none requested, still
// generating, unconfigured, or failed. The sound is versioned by
// sound_requested_at, not created_at: every re-run of the Add-sound action
// overwrites the SAME blob key, so the request time is what tells a browser its
// cached copy is stale.
func (c *ClipServer) ServeSound(w http.ResponseWriter, req *http.Request) {
	c.serve(w, req, "sound", func(h storage.Highlight) (string, string, time.Time) {
		modTime := h.CreatedAt
		if h.SoundRequestedAt != nil {
			modTime = *h.SoundRequestedAt
		}
		return h.SoundKey, h.SoundContentType, modTime
	})
}

// serve is the one request path behind the three asset routes; kind names the
// asset in logs. CONTRACT (#408): the mount must be "session AND tenant", not
// just session — its table row declares TenantRequired (#446), so the guard
// authenticates the operator AND resolves and injects the tenant server-side.
// A session-only gate injects just the user, so TenantID misses and this
// handler 401s every request (the production bug #408 fixed). The injected
// tenant scopes the row load, so a foreign-tenant id (or an unparsable one) is
// 404 — existence is never leaked — and so is a Highlight outside the resolved
// Active Campaign, an asset that does not exist yet, and a missing blob (the
// row and its bytes should agree, but a purge race must not 500).
func (c *ClipServer) serve(w http.ResponseWriter, req *http.Request, kind string, pick highlightAsset) {
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
		c.log.Error("highlight "+kind+": load row", "err", err, "highlight", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// A Campaign Guest (ADR-0065) is served PROMOTED Highlights only — a
	// candidate is the GM's private curation and 404s like a foreign id — and
	// only ever through a resolver: the row was loaded by the HOST Tenant, so
	// without the campaign check below a guest could fetch another Campaign's
	// asset by id.
	if _, guest := auth.Guest(req.Context()); guest && (c.resolve == nil || h.Status != storage.HighlightPromoted) {
		http.NotFound(w, req)
		return
	}

	// Active-Campaign scoping (#308): an asset whose highlight belongs to another
	// campaign than the resolved Active Campaign is 404 — existence never leaked, the
	// same posture the Highlight RPCs adopt. A nil resolver leaves scoping tenant-only.
	if c.resolve != nil {
		campaignID, ok, rerr := c.resolve(req.Context())
		if rerr != nil {
			c.log.Error("highlight "+kind+": resolve active campaign", "err", rerr, "highlight", id)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !ok || h.CampaignID != campaignID {
			http.NotFound(w, req)
			return
		}
	}

	// No asset yet: nothing to serve. 404 keeps the posture uniform with a
	// foreign id — and blob.Get("") would be ErrInvalidKey, a bogus 500.
	key, contentType, modTime := pick(h)
	if key == "" {
		http.NotFound(w, req)
		return
	}

	if err := blob.Serve(w, req, c.blobs, key, blob.ServeOptions{ContentType: contentType, ModTime: modTime}); err != nil {
		c.log.Error("highlight "+kind+": serve blob", "err", err, "highlight", id)
	}
}

// Compile-time pins: *storage.Store satisfies every narrow store seam this
// package declares, so a drift in a store method fails THIS package's build
// instead of surfacing only at the composition root (CONTRIBUTING: interface
// assertions).
var (
	_ ClipStore = (*storage.Store)(nil)
)
