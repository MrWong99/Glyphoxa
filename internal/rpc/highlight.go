package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/highlight"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Session Highlights RPCs (#308, Epic 8) on SessionService: List/Get read the
// tenant's highlights, Promote keeps a candidate past the 7-day purge, Delete
// drops a highlight and its clip (blob-then-row, ADR-0048). Every method is
// tenant-scoped AND Active-Campaign-scoped server-side (ADR-0039, #342/#353/#356):
// the client never supplies a tenant, the Campaign is resolved server-side (live
// Voice Session first, else the profile-first durable selection), and a highlight
// (or session) belonging to ANOTHER campaign is CodeNotFound — existence never
// leaked, exactly the GenerateRecap "names nothing" posture. PromoteHighlight
// deliberately does NOT enqueue enrichment; that is #311's hook.

// errNoSuchHighlight is the single static NotFound every Highlight RPC returns for
// a foreign-tenant, cross-campaign, unknown, or unparsable id — so a probe can
// never learn whether an id it does not own exists (mirrors SetAgentMute /
// GenerateRecap).
func errNoSuchHighlight() *connect.Error {
	return connect.NewError(connect.CodeNotFound, errors.New("no such highlight"))
}

// activeCampaignForHighlight resolves the Active Campaign every Highlight RPC scopes
// to (the SAME searchCampaign policy the other reads use: live Voice Session first,
// else the profile-first durable selection). A resolve failure is CodeInternal; NO
// active campaign is treated as "the id names nothing" — the static NotFound — so
// the scoping check has a campaign to compare against without leaking a distinct
// error.
func (s *SessionServer) activeCampaignForHighlight(ctx context.Context) (uuid.UUID, error) {
	campaignID, ok, err := s.searchCampaign(ctx)
	if err != nil {
		s.log.Error("Highlights: resolve active campaign failed", "err", err)
		return uuid.Nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if !ok {
		return uuid.Nil, errNoSuchHighlight()
	}
	return campaignID, nil
}

// HighlightStore is the storage surface the Highlight RPCs need; *storage.Store
// satisfies it. Every method is tenant-scoped.
type HighlightStore interface {
	ListHighlights(ctx context.Context, tenantID, voiceSessionID uuid.UUID) ([]storage.Highlight, error)
	GetHighlight(ctx context.Context, tenantID, id uuid.UUID) (storage.Highlight, error)
	PromoteHighlight(ctx context.Context, tenantID, id uuid.UUID) (storage.Highlight, error)
	DeleteHighlight(ctx context.Context, tenantID, id uuid.UUID) (string, error)
}

// highlightBlobs is the blob-seam surface DeleteHighlight needs (ADR-0048): drop
// the clip (and image, #311) before the row. *blob.Postgres satisfies it. Kept
// narrow so the RPC package carries no import of the concrete backend.
type highlightBlobs interface {
	Delete(ctx context.Context, key string) error
}

// HighlightEnqueuer schedules the image-enrichment job PromoteHighlight fires
// (#311, ADR-0049). It is the same kind/payload/run_after adapter the Saver's
// purge scheduler uses; main.go wires *storage.Store behind it. A nil enqueuer
// (unwired, keyless tests) makes promotion skip enrichment — the promote itself
// still succeeds.
type HighlightEnqueuer interface {
	Enqueue(ctx context.Context, kind string, payload any, runAfter time.Time) error
}

// SetHighlights wires the Session Highlights read/mutate seam onto the
// SessionServer (#308). Called once at boot after the store + blob backend are
// built; the many NewSessionServer call sites keep their signature. Unwired, the
// Highlight RPCs report CodeUnimplemented. enqueue (#311) is the image-enrichment
// job scheduler PromoteHighlight fires; nil disables enrichment (promote still
// works).
func (s *SessionServer) SetHighlights(store HighlightStore, blobs highlightBlobs, enqueue HighlightEnqueuer) {
	s.highlights = store
	s.blobs = blobs
	s.enqueue = enqueue
}

// notWired is the CodeUnimplemented error a Highlight RPC returns when the server
// was built without the highlight seam (web-standalone tests, keyless boots).
func (s *SessionServer) highlightsEnabled() error {
	if s.highlights == nil {
		return connect.NewError(connect.CodeUnimplemented, errors.New("highlights are not enabled on this server"))
	}
	return nil
}

// ListHighlights returns the tenant's Highlights for one Voice Session in the
// Active Campaign, newest moment first (#308). The Campaign is resolved server-side
// and the Voice Session MUST belong to it: an unparsable id, an unknown session, or
// a session in another campaign is CodeNotFound (it names nothing in the Active
// Campaign — the GenerateRecap posture), never CodeInvalidArgument. A session it
// owns with no highlights yields an empty list.
func (s *SessionServer) ListHighlights(
	ctx context.Context,
	req *connect.Request[managementv1.ListHighlightsRequest],
) (*connect.Response[managementv1.ListHighlightsResponse], error) {
	if err := s.highlightsEnabled(); err != nil {
		return nil, err
	}
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	sessionID, perr := uuid.Parse(req.Msg.GetVoiceSessionId())
	if perr != nil {
		return nil, errNoSuchHighlight() // a non-UUID id names no session
	}

	campaignID, err := s.activeCampaignForHighlight(ctx)
	if err != nil {
		return nil, err
	}
	// The session must belong to the Active Campaign, else it is cross-campaign and
	// NotFound (existence never leaked) — the same ownership check GenerateRecap runs.
	vs, gerr := s.store.GetVoiceSession(ctx, sessionID)
	if errors.Is(gerr, storage.ErrNotFound) {
		return nil, errNoSuchHighlight()
	}
	if gerr != nil {
		s.log.Error("ListHighlights: load voice session failed", "err", gerr)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if vs.CampaignID != campaignID {
		return nil, errNoSuchHighlight()
	}

	rows, err := s.highlights.ListHighlights(ctx, tenantID, sessionID)
	if err != nil {
		s.log.Error("ListHighlights: store list failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	out := make([]*managementv1.Highlight, 0, len(rows))
	for _, h := range rows {
		out = append(out, toProtoHighlight(h))
	}
	return connect.NewResponse(&managementv1.ListHighlightsResponse{Highlights: out}), nil
}

// GetHighlight returns one Highlight by id, tenant- AND Active-Campaign-scoped
// (#308). A foreign-tenant, cross-campaign, unknown, or unparsable id is all the
// same CodeNotFound (existence never leaked).
func (s *SessionServer) GetHighlight(
	ctx context.Context,
	req *connect.Request[managementv1.GetHighlightRequest],
) (*connect.Response[managementv1.GetHighlightResponse], error) {
	if err := s.highlightsEnabled(); err != nil {
		return nil, err
	}
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	id, perr := uuid.Parse(req.Msg.GetId())
	if perr != nil {
		return nil, errNoSuchHighlight()
	}
	campaignID, err := s.activeCampaignForHighlight(ctx)
	if err != nil {
		return nil, err
	}

	h, err := s.highlights.GetHighlight(ctx, tenantID, id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, errNoSuchHighlight()
	}
	if err != nil {
		s.log.Error("GetHighlight: store get failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if h.CampaignID != campaignID {
		return nil, errNoSuchHighlight() // cross-campaign: never leak that it exists
	}
	return connect.NewResponse(&managementv1.GetHighlightResponse{Highlight: toProtoHighlight(h)}), nil
}

// PromoteHighlight flips a candidate Highlight to 'promoted' within the tenant and
// Active Campaign (#308/#309), returning the updated row. Idempotent on the server.
// A foreign-tenant, cross-campaign, unknown, or unparsable id is CodeNotFound. The
// campaign ownership is checked BEFORE the mutation, so another campaign's row is
// never promoted. It does NOT enqueue enrichment (#311's hook).
func (s *SessionServer) PromoteHighlight(
	ctx context.Context,
	req *connect.Request[managementv1.PromoteHighlightRequest],
) (*connect.Response[managementv1.PromoteHighlightResponse], error) {
	if err := s.highlightsEnabled(); err != nil {
		return nil, err
	}
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	id, perr := uuid.Parse(req.Msg.GetId())
	if perr != nil {
		return nil, errNoSuchHighlight()
	}
	campaignID, err := s.activeCampaignForHighlight(ctx)
	if err != nil {
		return nil, err
	}
	// Load first to check campaign ownership BEFORE mutating — a cross-campaign row
	// must never be promoted.
	existing, gerr := s.highlights.GetHighlight(ctx, tenantID, id)
	if errors.Is(gerr, storage.ErrNotFound) {
		return nil, errNoSuchHighlight()
	}
	if gerr != nil {
		s.log.Error("PromoteHighlight: load row failed", "err", gerr)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if existing.CampaignID != campaignID {
		return nil, errNoSuchHighlight()
	}

	h, err := s.highlights.PromoteHighlight(ctx, tenantID, id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, errNoSuchHighlight()
	}
	if err != nil {
		s.log.Error("PromoteHighlight: store promote failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	// Enqueue AI image enrichment (#311, ADR-0049): a promoted Highlight with no
	// image yet gets a background generation job. Skipped when already enriched
	// (an idempotent re-promote never re-spends) or when the enqueuer is unwired.
	// An enqueue failure is logged only — the promotion succeeded and the boot
	// backstop / a re-promote can re-trigger enrichment; failing the RPC here would
	// wrongly report the keep as failed.
	if s.enqueue != nil && h.ImageKey == "" {
		payload, merr := highlight.MarshalEnrichImage(h.ID, tenantID)
		if merr != nil {
			s.log.Error("PromoteHighlight: marshal enrich payload failed", "err", merr, "highlight", h.ID)
		} else if eerr := s.enqueue.Enqueue(ctx, highlight.JobKindEnrichImage, json.RawMessage(payload), time.Now()); eerr != nil {
			s.log.Error("PromoteHighlight: enqueue image enrichment failed", "err", eerr, "highlight", h.ID)
		}
	}
	return connect.NewResponse(&managementv1.PromoteHighlightResponse{Highlight: toProtoHighlight(h)}), nil
}

// DeleteHighlight removes one Highlight and its clip within the tenant and Active
// Campaign (#308). Order is blob-then-row (ADR-0048): the row is loaded to read its
// clip_key AND check campaign ownership BEFORE removing anything, the clip is
// dropped through the seam (idempotent — an absent clip still deletes the row), then
// the row is removed. A foreign-tenant, cross-campaign, unknown, or unparsable id is
// CodeNotFound with no side effects.
func (s *SessionServer) DeleteHighlight(
	ctx context.Context,
	req *connect.Request[managementv1.DeleteHighlightRequest],
) (*connect.Response[managementv1.DeleteHighlightResponse], error) {
	if err := s.highlightsEnabled(); err != nil {
		return nil, err
	}
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	id, perr := uuid.Parse(req.Msg.GetId())
	if perr != nil {
		return nil, errNoSuchHighlight()
	}
	campaignID, err := s.activeCampaignForHighlight(ctx)
	if err != nil {
		return nil, err
	}

	// Load the row first so we have the clip key BEFORE removing anything, and so a
	// foreign-tenant / cross-campaign / unknown id is NotFound without side effects.
	h, err := s.highlights.GetHighlight(ctx, tenantID, id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, errNoSuchHighlight()
	}
	if err != nil {
		s.log.Error("DeleteHighlight: load row failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if h.CampaignID != campaignID {
		return nil, errNoSuchHighlight() // cross-campaign: never delete, never leak existence
	}

	// Blob first (idempotent Delete): a missing clip must not block the row delete.
	// A Highlight owns up to two blobs — the audio clip and (once enriched, #311)
	// the generated image — so drop both before the row (ADR-0048). The image key
	// is only present on an enriched row; an empty one is skipped.
	if s.blobs != nil {
		if err := s.blobs.Delete(ctx, h.ClipKey); err != nil {
			s.log.Error("DeleteHighlight: delete clip failed", "err", err, "highlight", id)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
		if h.ImageKey != "" {
			if err := s.blobs.Delete(ctx, h.ImageKey); err != nil {
				s.log.Error("DeleteHighlight: delete image failed", "err", err, "highlight", id)
				return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
			}
		}
	}
	if _, err := s.highlights.DeleteHighlight(ctx, tenantID, id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, errNoSuchHighlight()
		}
		s.log.Error("DeleteHighlight: delete row failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.DeleteHighlightResponse{}), nil
}

// toProtoHighlight maps a storage.Highlight onto its wire view (#308). clip_key
// is deliberately omitted (an internal blob-seam detail); promoted_at is set only
// once the row is promoted.
func toProtoHighlight(h storage.Highlight) *managementv1.Highlight {
	pb := &managementv1.Highlight{
		Id:              h.ID.String(),
		VoiceSessionId:  h.VoiceSessionID.String(),
		CampaignId:      h.CampaignID.String(),
		Status:          h.Status,
		StartsAt:        timestamppb.New(h.StartsAt),
		EndsAt:          timestamppb.New(h.EndsAt),
		Score:           h.Score,
		Excerpt:         h.Excerpt,
		Reason:          h.Reason,
		SpeakerIds:      h.SpeakerIDs,
		ClipContentType: h.ClipContentType,
		ClipSizeBytes:   h.ClipSizeBytes,
		// Image fields (#311): content type + size are exposed so the UI knows an
		// image exists and its type; image_key is deliberately omitted (an internal
		// blob-seam detail, the clip_key posture). Empty when no image yet.
		ImageContentType: h.ImageContentType,
		ImageSizeBytes:   h.ImageSizeBytes,
		CreatedAt:        timestamppb.New(h.CreatedAt),
	}
	if h.PromotedAt != nil {
		pb.PromotedAt = timestamppb.New(*h.PromotedAt)
	}
	return pb
}
