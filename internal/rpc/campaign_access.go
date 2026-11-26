package rpc

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// campaignAccess is the Campaign Guest management module (ADR-0065): who else
// may open the Active Campaign, and with which Guest Rights. Every procedure
// here is the GM's alone. The guest gate already refuses a Campaign Guest
// before any of these handlers runs (none of them is in the guest table);
// each handler nevertheless starts with [campaignAccess.requireGM], because a
// guest who could manage guests could grant themselves everything.
type campaignAccess struct {
	store  campaignAccessStore
	active *activeCampaignSource
	now    func() time.Time
	// mint produces an invitation token; a seam so tests can pin it.
	mint func() (string, error)
}

// campaignAccessStore is the narrow persistence the module needs. Every
// method is scoped to one Campaign — the resolved Active Campaign — so a GM
// can never list, change or revoke another Campaign's guests or links through
// this surface. *storage.Store satisfies it.
type campaignAccessStore interface {
	ListCampaignGuests(ctx context.Context, campaignID uuid.UUID) ([]storage.CampaignGuest, error)
	ListCampaignInvitations(ctx context.Context, campaignID uuid.UUID) ([]storage.Invitation, error)
	AddCampaignGuest(ctx context.Context, campaignID uuid.UUID, discordUserID string, rights []string, addedBy uuid.UUID) (storage.CampaignGuest, error)
	UpdateCampaignGuest(ctx context.Context, campaignID, userID uuid.UUID, rights []string) (storage.CampaignGuest, error)
	RemoveCampaignGuest(ctx context.Context, campaignID, userID uuid.UUID) error
	CreateInvitation(ctx context.Context, n storage.NewInvitation) (storage.Invitation, error)
	RevokeCampaignInvitation(ctx context.Context, campaignID, id uuid.UUID) error
}

func newCampaignAccess(store campaignAccessStore, active *activeCampaignSource) *campaignAccess {
	return &campaignAccess{store: store, active: active, now: time.Now, mint: auth.NewToken}
}

// requireGM is the gate every handler in this module runs FIRST: the caller
// must be signed in and must NOT be acting as a Campaign Guest.
func (s *campaignAccess) requireGM(ctx context.Context) (storage.User, error) {
	u, ok := auth.CurrentUser(ctx)
	if !ok {
		return storage.User{}, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}
	if _, guest := auth.Guest(ctx); guest {
		return storage.User{}, connect.NewError(connect.CodePermissionDenied, errors.New("only the GM manages who can open this campaign"))
	}
	return u, nil
}

// wireRights maps the request's rights onto the normalized stored set:
// unknown values dropped, implied rights added (ADR-0065).
func wireRights(in []managementv1.GuestRight) []string {
	return auth.GuestRightStrings(auth.GuestRightsFromProto(in))
}

func campaignGuestPB(g storage.CampaignGuest) *managementv1.CampaignGuest {
	return &managementv1.CampaignGuest{
		UserId:        g.UserID.String(),
		DiscordUserId: g.DiscordUserID,
		Name:          g.Name,
		Avatar:        g.Avatar,
		Rights:        auth.GuestRightsToProto(auth.GuestRightsFromStrings(g.Rights)),
		AddedAt:       timestamppb.New(g.AddedAt),
		HasSignedIn:   g.HasSignedIn,
	}
}

// ListCampaignAccess returns the Active Campaign's guests and its CAMPAIGN
// invitation links.
func (s *campaignAccess) ListCampaignAccess(
	ctx context.Context, _ *connect.Request[managementv1.ListCampaignAccessRequest],
) (*connect.Response[managementv1.ListCampaignAccessResponse], error) {
	if _, err := s.requireGM(ctx); err != nil {
		return nil, err
	}
	c, err := s.active.campaignFor(ctx, "ListCampaignAccess")
	if err != nil {
		return nil, err
	}
	guests, err := s.store.ListCampaignGuests(ctx, c.ID)
	if err != nil {
		return nil, internalErr(s.active.logger(), "ListCampaignAccess: list guests failed", err, "campaign_id", c.ID)
	}
	invitations, err := s.store.ListCampaignInvitations(ctx, c.ID)
	if err != nil {
		return nil, internalErr(s.active.logger(), "ListCampaignAccess: list invitations failed", err, "campaign_id", c.ID)
	}
	resp := &managementv1.ListCampaignAccessResponse{
		Guests:      mapSlice(guests, campaignGuestPB),
		Invitations: make([]*managementv1.Invitation, 0, len(invitations)),
	}
	now := s.now()
	for _, inv := range invitations {
		resp.Invitations = append(resp.Invitations, invitationPB(inv, now))
	}
	return connect.NewResponse(resp), nil
}

