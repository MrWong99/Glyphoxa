// Package nodeportrait serves Knowledge Graph Node portraits over plain HTTP
// (#590).
//
// A portrait's bytes live in the blob seam (ADR-0048) and are a BYTE STREAM, so
// they mount outside the Connect unary surface (ADR-0015) as a guarded plain
// mount beside the Highlight clip/image and Map image routes — the same
// posture, for the same reason: an <img> tag cannot speak Connect.
package nodeportrait

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

// PortraitStore is the read surface the serve needs; *storage.Store satisfies
// it.
type PortraitStore interface {
	NodePortrait(ctx context.Context, campaignID, nodeID uuid.UUID) (blobKey string, updatedAt time.Time, err error)
	// PublicNodePortrait is the same read with gm_private Nodes absent — what a
	// Campaign Guest without wiki.secrets is served from (ADR-0065).
	PublicNodePortrait(ctx context.Context, campaignID, nodeID uuid.UUID) (blobKey string, updatedAt time.Time, err error)
}

// CampaignResolver resolves the request's Active Campaign server-side,
// mirroring the worldmap/highlight seam. ok=false means no campaign resolves.
type CampaignResolver func(ctx context.Context) (uuid.UUID, bool, error)

// Server serves GET /api/v1/knowledge/nodes/{id}/portrait.
//
// CONTRACT: its mount row must be TenantRequired (#408/#446) — the guard
// authenticates the operator AND injects the tenant, which this handler
// re-reads. A session-only gate would leave the tenant missing and 401 every
// request.
type Server struct {
	store   PortraitStore
	blobs   blob.Store
	resolve CampaignResolver
	log     *slog.Logger
}

// NewServer wraps the portrait store, the blob seam and the Active-Campaign
// resolver. A nil resolver FAILS CLOSED: with no way to resolve a campaign,
// every request answers 404 (resolveCampaign reports no campaign) — there is
// no tenant-only serving mode (#591 review: the doc used to promise one the
// code never had).
func NewServer(store PortraitStore, blobs blob.Store, resolve CampaignResolver, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{store: store, blobs: blobs, resolve: resolve, log: log}
}

// ServePortrait streams one Node's portrait. A Node outside the resolved
// Active Campaign, an unparsable id, a Node with no portrait, and a missing
// blob are all 404 — existence is never leaked, matching the worldmap posture.
//
// NOTE on gm_private: a private NODE's portrait is still served to the OPERATOR
// here, because every caller that reaches this mount is the GM (ADR-0041
// operator gate). A player tier would need its own visibility-filtered read —
// a player is never told a private Node's id, so there is nothing to request.
func (s *Server) ServePortrait(w http.ResponseWriter, req *http.Request) {
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

	// The Node row is campaign-scoped, and the campaign is resolved server-side,
	// so the tenant is enforced transitively: a Node in another tenant's
	// campaign can never match the resolved id.
	campaignID, ok, err := s.resolveCampaign(req.Context())
	if err != nil {
		s.log.Error("node portrait: resolve active campaign", "err", err, "node", id, "tenant", tenantID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, req)
		return
	}

	// A gm_private entry does not exist for a Campaign Guest without
	// wiki.secrets (ADR-0065); the filter is in the query, so its portrait
	// 404s exactly like an entry of another Campaign.
	read := s.store.NodePortrait
	if auth.GuestLacks(req.Context(), auth.RightWikiSecrets) {
		read = s.store.PublicNodePortrait
	}
	key, updatedAt, err := read(req.Context(), campaignID, id)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, req)
		return
	}
	if err != nil {
		s.log.Error("node portrait: load row", "err", err, "node", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// A Node with no key has no portrait — an ordinary, expected state, and it
	// must be caught HERE: blob.Get("") is ErrInvalidKey, not ErrNotFound, so
	// falling through would log a bogus internal error and answer 500.
	if key == "" {
		http.NotFound(w, req)
		return
	}

	// A portrait is immutable for a given row version: a replace writes a NEW
	// blob key, and updated_at moves with it. blob.Serve turns the pair into
	// the validators, answers a still-fresh revalidation without fetching the
	// bytes, and stamps the nosniff/CSP hardening every byte mount shares
	// (#591 review: uploads are allowlisted to raster types, but a blob stored
	// before that allowlist — or through any future writer — must still never
	// execute).
	if err := blob.Serve(w, req, s.blobs, key, blob.ServeOptions{ModTime: updatedAt}); err != nil {
		s.log.Error("node portrait: serve blob", "err", err, "node", id)
	}
}

func (s *Server) resolveCampaign(ctx context.Context) (uuid.UUID, bool, error) {
	if s.resolve == nil {
		return uuid.Nil, false, nil
	}
	return s.resolve(ctx)
}
