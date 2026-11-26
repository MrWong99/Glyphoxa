package rpc

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// AdminStore is the deployment-scoped persistence behind the Platform Admin
// console (ADR-0065). None of it takes a tenant id — which is exactly why
// every AdminServer handler gates on [AdminServer.requireAdmin] before it
// touches the store. *storage.Store satisfies it.
type AdminStore interface {
	ListAccessPeople(ctx context.Context, includeDev bool) ([]storage.AccessPerson, error)
	ListAccessInvitations(ctx context.Context) ([]storage.Invitation, error)
	GrantAccess(ctx context.Context, discordUserID, note string, platformKeys bool, grantedBy uuid.UUID) (storage.AccessGrant, error)
	RevokeAccess(ctx context.Context, discordUserID string) error
	CreateInvitation(ctx context.Context, n storage.NewInvitation) (storage.Invitation, error)
	RevokeAccessInvitation(ctx context.Context, id uuid.UUID) error
	SetUserSuspended(ctx context.Context, discordUserID string, suspended bool) error
	SetTenantPlatformKeys(ctx context.Context, tenantID uuid.UUID, enabled bool) error
}

// Input caps of the Admin console. Notes and labels are one-line reminders
// ("plays Fridays"), not documents.
const (
	maxAccessNoteRunes       = 200
	maxInvitationLabelRunes  = 200
	maxInvitationExpiresDays = 365
	// maxSnowflakeDigits is generous: a Discord snowflake is a uint64, at most
	// 20 decimal digits.
	maxSnowflakeDigits = 20
)

// AdminServer implements the Connect AdminService (ADR-0065): who may use this
// instance and what each Tenant may spend. It is deployment-scoped and ignores
// the caller's workspace. EVERY procedure requires a Platform Admin.
type AdminServer struct {
	store  AdminStore
	admins auth.PlatformAdmins
	mode   auth.AdmissionMode
	now    func() time.Time
	// mint produces an invitation token; a seam so tests can pin it.
	mint func() (string, error)
	log  *slog.Logger
}

var _ managementv1connect.AdminServiceHandler = (*AdminServer)(nil)

// NewAdminServer builds the AdminService over the access store. admins is the
// deployment's ONE Platform Admin identity (the same value the OAuth callback
// and GetCurrentUser use); its zero value refuses every caller. mode is the
// effective Admission Mode.
func NewAdminServer(store AdminStore, admins auth.PlatformAdmins, mode auth.AdmissionMode, log *slog.Logger) *AdminServer {
	if log == nil {
		log = slog.Default()
	}
	return &AdminServer{store: store, admins: admins, mode: mode, now: time.Now, mint: auth.NewToken, log: log}
}

// Handler builds the Connect HTTP handler for AdminService, mirroring
// (*CampaignServer).Handler. Pass the auth interceptor stack via opts.
func (s *AdminServer) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return managementv1connect.NewAdminServiceHandler(s, opts...)
}

// requireAdmin is the gate every handler runs FIRST: the caller must be a
// signed-in Platform Admin. The policy interceptor already refuses a missing
// session; the unauthenticated branch here keeps the handler safe on its own.
// TestAdminService_EveryProcedureRequiresPlatformAdmin walks the service
// descriptor, so a new procedure that forgets this call fails that test.
func (s *AdminServer) requireAdmin(ctx context.Context) (storage.User, error) {
	u, ok := auth.CurrentUser(ctx)
	if !ok {
		return storage.User{}, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}
	if !s.admins.Is(u) {
		return storage.User{}, connect.NewError(connect.CodePermissionDenied, errors.New("platform admin only"))
	}
	return u, nil
}

// snowflake validates a Discord User ID from the wire: decimal digits only.
// A pasted username or a quoted value can never match a real Discord User, so
// it would create a grant that silently admits nobody.
func snowflake(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("discord user id is required"))
	}
	if len(id) > maxSnowflakeDigits {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("discord user id must be a numeric Discord ID"))
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return "", connect.NewError(connect.CodeInvalidArgument, errors.New("discord user id must be a numeric Discord ID"))
		}
	}
	return id, nil
}

