package auth

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
)

// GetInvitation previews a `/join/<token>` link (ADR-0065).
//
// FOUNDATION STUB — replaced by the instance-access implementation.
func (s *AuthServer) GetInvitation(
	context.Context, *connect.Request[managementv1.GetInvitationRequest],
) (*connect.Response[managementv1.GetInvitationResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("not implemented"))
}

// SwitchWorkspace selects the session's workspace (ADR-0065).
//
// FOUNDATION STUB — replaced by the Campaign Guest implementation.
func (s *AuthServer) SwitchWorkspace(
	context.Context, *connect.Request[managementv1.SwitchWorkspaceRequest],
) (*connect.Response[managementv1.SwitchWorkspaceResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("not implemented"))
}
