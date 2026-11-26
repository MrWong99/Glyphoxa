package auth

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

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// SessionDeleter revokes a session by token (logout). *storage.Store satisfies
// it via DeleteSession.
type SessionDeleter interface {
	DeleteSession(ctx context.Context, token string) error
}

// TenantNamer loads a Tenant by id — GetCurrentUser reads the bound Tenant's
// display name through it (ADR-0055). *storage.Store satisfies it via
// GetTenant.
type TenantNamer interface {
	GetTenant(ctx context.Context, id uuid.UUID) (storage.Tenant, error)
}

// TenantRenamer writes a Tenant's display name — the RenameTenant onboarding
// step (ADR-0055). *storage.Store satisfies it via RenameTenant.
type TenantRenamer interface {
	RenameTenant(ctx context.Context, id uuid.UUID, name string) (storage.Tenant, error)
}

// AuthServer implements the Connect AuthService (ADR-0016 / ADR-0039): it reads
// the operator the auth interceptor resolved into the context and tears the
// session down on logout. The interceptor stack ([Policy.Stack]) does the cookie
// validation + CSRF; this handler is the thin policy layer over it.
type AuthServer struct {
	sessions SessionDeleter
	tenants  TenantNamer
	renamer  TenantRenamer
	mode     AdmissionMode
	secure   bool // clear cookies with the Secure attribute ([EdgeConfig.SecureCookies])
	log      *slog.Logger

	// ADR-0065 seams, wired post-construction by the composition root. Each
	// zero value fails closed: nobody is a Platform Admin, no invitation
	// previews as valid.
	admins      PlatformAdmins
	ownTenants  TenantResolver
	invitations InvitationReader
	now         func() time.Time
}

var _ managementv1connect.AuthServiceHandler = (*AuthServer)(nil)

// NewAuthServer builds an AuthServer over the session store, the tenant reader
// (the GetCurrentUser tenant name, ADR-0055), the tenant renamer (the
// onboarding step), and the deployment's effective Admission Mode served to the
// login screen. A zero mode serves allowlist — the fail-safe default posture.
// edge must be the same [EdgeConfig] the OAuth flow was built with: Logout
// clears the cookies with the Secure attribute they were issued with.
func NewAuthServer(sessions SessionDeleter, tenants TenantNamer, renamer TenantRenamer, mode AdmissionMode, edge EdgeConfig, log *slog.Logger) *AuthServer {
	if log == nil {
		log = slog.Default()
	}
	return &AuthServer{sessions: sessions, tenants: tenants, renamer: renamer, mode: mode, secure: edge.SecureCookies, log: log, now: time.Now}
}

// SetPlatformAdmins wires the deployment's Platform Admin identity (ADR-0065)
// — GetCurrentUser reports is_platform_admin from it. Unset, nobody is one.
func (s *AuthServer) SetPlatformAdmins(admins PlatformAdmins) { s.admins = admins }

// SetOwnTenants wires the lookup behind GetCurrentUser.has_own_tenant: the
// Tenant the caller OPERATES, read directly rather than off the request's
// resolved Tenant, which names a host Tenant while a session acts as a
// Campaign Guest (ADR-0065). *storage.Store satisfies it via TenantForUser.
// Unset, the request's resolved Tenant stands in.
func (s *AuthServer) SetOwnTenants(tr TenantResolver) { s.ownTenants = tr }

// SetInvitations wires the store behind GetInvitation. Unset, every token
// previews as invalid.
func (s *AuthServer) SetInvitations(r InvitationReader) { s.invitations = r }

