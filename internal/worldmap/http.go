// Package worldmap serves Campaign Map images over plain HTTP (#538, ADR-0060).
//
// A Map's bytes live in the blob seam (ADR-0048) and are a BYTE STREAM, so they
// mount outside the Connect unary surface (ADR-0015) as a guarded plain mount
// beside the Highlight clip/image routes — the same posture, for the same reason:
// an <img> tag cannot speak Connect.
package worldmap

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

// MapStore is the read surface the image serve needs; *storage.Store satisfies it.
type MapStore interface {
	GetMap(ctx context.Context, campaignID, id uuid.UUID) (storage.CampaignMap, error)
}

// CampaignResolver resolves the request's Active Campaign server-side, mirroring
// the Highlight clip server's seam. ok=false means no campaign resolves.
type CampaignResolver func(ctx context.Context) (uuid.UUID, bool, error)

// ImageServer serves GET /api/v1/maps/{id}/image.
//
// CONTRACT: its mount row must be TenantRequired (#408/#446) — the guard
// authenticates the operator AND injects the tenant, which this handler re-reads
// to scope the row load. A session-only gate would leave the tenant missing and
// 401 every request.
type ImageServer struct {
	store   MapStore
	blobs   blob.Store
	resolve CampaignResolver
	log     *slog.Logger
}

// NewImageServer wraps the Map store, the blob seam and the Active-Campaign
// resolver. A nil resolver disables campaign scoping (tenant-only).
func NewImageServer(store MapStore, blobs blob.Store, resolve CampaignResolver, log *slog.Logger) *ImageServer {
	if log == nil {
		log = slog.Default()
	}
	return &ImageServer{store: store, blobs: blobs, resolve: resolve, log: log}
}

// ServeImage streams one Map's image. A Map outside the resolved Active Campaign,
// an unparsable id, a Map that carries no image at all, and a missing blob are all
// 404 — existence is never leaked, matching the Highlight serve's posture.
//
// NOTE on gm_private: a private MAP is still served to the OPERATOR here, because
// every caller that reaches this mount is the GM (ADR-0041 operator gate). The
// player-tier exclusion lives in the reads that decide which Maps a player is told
// about at all ([storage.Store.ListPlayerMaps]) — a player never learns the id, so
// there is nothing for them to request.
func (s *ImageServer) ServeImage(w http.ResponseWriter, req *http.Request) {
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

	// The Map row is campaign-scoped, and the campaign is resolved server-side, so
	// the tenant is enforced transitively: a Map in another tenant's campaign can
	// never match the resolved id.
	campaignID, ok, err := s.resolveCampaign(req.Context())
	if err != nil {
		s.log.Error("map image: resolve active campaign", "err", err, "map", id, "tenant", tenantID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, req)
		return
	}

	m, err := s.store.GetMap(req.Context(), campaignID, id)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, req)
		return
	}
	if err != nil {
		s.log.Error("map image: load row", "err", err, "map", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// A Map with no key has no picture — an imageless bundle import produces exactly
	// this row (internal/bundle/import.go). It is a 404 like any other missing
	// image, and it must be caught HERE: blob.Get("") is ErrInvalidKey, not
	// ErrNotFound, so falling through would log a bogus internal error and answer
	// 500 for an ordinary, expected state.
	if m.BlobKey == "" {
		http.NotFound(w, req)
		return
	}

	rc, meta, err := s.blobs.Get(req.Context(), m.BlobKey)
	if errors.Is(err, blob.ErrNotFound) {
		// The row and its bytes should agree, but a reconciliation race must not 500.
		http.NotFound(w, req)
		return
	}
	if err != nil {
		s.log.Error("map image: fetch blob", "err", err, "map", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		s.log.Error("map image: read blob", "err", err, "map", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if meta.ContentType != "" {
		w.Header().Set("Content-Type", meta.ContentType)
	}
	// A Map image is immutable for a given row version: a re-upload writes a NEW
	// blob key. updated_at is therefore a sound validator, and lets the browser skip
	// re-fetching a large scan on every pan.
	http.ServeContent(w, req, "", m.UpdatedAt, bytes.NewReader(data))
}

func (s *ImageServer) resolveCampaign(ctx context.Context) (uuid.UUID, bool, error) {
	if s.resolve == nil {
		return uuid.Nil, false, nil
	}
	return s.resolve(ctx)
}
