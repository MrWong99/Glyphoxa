package auth_test

import (
	"context"
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

// newAuthClient stands up the AuthService handler behind the real interceptor
// stack and an httptest server, with the given fakes. GetCurrentUser is the only
// public (unauthenticated-reachable) procedure.
func newAuthClient(t *testing.T, authn auth.Authenticator, del auth.SessionDeleter) managementv1connect.AuthServiceClient {
	t.Helper()
	stack := auth.NewStack(authn, fakeTenant{id: uuid.New()},
		managementv1connect.AuthServiceGetCurrentUserProcedure)
	server := auth.NewAuthServer(del, slog.Default())
	mux := http.NewServeMux()
	mux.Handle(server.Handler(stack.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return managementv1connect.NewAuthServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())
}

func TestGetCurrentUser_ValidCookie(t *testing.T) {
	t.Parallel()
	op := operator()
	client := newAuthClient(t, fakeAuthN{users: map[string]storage.User{validToken: op}}, &fakeDeleter{})

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
}

func TestGetCurrentUser_MissingCookie_401(t *testing.T) {
	t.Parallel()
	client := newAuthClient(t, fakeAuthN{}, &fakeDeleter{})

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
	client := newAuthClient(t, fakeAuthN{}, &fakeDeleter{})

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
	client := newAuthClient(t, fakeAuthN{}, del)

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
	client := newAuthClient(t, fakeAuthN{users: map[string]storage.User{validToken: op}}, del)

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
	client := newAuthClient(t, fakeAuthN{users: map[string]storage.User{validToken: op}}, del)

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