// errPlatformAdminTarget refuses grant/revoke/suspend on a Platform Admin:
// they are configured in the environment only (ADR-0065), and a row written
// here would suggest otherwise without changing what they can do.
var errPlatformAdminTarget = errors.New("this person is a Platform Admin; Platform Admins are configured in the environment and cannot be changed here")

// GetAccessOverview returns everything the Admin screen renders in one read.
func (s *AdminServer) GetAccessOverview(
	ctx context.Context, _ *connect.Request[managementv1.GetAccessOverviewRequest],
) (*connect.Response[managementv1.GetAccessOverviewResponse], error) {
	caller, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	people, err := s.store.ListAccessPeople(ctx, s.admins.Dev)
	if err != nil {
		return nil, internalErr(s.log, "admin: list access people", err)
	}
	invitations, err := s.store.ListAccessInvitations(ctx)
	if err != nil {
		return nil, internalErr(s.log, "admin: list access invitations", err)
	}

	resp := &managementv1.GetAccessOverviewResponse{AdmissionMode: admissionModePB(s.mode)}
	seen := make(map[string]struct{}, len(people))
	for _, p := range people {
		seen[p.DiscordUserID] = struct{}{}
		resp.People = append(resp.People, s.accessPersonPB(p, caller))
	}
	// A Platform Admin on the env list who has never signed in has no users
	// row, so the store cannot list them — but the screen must show who holds
	// the keys to the instance.
	for _, id := range s.admins.Allowlist.IDs() {
		if _, ok := seen[id]; ok {
			continue
		}
		resp.People = append(resp.People, &managementv1.AccessPerson{
			DiscordUserId: id,
			Status:        managementv1.AccessStatus_ACCESS_STATUS_PLATFORM_ADMIN,
			// What their first sign-in gets: they claim or seed a Tenant with
			// the column default.
			PlatformKeys: true,
		})
	}
	now := s.now()
	for _, inv := range invitations {
		resp.Invitations = append(resp.Invitations, invitationPB(inv, now))
	}
	return connect.NewResponse(resp), nil
}

// accessStatus derives how a person is admitted, most specific first.
func (s *AdminServer) accessStatus(p storage.AccessPerson) managementv1.AccessStatus {
	switch {
	case s.admins.IsDiscordUser(p.DiscordUserID):
		return managementv1.AccessStatus_ACCESS_STATUS_PLATFORM_ADMIN
	case p.HasGrant:
		return managementv1.AccessStatus_ACCESS_STATUS_GRANTED
	case p.TenantID != nil && s.mode == auth.AdmissionOpen:
		return managementv1.AccessStatus_ACCESS_STATUS_SIGNUP
	case p.TenantID == nil && p.GuestCampaignCount > 0:
		return managementv1.AccessStatus_ACCESS_STATUS_GUEST
	default:
		return managementv1.AccessStatus_ACCESS_STATUS_NONE
	}
}

func (s *AdminServer) accessPersonPB(p storage.AccessPerson, caller storage.User) *managementv1.AccessPerson {
	pb := &managementv1.AccessPerson{
		DiscordUserId:      p.DiscordUserID,
		Name:               p.Name,
		Avatar:             p.Avatar,
		Status:             s.accessStatus(p),
		Suspended:          p.SuspendedAt != nil,
		HasSignedIn:        p.Known,
		Note:               p.GrantNote,
		TenantName:         p.TenantName,
		CampaignCount:      int32(p.CampaignCount),
		GuestCampaignCount: int32(p.GuestCampaignCount),
		IsSelf:             p.DiscordUserID == caller.DiscordUserID,
	}
	if pb.Note == "" {
		pb.Note = p.InvitationLabel
	}
	if p.JoinedAt != nil {
		pb.JoinedAt = timestamppb.New(*p.JoinedAt)
	}
	if p.LastSeenAt != nil {
		pb.LastSeenAt = timestamppb.New(*p.LastSeenAt)
	}
	if p.TenantID != nil {
		pb.TenantId = p.TenantID.String()
		pb.PlatformKeys = p.TenantPlatformKeys
	} else {
		// No Tenant yet: what the grant will give the one they found.
		pb.PlatformKeys = p.HasGrant && p.GrantPlatformKeys
	}
	return pb
}

