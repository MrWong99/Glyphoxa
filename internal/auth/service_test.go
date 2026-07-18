package auth_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeAuthN is a keyless Authenticator: a token resolves to a user only if it is
// in the map, else ErrNotFound (the "missing/expired" path).
type fakeAuthN struct{ users map[string]storage.User }

func (f fakeAuthN) AuthenticateSession(_ context.Context, token string) (storage.User, error) {
	if u, ok := f.users[token]; ok {
		return u, nil
	}
	return storage.User{}, storage.ErrNotFound
}

type fakeTenant struct {
	id  uuid.UUID
	err error
}

func (f fakeTenant) TenantForUser(context.Context, uuid.UUID) (uuid.UUID, error) {
	return f.id, f.err
}

type fakeDeleter struct{ deleted []string }

func (f *fakeDeleter) DeleteSession(_ context.Context, token string) error {
	f.deleted = append(f.deleted, token)
	return nil
}

const (
	validToken = "valid-session-token"
	csrfValue  = "csrf-secret"
)

func operator() storage.User {
	return storage.User{
		ID: uuid.New(), DiscordUserID: "42",
		Name: "Sora Vance", Role: "operator", Avatar: "https://cdn/x.png",
	}
}

// fakeNamer is a scripted TenantNamer: known ids resolve to their Tenant, the
// rest to ErrNotFound.
type fakeNamer struct{ tenants map[uuid.UUID]storage.Tenant }

func (f fakeNamer) GetTenant(_ context.Context, id uuid.UUID) (storage.Tenant, error) {
	if tn, ok := f.tenants[id]; ok {
		return tn, nil
	}
	return storage.Tenant{}, storage.ErrNotFound
}

// authTestTenantName is the display name newAuthClient's namer serves for the
// stack-resolved tenant.
const authTestTenantName = "Glyphoxa Test Tenant"

// errNamer is a TenantNamer whose every lookup fails with err — the broken-DB
// path behind GetCurrentUser's CodeInternal branch.
type errNamer struct{ err error }

func (e errNamer) GetTenant(context.Context, uuid.UUID) (storage.Tenant, error) {
	return storage.Tenant{}, e.err
}

// newAuthClientWith stands up the AuthService handler behind the real
// interceptor stack and an httptest server, with fully caller-chosen fakes.
// GetCurrentUser is the only public (unauthenticated-reachable) procedure.
func newAuthClientWith(t *testing.T, authn auth.Authenticator, del auth.SessionDeleter, tr auth.TenantResolver, namer auth.TenantNamer) managementv1connect.AuthServiceClient {
	t.Helper()
	stack := auth.NewStack(authn, tr, managementv1connect.AuthServiceGetCurrentUserProcedure)
	server := auth.NewAuthServer(del, namer, slog.Default())
	mux := http.NewServeMux()
	mux.Handle(server.Handler(stack.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return managementv1connect.NewAuthServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())
}

// newAuthClient is [newAuthClientWith] on the happy-path fakes: the stack
// resolves a fresh tenant id whose name the namer serves. Returns that id so
// tests can assert the response's tenant fields.
func newAuthClient(t *testing.T, authn auth.Authenticator, del auth.SessionDeleter) (managementv1connect.AuthServiceClient, uuid.UUID) {
	t.Helper()
	tenantID := uuid.New()
	namer := fakeNamer{tenants: map[uuid.UUID]storage.Tenant{
		tenantID: {ID: tenantID, Name: authTestTenantName},
	}}
	return newAuthClientWith(t, authn, del, fakeTenant{id: tenantID}, namer), tenantID
}

func TestGetCurrentUser_ValidCookie(t *testing.T) {
	t.Parallel()
	op := operator()
	client, tenantID := newAuthClient(t, fakeAuthN{users: map[string]storage.User{validToken: op}}, &fakeDeleter{})

	req := connect.NewRequest(&managementv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.SessionCookieName+"="+validToken)

	resp, err := client.GetCurrentUser(context.Background(), req)
	if err != nil {
		t.Fatalf("GetCurrentUser(valid): %v", err)
	}
	got := resp.Msg.GetUser()
	if got.GetName() != op.Name || got.GetRole() != op.Role || got.GetAvatar() != op.Avatar {
		t.Errorf("user = %+v, want name/role/avatar of %+v", got, op)
	}
	// ADR-0055 growth: the internal user id plus the resolved Tenant id/name.
	if got.GetId() != op.ID.String() {
		t.Errorf("user id = %q, want %s", got.GetId(), op.ID)
	}
	if resp.Msg.GetTenantId() != tenantID.String() {
		t.Errorf("tenant_id = %q, want %s", resp.Msg.GetTenantId(), tenantID)
	}
	if resp.Msg.GetTenantName() != authTestTenantName {
		t.Errorf("tenant_name = %q, want %q", resp.Msg.GetTenantName(), authTestTenantName)
	}
}

func TestGetCurrentUser_MissingCookie_401(t *testing.T) {
	t.Parallel()
	client, _ := newAuthClient(t, fakeAuthN{}, &fakeDeleter{})

	_, err := client.GetCurrentUser(context.Background(),
		connect.NewRequest(&managementv1.GetCurrentUserRequest{}))
	if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want CodeUnauthenticated", got)
	}
}

func TestGetCurrentUser_ExpiredCookie_401(t *testing.T) {
	t.Parallel()
	// An empty user map means every token resolves to ErrNotFound — the
	// expired/unknown path.
	client, _ := newAuthClient(t, fakeAuthN{}, &fakeDeleter{})

	req := connect.NewRequest(&managementv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.SessionCookieName+"=expired-or-unknown")

	_, err := client.GetCurrentUser(context.Background(), req)
	if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want CodeUnauthenticated", got)
	}
}

