package auth

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// InvitationReader is what the public join preview reads: the invitation
// behind a token, and the display name of whoever minted it. *storage.Store
// satisfies it.
type InvitationReader interface {
	GetInvitationByToken(ctx context.Context, token string) (storage.Invitation, error)
	GetUserByID(ctx context.Context, id uuid.UUID) (storage.User, error)
	// GetInvitationCampaign names the Campaign and host Tenant a CAMPAIGN
	// invitation opens; storage.ErrNotFound when it cannot be redeemed any
	// more (the Campaign is archived or gone).
	GetInvitationCampaign(ctx context.Context, campaignID uuid.UUID) (storage.InvitationTarget, error)
}

// GetInvitation previews a `/join/<token>` link (ADR-0065) for the join page.
// It is in the interceptor stack's public set — the visitor has no session —
// and deliberately thin: an unknown token, a dead link (revoked, expired, used
// up) and a link of a kind this server does not preview all answer the SAME
// empty `valid = false`, never CodeNotFound, so the response cannot be used to
// probe which links exist. A link pinned to one Discord User previews as valid
// for everyone: the visitor is anonymous here, and the redemption is what
// enforces the pin.
func (s *AuthServer) GetInvitation(
	ctx context.Context, req *connect.Request[managementv1.GetInvitationRequest],
) (*connect.Response[managementv1.GetInvitationResponse], error) {
	invalid := connect.NewResponse(&managementv1.GetInvitationResponse{})
	token := req.Msg.GetToken()
	if s.invitations == nil || !validJoinToken(token) {
		return invalid, nil
	}
	inv, err := s.invitations.GetInvitationByToken(ctx, token)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return invalid, nil
	case err != nil:
		s.log.Error("get invitation: load", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if !inv.Live(s.now()) {
		return invalid, nil
	}
	switch inv.Kind {
	case storage.InvitationAccess:
		return connect.NewResponse(&managementv1.GetInvitationResponse{
			Valid:       true,
			Kind:        managementv1.InvitationKind_INVITATION_KIND_ACCESS,
			InviterName: s.inviterName(ctx, inv),
		}), nil
	case storage.InvitationCampaign:
		if inv.CampaignID == nil {
			return invalid, nil
		}
		target, err := s.invitations.GetInvitationCampaign(ctx, *inv.CampaignID)
		switch {
		case errors.Is(err, storage.ErrNotFound):
			// An archived Campaign's links cannot be redeemed, so they preview
			// like any other dead link.
			return invalid, nil
		case err != nil:
			s.log.Error("get invitation: load campaign", "err", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
		return connect.NewResponse(&managementv1.GetInvitationResponse{
			Valid:        true,
			Kind:         managementv1.InvitationKind_INVITATION_KIND_CAMPAIGN,
			InviterName:  s.inviterName(ctx, inv),
			CampaignName: target.CampaignName,
			TenantName:   target.TenantName,
			Rights:       GuestRightsToProto(GuestRightsFromStrings(inv.Rights)),
		}), nil
	default:
		return invalid, nil
	}
}

// inviterName is the display name of whoever minted the invitation, or "" when
// they are gone (created_by is ON DELETE SET NULL) or cannot be read — a
// missing name must not turn a working link into an error page.
func (s *AuthServer) inviterName(ctx context.Context, inv storage.Invitation) string {
	if inv.CreatedBy == nil {
		return ""
	}
	u, err := s.invitations.GetUserByID(ctx, *inv.CreatedBy)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			s.log.Warn("get invitation: load inviter", "err", err)
		}
		return ""
	}
	return u.Name
}

// SwitchWorkspace selects what this browser session acts as (ADR-0065): the
// caller's own Tenant (empty campaign_id), or a Campaign they are a Campaign
// Guest of. The choice is written to the server-side session row and nothing
// else — it grants nothing: every later request re-resolves it against
// campaign_guest. A Campaign the caller is not a guest of (or an archived one)
// is CodeNotFound, the same answer as one that does not exist. Switching to
// "my own Tenant" without one is CodeFailedPrecondition.
func (s *AuthServer) SwitchWorkspace(
	ctx context.Context, req *connect.Request[managementv1.SwitchWorkspaceRequest],
) (*connect.Response[managementv1.SwitchWorkspaceResponse], error) {
	u, ok := CurrentUser(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}
	token := cookieValue(req.Header(), SessionCookieName)
	if s.guests == nil || s.workspaces == nil || token == "" {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("workspaces are not enabled on this server"))
	}

	var target *uuid.UUID
	if raw := req.Msg.GetCampaignId(); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid campaign id"))
		}
		target = &id
	} else {
		switch _, err := s.workspaces.OwnTenant(ctx, u); {
		case errors.Is(err, storage.ErrNotFound):
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("you have no workspace of your own"))
		case err != nil:
			s.log.Error("switch workspace: resolve own tenant", "user_id", u.ID, "err", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
	}
	switch err := s.guests.SetSessionWorkspace(ctx, u.ID, token, target); {
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such shared campaign"))
	case err != nil:
		s.log.Error("switch workspace: set session workspace", "user_id", u.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.SwitchWorkspaceResponse{}), nil
}