func admissionModePB(m auth.AdmissionMode) managementv1.AdmissionMode {
	if m == auth.AdmissionOpen {
		return managementv1.AdmissionMode_ADMISSION_MODE_OPEN
	}
	return managementv1.AdmissionMode_ADMISSION_MODE_ALLOWLIST
}

// invitationPB renders an invitation as its minter sees it. It never carries
// the token — storage does not have it.
func invitationPB(inv storage.Invitation, now time.Time) *managementv1.Invitation {
	pb := &managementv1.Invitation{
		Id:            inv.ID.String(),
		Kind:          managementv1.InvitationKind_INVITATION_KIND_ACCESS,
		Label:         inv.Label,
		PlatformKeys:  inv.PlatformKeys,
		DiscordUserId: inv.DiscordUserID,
		Uses:          int32(inv.Uses),
		CreatedAt:     timestamppb.New(inv.CreatedAt),
		Live:          inv.Live(now),
	}
	if inv.Kind == storage.InvitationCampaign {
		pb.Kind = managementv1.InvitationKind_INVITATION_KIND_CAMPAIGN
	}
	if inv.MaxUses != nil {
		pb.MaxUses = int32(*inv.MaxUses)
	}
	if inv.ExpiresAt != nil {
		pb.ExpiresAt = timestamppb.New(*inv.ExpiresAt)
	}
	if inv.RevokedAt != nil {
		pb.RevokedAt = timestamppb.New(*inv.RevokedAt)
	}
	return pb
}

// GrantAccess admits a Discord User by snowflake (an Access Grant).
func (s *AdminServer) GrantAccess(
	ctx context.Context, req *connect.Request[managementv1.GrantAccessRequest],
) (*connect.Response[managementv1.GrantAccessResponse], error) {
	caller, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := snowflake(req.Msg.GetDiscordUserId())
	if err != nil {
		return nil, err
	}
	note := strings.TrimSpace(req.Msg.GetNote())
	if utf8.RuneCountInString(note) > maxAccessNoteRunes {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("note must be at most 200 characters"))
	}
	if s.admins.IsDiscordUser(id) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errPlatformAdminTarget)
	}
	if _, err := s.store.GrantAccess(ctx, id, note, req.Msg.GetPlatformKeys(), caller.ID); err != nil {
		return nil, internalErr(s.log, "admin: grant access", err, "discord_user_id", id)
	}
	s.log.Info("admin: access granted", "discord_user_id", id, "platform_keys", req.Msg.GetPlatformKeys(), "by", caller.DiscordUserID)
	return connect.NewResponse(&managementv1.GrantAccessResponse{}), nil
}

// RevokeAccess removes a Discord User's Access Grant and signs them out
// everywhere; their Tenant and its data are kept.
func (s *AdminServer) RevokeAccess(
	ctx context.Context, req *connect.Request[managementv1.RevokeAccessRequest],
) (*connect.Response[managementv1.RevokeAccessResponse], error) {
	caller, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := snowflake(req.Msg.GetDiscordUserId())
	if err != nil {
		return nil, err
	}
	if s.admins.IsDiscordUser(id) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errPlatformAdminTarget)
	}
	switch err := s.store.RevokeAccess(ctx, id); {
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("this person holds no access grant"))
	case err != nil:
		return nil, internalErr(s.log, "admin: revoke access", err, "discord_user_id", id)
	}
	s.log.Info("admin: access revoked", "discord_user_id", id, "by", caller.DiscordUserID)
	return connect.NewResponse(&managementv1.RevokeAccessResponse{}), nil
}

