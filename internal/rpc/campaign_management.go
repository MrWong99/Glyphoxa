package rpc

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Campaign management handlers (#264, Epic 2/8) on the campaignManagement module:
// list, create,
// update, and durable-select the operator's campaigns. They follow the same error
// mapping the agent CRUD uses (ErrNotFound→CodeNotFound, empty name→
// CodeInvalidArgument, generic CodeInternal with a server-side slog). Reads are
// NO_SIDE_EFFECTS; mutations are CSRF-guarded by the interceptor stack.

// ListCampaigns returns the operator's campaigns in name order (the store's stable
// name-then-id ordering). By default only ACTIVE campaigns are returned; when
// include_archived is set the archived ones are included too — the archive-
// management panel's view (#269). A storage failure is CodeInternal.
func (s *campaignManagement) ListCampaigns(
	ctx context.Context,
	req *connect.Request[managementv1.ListCampaignsRequest],
) (*connect.Response[managementv1.ListCampaignsResponse], error) {
	list := s.store.ListCampaigns
	if req.Msg.GetIncludeArchived() {
		list = s.store.ListAllCampaigns
	}
	campaigns, err := list(ctx)
	if err != nil {
		slog.Default().Error("ListCampaigns: store list failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	out := make([]*managementv1.Campaign, 0, len(campaigns))
	for _, c := range campaigns {
		out = append(out, toProtoCampaign(c))
	}
	return connect.NewResponse(&managementv1.ListCampaignsResponse{Campaigns: out}), nil
}

// CreateCampaign creates a campaign in the operator's tenant and returns it. name
// is required (empty is CodeInvalidArgument); system/language are optional
// free-text stored verbatim. The tenant is resolved server-side from the auth
// interceptor's context (ADR-0039), never a client-supplied id. The ADR-0009
// auto-Butler trigger fires on the insert, so the new campaign gets its Butler
// (with the dice grant) as a database invariant. The created row is read back so
// the response carries the server-assigned id and timestamps.
func (s *campaignManagement) CreateCampaign(
	ctx context.Context,
	req *connect.Request[managementv1.CreateCampaignRequest],
) (*connect.Response[managementv1.CreateCampaignResponse], error) {
	m := req.Msg
	name := strings.TrimSpace(m.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))
	}

	tenantID, ok := auth.TenantID(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no tenant in context"))
	}

	id, err := s.store.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: tenantID,
		Name:     name,
		System:   m.GetSystem(),
		Language: m.GetLanguage(),
	})
	if err != nil {
		slog.Default().Error("CreateCampaign: store create failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	// Read the row back so the response carries the canonical persisted shape
	// (server-assigned id, created_at/updated_at), mirroring CreateAgent.
	created, err := s.store.GetCampaign(ctx, id)
	if err != nil {
		slog.Default().Error("CreateCampaign: read-back failed", "campaign_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.CreateCampaignResponse{Campaign: toProtoCampaign(created)}), nil
}

// UpdateCampaign writes a campaign's name/system/language and returns the updated
// row. id is required (unparsable is CodeInvalidArgument) and name must be
// non-empty (CodeInvalidArgument). System/Language are written opaquely, exactly
// as stored today — no validation, no vocabulary curation (that is the settings
// editor slice's call). An unknown id is CodeNotFound.
func (s *campaignManagement) UpdateCampaign(
	ctx context.Context,
	req *connect.Request[managementv1.UpdateCampaignRequest],
) (*connect.Response[managementv1.UpdateCampaignResponse], error) {
	m := req.Msg
	id, err := uuid.Parse(m.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid campaign id"))
	}
	name := strings.TrimSpace(m.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))
	}

	updated, err := s.store.UpdateCampaign(ctx, storage.CampaignUpdate{
		ID:       id,
		Name:     name,
		System:   m.GetSystem(),
		Language: m.GetLanguage(),
		// tape_armed is `optional` on the wire: forward the pointer as-is so a
		// request that omits it leaves the current opt-in unchanged (ADR-0051).
		TapeArmed: m.TapeArmed,
	})
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("campaign not found"))
		}
		slog.Default().Error("UpdateCampaign: store update failed", "campaign_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.UpdateCampaignResponse{Campaign: toProtoCampaign(updated)}), nil
}

// SetActiveCampaign records the operator's durable Active Campaign selection and
// returns the resulting resolved Active Campaign. The campaign_id is validated via
// GetCampaign first (an unparsable id is CodeInvalidArgument; an unknown id is
// CodeNotFound) so a bogus selection is never persisted. The selection is written
// to users.active_campaign_id keyed on the operator's DiscordUserID — the IDENTICAL
// row `/glyphoxa use` writes (migration 00014), keeping both surfaces in lockstep.
// The resolved campaign is read back through the shared live-first policy, so while
// a Voice Session is live its campaign still wins (#222) and the returned campaign
// may differ from the one just selected.
func (s *campaignManagement) SetActiveCampaign(
	ctx context.Context,
	req *connect.Request[managementv1.SetActiveCampaignRequest],
) (*connect.Response[managementv1.SetActiveCampaignResponse], error) {
	id, err := uuid.Parse(req.Msg.GetCampaignId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid campaign id"))
	}

	u, ok := auth.CurrentUser(ctx)
	if !ok || u.DiscordUserID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no operator in context"))
	}

	// Validate the target exists before persisting the pointer — an unknown id is
	// a client error (CodeNotFound), never a silently-stored dangling selection.
	target, err := s.store.GetCampaign(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("campaign not found"))
		}
		slog.Default().Error("SetActiveCampaign: validate campaign failed", "campaign_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	// An archived campaign cannot be the Active Campaign (#269): it is excluded from
	// every resolution surface, so selecting it would resolve to a DIFFERENT campaign
	// (the fallback) — refuse up front with a clear precondition failure instead.
	if target.ArchivedAt != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("campaign is archived"))
	}

	if err := s.store.SetActiveCampaign(ctx, u.DiscordUserID, id); err != nil {
		// The write re-checks existence via the active_campaign_id FK, so a campaign
		// deleted between the validation above and this write is still CodeNotFound,
		// never a stored dangling pointer or a misleading CodeInternal.
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("campaign not found"))
		}
		slog.Default().Error("SetActiveCampaign: store write failed", "campaign_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	// Return the resolved Active Campaign via the one shared live-first policy, so
	// this surface agrees with GetActiveCampaign/GetCampaignRoster/ListNodes (#222).
	resolved, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("SetActiveCampaign: resolve active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.SetActiveCampaignResponse{Campaign: toProtoCampaign(resolved)}), nil
}