// AddCampaignGuest makes a Discord User a guest of the Active Campaign by
// snowflake; they need not have signed in yet. Adding an existing guest
// replaces their rights. The Tenant's own operator is CodeInvalidArgument.
func (s *campaignAccess) AddCampaignGuest(
	ctx context.Context, req *connect.Request[managementv1.AddCampaignGuestRequest],
) (*connect.Response[managementv1.AddCampaignGuestResponse], error) {
	caller, err := s.requireGM(ctx)
	if err != nil {
		return nil, err
	}
	id, err := snowflake(req.Msg.GetDiscordUserId())
	if err != nil {
		return nil, err
	}
	c, err := s.active.campaignFor(ctx, "AddCampaignGuest")
	if err != nil {
		return nil, err
	}
	g, err := s.store.AddCampaignGuest(ctx, c.ID, id, wireRights(req.Msg.GetRights()), caller.ID)
	switch {
	case errors.Is(err, storage.ErrGuestIsOperator):
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("that is this table's own GM — they already have full access"))
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
	case err != nil:
		return nil, internalErr(s.active.logger(), "AddCampaignGuest: store write failed", err, "campaign_id", c.ID)
	}
	s.active.logger().Info("campaign guest added", "campaign_id", c.ID, "guest_discord_user_id", id, "by", caller.DiscordUserID)
	return connect.NewResponse(&managementv1.AddCampaignGuestResponse{Guest: campaignGuestPB(g)}), nil
}

// UpdateCampaignGuest replaces a guest's rights. An empty set is allowed: the
// guest keeps the Campaign in their list and can open nothing in it.
func (s *campaignAccess) UpdateCampaignGuest(
	ctx context.Context, req *connect.Request[managementv1.UpdateCampaignGuestRequest],
) (*connect.Response[managementv1.UpdateCampaignGuestResponse], error) {
	caller, err := s.requireGM(ctx)
	if err != nil {
		return nil, err
	}
	userID, err := uuid.Parse(req.Msg.GetUserId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid user id"))
	}
	c, err := s.active.campaignFor(ctx, "UpdateCampaignGuest")
	if err != nil {
		return nil, err
	}
	g, err := s.store.UpdateCampaignGuest(ctx, c.ID, userID, wireRights(req.Msg.GetRights()))
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("this person is not a guest of this campaign"))
	case err != nil:
		return nil, internalErr(s.active.logger(), "UpdateCampaignGuest: store write failed", err, "campaign_id", c.ID)
	}
	s.active.logger().Info("campaign guest rights changed", "campaign_id", c.ID, "guest_user_id", userID, "by", caller.DiscordUserID)
	return connect.NewResponse(&managementv1.UpdateCampaignGuestResponse{Guest: campaignGuestPB(g)}), nil
}

// RemoveCampaignGuest ends a guest's access; it takes effect on their next
// request, because the workspace is re-resolved on every one.
func (s *campaignAccess) RemoveCampaignGuest(
	ctx context.Context, req *connect.Request[managementv1.RemoveCampaignGuestRequest],
) (*connect.Response[managementv1.RemoveCampaignGuestResponse], error) {
	caller, err := s.requireGM(ctx)
	if err != nil {
		return nil, err
	}
	userID, err := uuid.Parse(req.Msg.GetUserId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid user id"))
	}
	c, err := s.active.campaignFor(ctx, "RemoveCampaignGuest")
	if err != nil {
		return nil, err
	}
	switch err := s.store.RemoveCampaignGuest(ctx, c.ID, userID); {
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("this person is not a guest of this campaign"))
	case err != nil:
		return nil, internalErr(s.active.logger(), "RemoveCampaignGuest: store delete failed", err, "campaign_id", c.ID)
	}
	s.active.logger().Info("campaign guest removed", "campaign_id", c.ID, "guest_user_id", userID, "by", caller.DiscordUserID)
	return connect.NewResponse(&managementv1.RemoveCampaignGuestResponse{}), nil
}