// CreateAccessInvitation mints an ACCESS invitation link and returns its token
// — the only time the token is readable; storage keeps a digest.
func (s *AdminServer) CreateAccessInvitation(
	ctx context.Context, req *connect.Request[managementv1.CreateAccessInvitationRequest],
) (*connect.Response[managementv1.CreateAccessInvitationResponse], error) {
	caller, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	label := strings.TrimSpace(req.Msg.GetLabel())
	if utf8.RuneCountInString(label) > maxInvitationLabelRunes {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("label must be at most 200 characters"))
	}
	n := storage.NewInvitation{
		Kind:         storage.InvitationAccess,
		Label:        label,
		PlatformKeys: req.Msg.GetPlatformKeys(),
		CreatedBy:    caller.ID,
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
	if n.Token, err = s.mint(); err != nil {
		return nil, internalErr(s.log, "admin: mint invitation token", err)
	}
	inv, err := s.store.CreateInvitation(ctx, n)
	if err != nil {
		return nil, internalErr(s.log, "admin: create access invitation", err)
	}
	s.log.Info("admin: access invitation created", "invitation_id", inv.ID, "by", caller.DiscordUserID)
	return connect.NewResponse(&managementv1.CreateAccessInvitationResponse{
		Invitation: invitationPB(inv, s.now()),
		Token:      n.Token,
	}), nil
}

// RevokeAccessInvitation stops an ACCESS invitation link from working. Grants
// it already created are untouched.
func (s *AdminServer) RevokeAccessInvitation(
	ctx context.Context, req *connect.Request[managementv1.RevokeAccessInvitationRequest],
) (*connect.Response[managementv1.RevokeAccessInvitationResponse], error) {
	caller, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid invitation id"))
	}
	switch err := s.store.RevokeAccessInvitation(ctx, id); {
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("invitation not found"))
	case err != nil:
		return nil, internalErr(s.log, "admin: revoke access invitation", err, "invitation_id", id)
	}
	s.log.Info("admin: access invitation revoked", "invitation_id", id, "by", caller.DiscordUserID)
	return connect.NewResponse(&managementv1.RevokeAccessInvitationResponse{}), nil
}

// SetUserSuspended suspends or restores a user (ADR-0055 Suspension), in every
// Admission Mode. It is the tool for someone a Platform Admin did not grant:
// an `open`-mode signup, or a Campaign Guest.
func (s *AdminServer) SetUserSuspended(
	ctx context.Context, req *connect.Request[managementv1.SetUserSuspendedRequest],
) (*connect.Response[managementv1.SetUserSuspendedResponse], error) {
	caller, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := snowflake(req.Msg.GetDiscordUserId())
	if err != nil {
		return nil, err
	}
	// The caller's own row is refused by name, not only because a caller is
	// always a Platform Admin: locking yourself out of the one screen that
	// could undo it must stay impossible even if that ever changes.
	if id == caller.DiscordUserID {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("you cannot suspend yourself"))
	}
	if s.admins.IsDiscordUser(id) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errPlatformAdminTarget)
	}
	switch err := s.store.SetUserSuspended(ctx, id, req.Msg.GetSuspended()); {
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("this person has never signed in"))
	case err != nil:
		return nil, internalErr(s.log, "admin: set user suspended", err, "discord_user_id", id)
	}
	s.log.Info("admin: user suspension changed", "discord_user_id", id, "suspended", req.Msg.GetSuspended(), "by", caller.DiscordUserID)
	return connect.NewResponse(&managementv1.SetUserSuspendedResponse{}), nil
}

// SetTenantPlatformKeys decides whether a Tenant may spend the deployment's
// Platform Keys. Only meaningful in `allowlist` Admission Mode: in `open` mode
// the Plan decides (ADR-0054), and a switch that changed nothing would lie.
func (s *AdminServer) SetTenantPlatformKeys(
	ctx context.Context, req *connect.Request[managementv1.SetTenantPlatformKeysRequest],
) (*connect.Response[managementv1.SetTenantPlatformKeysResponse], error) {
	caller, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if s.mode == auth.AdmissionOpen {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("in open admission mode the plan decides who may use platform keys"))
	}
	id, err := uuid.Parse(req.Msg.GetTenantId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid tenant id"))
	}
	switch err := s.store.SetTenantPlatformKeys(ctx, id, req.Msg.GetEnabled()); {
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("tenant not found"))
	case err != nil:
		return nil, internalErr(s.log, "admin: set tenant platform keys", err, "tenant_id", id)
	}
	s.log.Info("admin: tenant platform keys changed", "tenant_id", id, "enabled", req.Msg.GetEnabled(), "by", caller.DiscordUserID)
	return connect.NewResponse(&managementv1.SetTenantPlatformKeysResponse{}), nil
}
