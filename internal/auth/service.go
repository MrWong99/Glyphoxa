package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

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

// AuthServer implements the Connect AuthService (ADR-0016 / ADR-0039): it reads
// the operator the auth interceptor resolved into the context and tears the
// session down on logout. The interceptor stack ([NewStack]) does the cookie
// validation + CSRF; this handler is the thin policy layer over it.
type AuthServer struct {
	sessions SessionDeleter
	tenants  TenantNamer
	log      *slog.Logger
}

var _ managementv1connect.AuthServiceHandler = (*AuthServer)(nil)

// NewAuthServer builds an AuthServer over the session store and the tenant
// reader (the GetCurrentUser tenant name, ADR-0055).
func NewAuthServer(sessions SessionDeleter, tenants TenantNamer, log *slog.Logger) *AuthServer {
	if log == nil {
		log = slog.Default()
	}
	return &AuthServer{sessions: sessions, tenants: tenants, log: log}
}

// GetCurrentUser returns the signed-in operator's identity — display fields,
// the internal user id, and the resolved bound Tenant's id/name (ADR-0055; the
// tenant interceptor already resolved the id server-side, ADR-0039). The auth
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
	secure := headerSecure(req.Header())
	resp.Header().Add("Set-Cookie", clearCookie(SessionCookieName, true, secure).String())
	resp.Header().Add("Set-Cookie", clearCookie(CSRFCookieName, false, secure).String())
	return resp, nil
}

// Handler builds the Connect HTTP handler for AuthService and returns the path +
// handler, mirroring (*rpc.CampaignServer).Handler. Pass the auth interceptor
// stack via opts (see [Stack.HandlerOptions]).
func (s *AuthServer) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return managementv1connect.NewAuthServiceHandler(s, opts...)
}
