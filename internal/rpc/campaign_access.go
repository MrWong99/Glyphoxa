package rpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
)

// Campaign Guest management (ADR-0065).
//
// FOUNDATION STUBS — replaced by the Campaign Guest implementation.

var errCampaignAccessUnimplemented = connect.NewError(connect.CodeUnimplemented, errors.New("not implemented"))

func (s *CampaignServer) ListCampaignAccess(
	context.Context, *connect.Request[managementv1.ListCampaignAccessRequest],
) (*connect.Response[managementv1.ListCampaignAccessResponse], error) {
	return nil, errCampaignAccessUnimplemented
}

func (s *CampaignServer) AddCampaignGuest(
	context.Context, *connect.Request[managementv1.AddCampaignGuestRequest],
) (*connect.Response[managementv1.AddCampaignGuestResponse], error) {
	return nil, errCampaignAccessUnimplemented
}

func (s *CampaignServer) UpdateCampaignGuest(
	context.Context, *connect.Request[managementv1.UpdateCampaignGuestRequest],
) (*connect.Response[managementv1.UpdateCampaignGuestResponse], error) {
	return nil, errCampaignAccessUnimplemented
}

func (s *CampaignServer) RemoveCampaignGuest(
	context.Context, *connect.Request[managementv1.RemoveCampaignGuestRequest],
) (*connect.Response[managementv1.RemoveCampaignGuestResponse], error) {
	return nil, errCampaignAccessUnimplemented
}

func (s *CampaignServer) CreateCampaignInvitation(
	context.Context, *connect.Request[managementv1.CreateCampaignInvitationRequest],
) (*connect.Response[managementv1.CreateCampaignInvitationResponse], error) {
	return nil, errCampaignAccessUnimplemented
}

func (s *CampaignServer) RevokeCampaignInvitation(
	context.Context, *connect.Request[managementv1.RevokeCampaignInvitationRequest],
) (*connect.Response[managementv1.RevokeCampaignInvitationResponse], error) {
	return nil, errCampaignAccessUnimplemented
}
