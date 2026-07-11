package rpc

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/highlight"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Campaign archive/delete lifecycle handlers (#269, decided on #265) on
// CampaignServer. Archive is the primary flow; hard delete is only for
// already-archived campaigns. Archive and Delete refuse the campaign backing the
// LIVE Voice Session (the same in-process liveCampaign truth resolveActiveCampaign
// consults, ADR-0039 — no second session-truth source). Error mapping mirrors the
// campaign management handlers: ErrNotFound→CodeNotFound, ErrNotArchived→
// CodeFailedPrecondition, an unparsable id→CodeInvalidArgument, generic
// CodeInternal with a server-side slog.

// liveGuard refuses an operation on the campaign backing the LIVE Voice Session
// (#265): the campaign that is currently voicing can be neither archived nor
// deleted out from under it. It consults the SAME liveCampaign closure
// resolveActiveCampaign uses, so there is one source of session truth (ADR-0039).
// It returns nil when no session is live, the source is unwired (keyless tests),
// or a DIFFERENT campaign is live. Note the inherent TOCTOU: liveCampaign is
// in-process manager state and a session could end (or start) in the millisecond
// after this check — accepted for the single-operator web tier, where the window
// is negligible and the DB cascade is still safe either way.
func (s *CampaignServer) liveGuard(id uuid.UUID, verb string) error {
	if s.liveCampaign == nil {
		return nil
	}
	if lid, active := s.liveCampaign(); active && lid == id {
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("campaign backs the live Voice Session and cannot be "+verb+" while it runs"))
	}
	return nil
}

// ArchiveCampaign archives a campaign so it drops out of the list, the /glyphoxa
// use autocomplete, and the Active-Campaign resolution, and can no longer start a
// Voice Session (#269). It refuses the live session's campaign
// (CodeFailedPrecondition) and an unknown id (CodeNotFound); the store write is
// idempotent, so re-archiving is a no-op returning the same campaign.
func (s *CampaignServer) ArchiveCampaign(
	ctx context.Context,
	req *connect.Request[managementv1.ArchiveCampaignRequest],
) (*connect.Response[managementv1.ArchiveCampaignResponse], error) {
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid campaign id"))
	}
	if err := s.liveGuard(id, "archived"); err != nil {
		return nil, err
	}

	c, err := s.store.ArchiveCampaign(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("campaign not found"))
		}
		slog.Default().Error("ArchiveCampaign: store archive failed", "campaign_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.ArchiveCampaignResponse{Campaign: toProtoCampaign(c)}), nil
}

// UnarchiveCampaign returns an archived campaign to the active set (#269). There
// is no live-guard: a live session's campaign is never archived, so it can never
// be a target here. An unknown id is CodeNotFound.
func (s *CampaignServer) UnarchiveCampaign(
	ctx context.Context,
	req *connect.Request[managementv1.UnarchiveCampaignRequest],
) (*connect.Response[managementv1.UnarchiveCampaignResponse], error) {
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid campaign id"))
	}

	c, err := s.store.UnarchiveCampaign(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("campaign not found"))
		}
		slog.Default().Error("UnarchiveCampaign: store unarchive failed", "campaign_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.UnarchiveCampaignResponse{Campaign: toProtoCampaign(c)}), nil
}

// DeleteCampaign permanently removes an already-archived campaign and everything
// cascading from it (#269). It refuses the live session's campaign
// (CodeFailedPrecondition), a non-archived campaign (CodeFailedPrecondition —
// archive first), and an unknown id (CodeNotFound). The re-typed name confirmation
// is a UI-only guard (the request carries only the id); the server precondition is
// purely "already archived".
func (s *CampaignServer) DeleteCampaign(
	ctx context.Context,
	req *connect.Request[managementv1.DeleteCampaignRequest],
) (*connect.Response[managementv1.DeleteCampaignResponse], error) {
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid campaign id"))
	}
	if err := s.liveGuard(id, "deleted"); err != nil {
		return nil, err
	}

	// Capture the campaign's Highlight clip keys BEFORE the delete — the row cascade
	// removes the highlight rows, after which they can't be listed (#308, ADR-0048).
	var clipKeys []string
	if s.clips != nil {
		keys, err := s.clips.CampaignClipKeys(ctx, id)
		if err != nil {
			slog.Default().Error("DeleteCampaign: list highlight clip keys failed", "campaign_id", id, "err", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
		clipKeys = keys
	}

	// The blob sweep is a DURABLE job enqueued in the delete's OWN transaction
	// (#308, ADR-0049): it exists iff the delete committed, so a refused delete never
	// schedules a sweep of a surviving campaign's clips, and a crash right after the
	// delete never loses the sweep. With no clips there is nothing to sweep, so the
	// plain delete runs. The inline best-effort sweep below is a fast-path; the job is
	// the backstop that guarantees eventual cleanup.
	var deleteErr error
	if len(clipKeys) > 0 {
		payload, merr := highlight.MarshalCampaignSweep(clipKeys)
		if merr != nil {
			slog.Default().Error("DeleteCampaign: marshal clip sweep payload failed", "campaign_id", id, "err", merr)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
		deleteErr = s.store.DeleteCampaignWithJob(ctx, id, highlight.JobKindSweepCampaignClips, payload)
	} else {
		deleteErr = s.store.DeleteCampaign(ctx, id)
	}
	if deleteErr != nil {
		switch {
		case errors.Is(deleteErr, storage.ErrNotFound):
			return nil, connect.NewError(connect.CodeNotFound, errors.New("campaign not found"))
		case errors.Is(deleteErr, storage.ErrNotArchived):
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("campaign must be archived before deletion"))
		default:
			slog.Default().Error("DeleteCampaign: store delete failed", "campaign_id", id, "err", deleteErr)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
	}

	// Fast-path: the rows are gone (cascade); drop their clips through the seam now so
	// storage reclaims immediately. Best-effort — a failure here logs and leaves the
	// blob for the durable sweep job (idempotent Delete), never failing the RPC.
	for _, k := range clipKeys {
		if err := s.clips.DeleteClip(ctx, k); err != nil {
			slog.Default().Warn("DeleteCampaign: highlight clip sweep deferred to job", "campaign_id", id, "key", k, "err", err)
		}
	}
	return connect.NewResponse(&managementv1.DeleteCampaignResponse{}), nil
}
