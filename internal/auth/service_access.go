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
	default:
		// ADR-0065 lane 2 seam: a live CAMPAIGN invitation previews its
		// campaign_name / tenant_name / rights here. Until then it is
		// indistinguishable from a link that does not exist.
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

// SwitchWorkspace selects the session's workspace (ADR-0065).
//
// FOUNDATION STUB — replaced by the Campaign Guest implementation.
func (s *AuthServer) SwitchWorkspace(
	context.Context, *connect.Request[managementv1.SwitchWorkspaceRequest],
) (*connect.Response[managementv1.SwitchWorkspaceResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("not implemented"))
}