// CreateCampaignInvitation mints a CAMPAIGN invitation link for the Active
// Campaign and returns its token — the only time the token is readable;
// storage keeps a digest.
func (s *campaignAccess) CreateCampaignInvitation(
	ctx context.Context, req *connect.Request[managementv1.CreateCampaignInvitationRequest],
) (*connect.Response[managementv1.CreateCampaignInvitationResponse], error) {
	caller, err := s.requireGM(ctx)
	if err != nil {
		return nil, err
	}
	label := strings.TrimSpace(req.Msg.GetLabel())
	if utf8.RuneCountInString(label) > maxInvitationLabelRunes {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("label must be at most 200 characters"))
	}
	n := storage.NewInvitation{
		Kind:      storage.InvitationCampaign,
		Label:     label,
		Rights:    wireRights(req.Msg.GetRights()),
		CreatedBy: caller.ID,
	}
	switch max := int(req.Msg.GetMaxUses()); {
	case max < 0:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("max uses must not be negative"))
	case max > 0:
		n.MaxUses = &max
	}
	switch days := req.Msg.GetExpiresInDays(); {
	case days < 0 || days > maxInvitationExpiresDays:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("expiry must be between 0 and 365 days"))
	case days > 0:
		exp := s.now().Add(time.Duration(days) * 24 * time.Hour)
		n.ExpiresAt = &exp
	}
	if pinned := strings.TrimSpace(req.Msg.GetDiscordUserId()); pinned != "" {
		if n.DiscordUserID, err = snowflake(pinned); err != nil {
			return nil, err
		}
	}
	c, err := s.active.campaignFor(ctx, "CreateCampaignInvitation")
	if err != nil {
		return nil, err
	}
	n.CampaignID = &c.ID
	if n.Token, err = s.mint(); err != nil {
		return nil, internalErr(s.active.logger(), "CreateCampaignInvitation: mint token failed", err)
	}
	inv, err := s.store.CreateInvitation(ctx, n)
	if err != nil {
		return nil, internalErr(s.active.logger(), "CreateCampaignInvitation: store write failed", err, "campaign_id", c.ID)
	}
	s.active.logger().Info("campaign invitation created", "campaign_id", c.ID, "invitation_id", inv.ID, "by", caller.DiscordUserID)
	return connect.NewResponse(&managementv1.CreateCampaignInvitationResponse{
		Invitation: invitationPB(inv, s.now()),
		Token:      n.Token,
	}), nil
}

// RevokeCampaignInvitation stops one of the Active Campaign's links from
// working. Guests it already admitted stay.
func (s *campaignAccess) RevokeCampaignInvitation(
	ctx context.Context, req *connect.Request[managementv1.RevokeCampaignInvitationRequest],
) (*connect.Response[managementv1.RevokeCampaignInvitationResponse], error) {
	caller, err := s.requireGM(ctx)
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid invitation id"))
	}
	c, err := s.active.campaignFor(ctx, "RevokeCampaignInvitation")
	if err != nil {
		return nil, err
	}
	switch err := s.store.RevokeCampaignInvitation(ctx, c.ID, id); {
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such invitation for this campaign"))
	case err != nil:
		return nil, internalErr(s.active.logger(), "RevokeCampaignInvitation: store write failed", err, "campaign_id", c.ID)
	}
	s.active.logger().Info("campaign invitation revoked", "campaign_id", c.ID, "invitation_id", id, "by", caller.DiscordUserID)
	return connect.NewResponse(&managementv1.RevokeCampaignInvitationResponse{}), nil
}