// GetCurrentUser returns the signed-in operator's identity — display fields,
// the internal user id, the resolved bound Tenant's id/name (ADR-0055; the
// tenant interceptor already resolved the id server-side, ADR-0039), whether
// they are a Platform Admin and whether they operate a Tenant (ADR-0065). The auth
// interceptor leaves this procedure reachable unauthenticated (it is in the
// public set), so a missing/expired session reaches here with no operator in the
// context and yields CodeUnauthenticated — the SPA's 401 → /login signal. An
// operator with no bound Tenant yet gets empty tenant fields, not an error.
func (s *AuthServer) GetCurrentUser(
	ctx context.Context,
	_ *connect.Request[managementv1.GetCurrentUserRequest],
) (*connect.Response[managementv1.GetCurrentUserResponse], error) {
	u, ok := CurrentUser(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}
	resp := &managementv1.GetCurrentUserResponse{
		User: &managementv1.User{
			Id:     u.ID.String(),
			Name:   u.Name,
			Role:   u.Role,
			Avatar: u.Avatar,
		},
	}
	resp.IsPlatformAdmin = s.admins.Is(u)
	hasOwn, err := s.hasOwnTenant(ctx, u)
	if err != nil {
		s.log.Error("get current user: resolve own tenant", "user_id", u.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	resp.HasOwnTenant = hasOwn
	if tid, ok := TenantID(ctx); ok {
		resp.TenantId = tid.String()
		switch tn, err := s.tenants.GetTenant(ctx, tid); {
		case errors.Is(err, storage.ErrNotFound):
			// The tenant vanished between the interceptor's resolution and this
			// read — a deletion race. Keep the id, serve no name.
		case err != nil:
			s.log.Error("get current user: load tenant", "tenant_id", tid, "err", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		default:
			resp.TenantName = tn.Name
		}
	}
	return connect.NewResponse(resp), nil
}

// hasOwnTenant reports whether the user operates a Tenant of their own.
func (s *AuthServer) hasOwnTenant(ctx context.Context, u storage.User) (bool, error) {
	if s.ownTenants == nil {
		_, ok := TenantID(ctx)
		return ok, nil
	}
	switch _, err := s.ownTenants.TenantForUser(ctx, u.ID); {
	case err == nil:
		return true, nil
	case errors.Is(err, storage.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// Logout deletes the server-side session row and clears the session + CSRF
// cookies. It is state-changing, so the auth + CSRF interceptors have already
// proven a valid session and a matching CSRF token before it runs.
func (s *AuthServer) Logout(
	ctx context.Context,
	req *connect.Request[managementv1.LogoutRequest],
) (*connect.Response[managementv1.LogoutResponse], error) {
	if token := cookieValue(req.Header(), SessionCookieName); token != "" {
		if err := s.sessions.DeleteSession(ctx, token); err != nil {
			s.log.Error("logout: delete session", "err", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
		}
	}
	resp := connect.NewResponse(&managementv1.LogoutResponse{})
	secure := s.secure
	resp.Header().Add("Set-Cookie", clearCookie(SessionCookieName, true, secure).String())
	resp.Header().Add("Set-Cookie", clearCookie(CSRFCookieName, false, secure).String())
	return resp, nil
}

// GetAdmissionMode returns the deployment's effective Admission Mode
// (ADR-0055) so the login screen can frame self-signup in open mode. Like
// GetCurrentUser it is in the interceptor stack's public set — the login
// screen has no session yet — and it deliberately carries nothing but the
// posture enum.
func (s *AuthServer) GetAdmissionMode(
	_ context.Context,
	_ *connect.Request[managementv1.GetAdmissionModeRequest],
) (*connect.Response[managementv1.GetAdmissionModeResponse], error) {
	mode := managementv1.AdmissionMode_ADMISSION_MODE_ALLOWLIST
	if s.mode == AdmissionOpen {
		mode = managementv1.AdmissionMode_ADMISSION_MODE_OPEN
	}
	return connect.NewResponse(&managementv1.GetAdmissionModeResponse{AdmissionMode: mode}), nil
}

// RenameTenant sets the caller's bound Tenant display name — the ADR-0055
// name-your-Tenant onboarding step. The Tenant is resolved server-side from
// the session (ADR-0039); a caller with no bound Tenant yet fails with
// CodeFailedPrecondition. The name is required and capped at 200 characters
// (CodeInvalidArgument otherwise) — a display name, not a document.
func (s *AuthServer) RenameTenant(
	ctx context.Context,
	req *connect.Request[managementv1.RenameTenantRequest],
) (*connect.Response[managementv1.RenameTenantResponse], error) {
	name := strings.TrimSpace(req.Msg.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))
	}
	if utf8.RuneCountInString(name) > 200 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name must be at most 200 characters"))
	}
	tid, ok := TenantID(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no tenant bound"))
	}
	switch ten, err := s.renamer.RenameTenant(ctx, tid, name); {
	case errors.Is(err, storage.ErrNotFound):
		// The tenant vanished between the interceptor's resolution and this
		// write — a deletion race.
		return nil, connect.NewError(connect.CodeNotFound, errors.New("tenant not found"))
	case err != nil:
		s.log.Error("rename tenant", "tenant_id", tid, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	default:
		return connect.NewResponse(&managementv1.RenameTenantResponse{
			TenantId:   ten.ID.String(),
			TenantName: ten.Name,
		}), nil
	}
}

// Handler builds the Connect HTTP handler for AuthService and returns the path +
// handler, mirroring (*rpc.CampaignServer).Handler. Pass the auth interceptor
// stack via opts (see [Stack.HandlerOptions]).
func (s *AuthServer) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return managementv1connect.NewAuthServiceHandler(s, opts...)
}