func TestLogout_Unauthenticated_Rejected(t *testing.T) {
	t.Parallel()
	// Logout is state-changing and NOT public, so the auth interceptor rejects an
	// unauthenticated call before any handler runs.
	del := &fakeDeleter{}
	client, _ := newAuthClient(t, fakeAuthN{}, del)

	_, err := client.Logout(context.Background(), connect.NewRequest(&managementv1.LogoutRequest{}))
	if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want CodeUnauthenticated", got)
	}
	if len(del.deleted) != 0 {
		t.Errorf("session deleted on a rejected logout: %v", del.deleted)
	}
}

func TestLogout_MissingCSRF_Rejected(t *testing.T) {
	t.Parallel()
	op := operator()
	del := &fakeDeleter{}
	client, _ := newAuthClient(t, fakeAuthN{users: map[string]storage.User{validToken: op}}, del)

	// Valid session, but no X-CSRF-Token header → double-submit fails.
	req := connect.NewRequest(&managementv1.LogoutRequest{})
	req.Header().Set("Cookie", auth.SessionCookieName+"="+validToken+"; "+auth.CSRFCookieName+"="+csrfValue)

	_, err := client.Logout(context.Background(), req)
	if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
		t.Fatalf("code = %v, want CodePermissionDenied", got)
	}
	if len(del.deleted) != 0 {
		t.Errorf("session deleted despite CSRF failure: %v", del.deleted)
	}
}

func TestLogout_Authenticated_DeletesAndClears(t *testing.T) {
	t.Parallel()
	op := operator()
	del := &fakeDeleter{}
	client, _ := newAuthClient(t, fakeAuthN{users: map[string]storage.User{validToken: op}}, del)

	req := connect.NewRequest(&managementv1.LogoutRequest{})
	req.Header().Set("Cookie", auth.SessionCookieName+"="+validToken+"; "+auth.CSRFCookieName+"="+csrfValue)
	req.Header().Set("X-CSRF-Token", csrfValue)

	resp, err := client.Logout(context.Background(), req)
	if err != nil {
		t.Fatalf("Logout(valid): %v", err)
	}
	if len(del.deleted) != 1 || del.deleted[0] != validToken {
		t.Fatalf("deleted = %v, want [%s]", del.deleted, validToken)
	}
	// The session cookie is cleared on the response (Max-Age=0 from MaxAge=-1).
	var cleared bool
	for _, sc := range resp.Header().Values("Set-Cookie") {
		if strings.HasPrefix(sc, auth.SessionCookieName+"=") && strings.Contains(sc, "Max-Age=0") {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("session cookie not cleared; Set-Cookie = %v", resp.Header().Values("Set-Cookie"))
	}
}

// TestGetCurrentUser_TenantBranches pins the three non-happy tenant branches
// GetCurrentUser grew (ADR-0055): (a) an operator with no resolvable tenant gets
// empty tenant fields, not an error; (b) a tenant that vanished between the
// interceptor's resolution and the name read (a deletion race) keeps its id but
// serves no name; (c) any other tenant-read failure is CodeInternal.
func TestGetCurrentUser_TenantBranches(t *testing.T) {
	t.Parallel()
	op := operator()
	authn := fakeAuthN{users: map[string]storage.User{validToken: op}}
	authedReq := func() *connect.Request[managementv1.GetCurrentUserRequest] {
		req := connect.NewRequest(&managementv1.GetCurrentUserRequest{})
		req.Header().Set("Cookie", auth.SessionCookieName+"="+validToken)
		return req
	}

	t.Run("no bound tenant -> empty fields", func(t *testing.T) {
		t.Parallel()
		client := newAuthClientWith(t, authn, &fakeDeleter{},
			fakeTenant{err: storage.ErrNotFound}, fakeNamer{})
		resp, err := client.GetCurrentUser(context.Background(), authedReq())
		if err != nil {
			t.Fatalf("GetCurrentUser(tenantless): %v", err)
		}
		if resp.Msg.GetTenantId() != "" || resp.Msg.GetTenantName() != "" {
			t.Errorf("tenant fields = (%q, %q), want empty for a tenantless operator",
				resp.Msg.GetTenantId(), resp.Msg.GetTenantName())
		}
		if resp.Msg.GetUser().GetId() != op.ID.String() {
			t.Errorf("user id = %q, want %s regardless of tenant state", resp.Msg.GetUser().GetId(), op.ID)
		}
	})

	t.Run("tenant deleted mid-request -> id without name", func(t *testing.T) {
		t.Parallel()
		tenantID := uuid.New()
		client := newAuthClientWith(t, authn, &fakeDeleter{},
			fakeTenant{id: tenantID}, fakeNamer{}) // namer knows no tenants -> ErrNotFound
		resp, err := client.GetCurrentUser(context.Background(), authedReq())
		if err != nil {
			t.Fatalf("GetCurrentUser(deletion race): %v", err)
		}
		if resp.Msg.GetTenantId() != tenantID.String() {
			t.Errorf("tenant_id = %q, want %s (kept through the race)", resp.Msg.GetTenantId(), tenantID)
		}
		if resp.Msg.GetTenantName() != "" {
			t.Errorf("tenant_name = %q, want empty when the tenant row is gone", resp.Msg.GetTenantName())
		}
	})

	t.Run("tenant read failure -> CodeInternal", func(t *testing.T) {
		t.Parallel()
		client := newAuthClientWith(t, authn, &fakeDeleter{},
			fakeTenant{id: uuid.New()}, errNamer{err: errors.New("db down")})
		_, err := client.GetCurrentUser(context.Background(), authedReq())
		if got := connect.CodeOf(err); got != connect.CodeInternal {
			t.Fatalf("code = %v, want CodeInternal on a tenant read failure", got)
		}
	})
}
