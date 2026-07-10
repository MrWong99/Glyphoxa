package rpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Session Highlights RPCs (#308, Epic 8) on SessionService: List/Get read the
// tenant's highlights, Promote keeps a candidate past the 7-day purge, Delete
// drops a highlight and its clip (blob-then-row, ADR-0048). Every method is
// tenant-scoped server-side (ADR-0039) — the client never supplies a tenant, and
// a foreign/unknown id is CodeNotFound (existence never leaked, the SetAgentMute
// posture). PromoteHighlight deliberately does NOT enqueue enrichment; that is
// #311's hook.

// HighlightStore is the storage surface the Highlight RPCs need; *storage.Store
// satisfies it. Every method is tenant-scoped.
type HighlightStore interface {
	ListHighlights(ctx context.Context, tenantID, voiceSessionID uuid.UUID) ([]storage.Highlight, error)
	GetHighlight(ctx context.Context, tenantID, id uuid.UUID) (storage.Highlight, error)
	PromoteHighlight(ctx context.Context, tenantID, id uuid.UUID) (storage.Highlight, error)
	DeleteHighlight(ctx context.Context, tenantID, id uuid.UUID) (string, error)
}

// highlightBlobs is the blob-seam surface DeleteHighlight needs (ADR-0048): drop
// the clip before the row. *blob.Postgres satisfies it. Kept narrow so the RPC
// package carries no import of the concrete backend.
type highlightBlobs interface {
	Delete(ctx context.Context, key string) error
}

// SetHighlights wires the Session Highlights read/mutate seam onto the
// SessionServer (#308). Called once at boot after the store + blob backend are
// built; the many NewSessionServer call sites keep their signature. Unwired, the
// Highlight RPCs report CodeUnimplemented.
func (s *SessionServer) SetHighlights(store HighlightStore, blobs highlightBlobs) {
	s.highlights = store
	s.blobs = blobs
}

// notWired is the CodeUnimplemented error a Highlight RPC returns when the server
// was built without the highlight seam (web-standalone tests, keyless boots).
func (s *SessionServer) highlightsEnabled() error {
	if s.highlights == nil {
		return connect.NewError(connect.CodeUnimplemented, errors.New("highlights are not enabled on this server"))
	}
	return nil
}

// ListHighlights returns the tenant's Highlights for one Voice Session, newest
// moment first (#308). The voice_session_id is client-supplied but the read is
// tenant-scoped, so it can only ever surface the operator's own highlights. An
// unparsable id is CodeInvalidArgument; a session with none yields an empty list.
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
	sessionID, err := uuid.Parse(req.Msg.GetVoiceSessionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid voice session id"))
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

// GetHighlight returns one Highlight by id, tenant-scoped (#308). A foreign or
// unknown id is CodeNotFound (existence never leaked); an unparsable id is the
// same NotFound (it names nothing).
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
	notFound := connect.NewError(connect.CodeNotFound, errors.New("no such highlight"))
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, notFound
	}

	h, err := s.highlights.GetHighlight(ctx, tenantID, id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, notFound
	}
	if err != nil {
		s.log.Error("GetHighlight: store get failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.GetHighlightResponse{Highlight: toProtoHighlight(h)}), nil
}

// PromoteHighlight flips a candidate Highlight to 'promoted' within the tenant
// (#308/#309), returning the updated row. Idempotent on the server. A
// foreign/unknown/unparsable id is CodeNotFound. It does NOT enqueue enrichment
// (#311's hook).
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
	notFound := connect.NewError(connect.CodeNotFound, errors.New("no such highlight"))
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, notFound
	}

	h, err := s.highlights.PromoteHighlight(ctx, tenantID, id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, notFound
	}
	if err != nil {
		s.log.Error("PromoteHighlight: store promote failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.PromoteHighlightResponse{Highlight: toProtoHighlight(h)}), nil
}

// DeleteHighlight removes one Highlight and its clip within the tenant (#308).
// Order is blob-then-row (ADR-0048): the row is loaded to read its clip_key, the
// clip is dropped through the seam (idempotent — an absent clip still deletes the
// row), then the row is removed. A foreign/unknown/unparsable id is CodeNotFound.
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
	notFound := connect.NewError(connect.CodeNotFound, errors.New("no such highlight"))
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, notFound
	}

	// Load the row first so we have the clip key BEFORE removing anything, and so a
	// foreign/unknown id is NotFound without side effects.
	h, err := s.highlights.GetHighlight(ctx, tenantID, id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, notFound
	}
	if err != nil {
		s.log.Error("DeleteHighlight: load row failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	// Blob first (idempotent Delete): a missing clip must not block the row delete.
	if s.blobs != nil {
		if err := s.blobs.Delete(ctx, h.ClipKey); err != nil {
			s.log.Error("DeleteHighlight: delete clip failed", "err", err, "highlight", id)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
	}
	if _, err := s.highlights.DeleteHighlight(ctx, tenantID, id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, notFound
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
		CreatedAt:       timestamppb.New(h.CreatedAt),
	}
	if h.PromotedAt != nil {
		pb.PromotedAt = timestamppb.New(*h.PromotedAt)
	}
	return pb
}
