package auth_test

// The #446 drift regression test: drive the Connect adapter (the interceptor
// stack) and the plain-HTTP adapter (the guarded mount table) with the SAME
// session/CSRF input matrix and assert they reach IDENTICAL decisions. This is
// the test that makes the #408 class visible: there, the two hand-synced
// copies of the gate diverged (a mount composed session-only, no tenant) and
// nothing failed until production.

import (
	"context"
	"errors"
	"fmt"
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

// outcome is the transport-free decision class both adapters must agree on.
type outcome string

const (
	allowed         outcome = "allowed"
	unauthenticated outcome = "unauthenticated" // CodeUnauthenticated / 401
	forbidden       outcome = "forbidden"       // CodePermissionDenied / 403
)

func connectOutcome(err error) outcome {
	if err == nil {
		return allowed
	}
	switch connect.CodeOf(err) {
	case connect.CodeUnauthenticated:
		return unauthenticated
	case connect.CodePermissionDenied:
		return forbidden
	default:
		return outcome(fmt.Sprintf("unexpected connect code %v", connect.CodeOf(err)))
	}
}

func httpOutcome(status int) outcome {
	switch {
	case status >= 200 && status < 300:
		return allowed
	case status == http.StatusUnauthorized:
		return unauthenticated
	case status == http.StatusForbidden:
		return forbidden
	default:
		return outcome(fmt.Sprintf("unexpected http status %d", status))
	}
}

// driftCell is one point of the principal/cookie/CSRF matrix plus the outcome
// the shared policy defines for it. The expectation is stated per cell so a
// bug that shifts BOTH adapters the same way still fails the test.
type driftCell struct {
	name          string
	sessionCookie string // "" = no cookie sent
	csrfCookie    string
	csrfHeader    string
	wantRead      outcome // CSRF-exempt path (NO_SIDE_EFFECTS RPC / GET mount)
	wantWrite     outcome // state-changing path (mutating RPC / POST mount)
}

const driftCSRF = "csrf-double-submit-token"

// denialText pins the exact rejection text per denial class. The strings live
// once in the policy (Denial.Message) and ride both transports verbatim; a
// silent reword would otherwise ship green on every test.
var denialText = map[outcome]string{
	unauthenticated: "please sign in",
	forbidden:       "csrf check failed, retry",
}

// assertDenialText asserts a denied request carried the pinned text on both
// transports: the connect.Error message and the HTTP body (http.Error appends
// a trailing newline).
func assertDenialText(t *testing.T, path string, want outcome, connectErr error, rec *httptest.ResponseRecorder) {
	t.Helper()
	wantText, denied := denialText[want]
	if !denied {
		return
	}
	var cerr *connect.Error
	if !errors.As(connectErr, &cerr) {
		t.Errorf("%s via Connect: want *connect.Error, got %v", path, connectErr)
	} else if cerr.Message() != wantText {
		t.Errorf("%s via Connect: denial text = %q, want %q", path, cerr.Message(), wantText)
	}
	if got := strings.TrimSuffix(rec.Body.String(), "\n"); got != wantText {
		t.Errorf("%s via HTTP mount: denial body = %q, want %q", path, got, wantText)
	}
}

func driftMatrix() []driftCell {
	return []driftCell{
		{name: "no session, no csrf", wantRead: unauthenticated, wantWrite: unauthenticated},
		{name: "no session, valid csrf", csrfCookie: driftCSRF, csrfHeader: driftCSRF,
			wantRead: unauthenticated, wantWrite: unauthenticated},
		{name: "invalid session, valid csrf", sessionCookie: "expired-or-unknown",
			csrfCookie: driftCSRF, csrfHeader: driftCSRF,
			wantRead: unauthenticated, wantWrite: unauthenticated},
		{name: "valid session, matching csrf", sessionCookie: validToken,
			csrfCookie: driftCSRF, csrfHeader: driftCSRF,
			wantRead: allowed, wantWrite: allowed},
		{name: "valid session, mismatched csrf", sessionCookie: validToken,
			csrfCookie: driftCSRF, csrfHeader: "attacker-guess",
			wantRead: allowed, wantWrite: forbidden},
		{name: "valid session, missing csrf header", sessionCookie: validToken,
			csrfCookie: driftCSRF,
			wantRead:   allowed, wantWrite: forbidden},
		{name: "valid session, missing csrf cookie", sessionCookie: validToken,
			csrfHeader: driftCSRF,
			wantRead:   allowed, wantWrite: forbidden},
		{name: "valid session, no csrf at all", sessionCookie: validToken,
			wantRead: allowed, wantWrite: forbidden},
	}
}

// connectAdapter stands up the real AuthService behind the real interceptor
// stack with NO public procedures, so GetCurrentUser behaves as a gated
// NO_SIDE_EFFECTS read and Logout as a gated mutation — the same Check shapes
// the guarded mounts use.
func connectAdapter(t *testing.T, authn auth.Authenticator, tenants auth.TenantResolver) managementv1connect.AuthServiceClient {
	t.Helper()
	stack := auth.NewStack(authn, tenants)
	server := auth.NewAuthServer(&fakeDeleter{}, nil)
	mux := http.NewServeMux()
	mux.Handle(server.Handler(stack.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return managementv1connect.NewAuthServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())
}

// recordingAuthService is an AuthService whose GetCurrentUser records what the
// REAL interceptor stack put into the request context — the Connect-side
// mirror of the guarded mounts' ctxSeen handler. The real AuthServer never
// reads TenantID, so without this the tenant half of the Connect injection
// would go unasserted (an interceptor that stopped resolving tenants — the
// #408 injection drift — would break every tenant-scoped RPC with the suite
// green).
type recordingAuthService struct {
	managementv1connect.UnimplementedAuthServiceHandler
	seen *ctxSeen
}

func (s *recordingAuthService) GetCurrentUser(ctx context.Context, _ *connect.Request[managementv1.GetCurrentUserRequest]) (*connect.Response[managementv1.GetCurrentUserResponse], error) {
	s.seen.user, s.seen.hasUser = auth.CurrentUser(ctx)
	s.seen.tenant, s.seen.hasTenant = auth.TenantID(ctx)
	return connect.NewResponse(&managementv1.GetCurrentUserResponse{}), nil
}

// recordingConnectAdapter mounts recordingAuthService behind the real stack.
func recordingConnectAdapter(t *testing.T, authn auth.Authenticator, tenants auth.TenantResolver) (managementv1connect.AuthServiceClient, *ctxSeen) {
	t.Helper()
	seen := &ctxSeen{}
	stack := auth.NewStack(authn, tenants)
	mux := http.NewServeMux()
	mux.Handle(managementv1connect.NewAuthServiceHandler(&recordingAuthService{seen: seen}, stack.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return managementv1connect.NewAuthServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON()), seen
}

// httpAdapter builds a guarded GET (read) and POST (write) mount off the same
// policy inputs, recording what the inner handler observed in ctx.
type ctxSeen struct {
	user      storage.User
	hasUser   bool
	tenant    uuid.UUID
	hasTenant bool
}

func httpAdapter(t *testing.T, authn auth.Authenticator, tenants auth.TenantResolver, mode auth.TenantMode) (read, write http.Handler, seen *ctxSeen) {
	t.Helper()
	seen = &ctxSeen{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.user, seen.hasUser = auth.CurrentUser(r.Context())
		seen.tenant, seen.hasTenant = auth.TenantID(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	guarded := auth.MustGuardMounts(auth.NewPolicy(authn, tenants), []auth.GuardedMount{
		{Pattern: "GET /read", Tenant: mode, Handler: inner},
		{Pattern: "POST /write", Tenant: mode, Handler: inner},
	})
	return guarded[0].Handler, guarded[1].Handler, seen
}

func applyCell(h http.Header, c driftCell) {
	cookies := ""
	if c.sessionCookie != "" {
		cookies = auth.SessionCookieName + "=" + c.sessionCookie
	}
	if c.csrfCookie != "" {
		if cookies != "" {
			cookies += "; "
		}
		cookies += auth.CSRFCookieName + "=" + c.csrfCookie
	}
	if cookies != "" {
		h.Set("Cookie", cookies)
	}
	if c.csrfHeader != "" {
		h.Set("X-CSRF-Token", c.csrfHeader)
	}
}

// TestAdapterParity_NoDrift is the #446 drift regression: for every cell of
// the session×CSRF matrix, the Connect interceptor and the guarded plain
// mount must produce the same decision class on both the read (CSRF-exempt)
// and write (CSRF-gated) paths — and that class must be the one the policy
// defines. Both sides run the tenant resolver successfully, isolating the
// session/CSRF gate that drifted in #408.
func TestAdapterParity_NoDrift(t *testing.T) {
	t.Parallel()
	op := operator()
	tenantID := uuid.New()

	for _, cell := range driftMatrix() {
		t.Run(cell.name, func(t *testing.T) {
			authn := fakeAuthN{users: map[string]storage.User{validToken: op}}
			tenants := fakeTenant{id: tenantID}
			client := connectAdapter(t, authn, tenants)
			// The Connect stack's tenant mode is TenantOptional; declare the
			// mounts the same so the whole Check matches, not just parts.
			readMount, writeMount, _ := httpAdapter(t, authn, tenants, auth.TenantOptional)

			// Read path: GetCurrentUser (NO_SIDE_EFFECTS) vs the GET mount.
			readReq := connect.NewRequest(&managementv1.GetCurrentUserRequest{})
			applyCell(readReq.Header(), cell)
			_, readErr := client.GetCurrentUser(context.Background(), readReq)

			httpRead := httptest.NewRequest(http.MethodGet, "/read", nil)
			applyCell(httpRead.Header, cell)
			readRec := httptest.NewRecorder()
			readMount.ServeHTTP(readRec, httpRead)

			if got, want := connectOutcome(readErr), cell.wantRead; got != want {
				t.Errorf("read via Connect = %s, want %s (err=%v)", got, want, readErr)
			}
			if got, want := httpOutcome(readRec.Code), cell.wantRead; got != want {
				t.Errorf("read via HTTP mount = %s, want %s (status=%d)", got, want, readRec.Code)
			}
			if connectOutcome(readErr) != httpOutcome(readRec.Code) {
				t.Errorf("READ DRIFT: Connect=%s HTTP=%s for the same inputs",
					connectOutcome(readErr), httpOutcome(readRec.Code))
			}
			assertDenialText(t, "read", cell.wantRead, readErr, readRec)

			// Write path: Logout (mutating) vs the POST mount.
			writeReq := connect.NewRequest(&managementv1.LogoutRequest{})
			applyCell(writeReq.Header(), cell)
			_, writeErr := client.Logout(context.Background(), writeReq)

			httpWrite := httptest.NewRequest(http.MethodPost, "/write", nil)
			applyCell(httpWrite.Header, cell)
			writeRec := httptest.NewRecorder()
			writeMount.ServeHTTP(writeRec, httpWrite)

			if got, want := connectOutcome(writeErr), cell.wantWrite; got != want {
				t.Errorf("write via Connect = %s, want %s (err=%v)", got, want, writeErr)
			}
			if got, want := httpOutcome(writeRec.Code), cell.wantWrite; got != want {
				t.Errorf("write via HTTP mount = %s, want %s (status=%d)", got, want, writeRec.Code)
			}
			if connectOutcome(writeErr) != httpOutcome(writeRec.Code) {
				t.Errorf("WRITE DRIFT: Connect=%s HTTP=%s for the same inputs",
					connectOutcome(writeErr), httpOutcome(writeRec.Code))
			}
			assertDenialText(t, "write", cell.wantWrite, writeErr, writeRec)
		})
	}
}

// TestAdapterParity_PrincipalInjection proves both adapters inject the SAME
// resolved principal AND tenant into the request context — the injection half
// of the gate, which #408 showed can drift independently of the reject half
// (there, one transport's handlers saw a TenantID the other's never got).
func TestAdapterParity_PrincipalInjection(t *testing.T) {
	t.Parallel()
	op := operator()
	tenantID := uuid.New()
	authn := fakeAuthN{users: map[string]storage.User{validToken: op}}
	tenants := fakeTenant{id: tenantID}

	// Connect side: a recording handler behind the REAL stack observes the
	// injected operator and tenant.
	client, connectSeen := recordingConnectAdapter(t, authn, tenants)
	req := connect.NewRequest(&managementv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.SessionCookieName+"="+validToken)
	if _, err := client.GetCurrentUser(context.Background(), req); err != nil {
		t.Fatalf("GetCurrentUser: %v", err)
	}
	if !connectSeen.hasUser || connectSeen.user.ID != op.ID {
		t.Errorf("Connect-injected principal = %+v (hasUser=%t), want %s",
			connectSeen.user, connectSeen.hasUser, op.ID)
	}
	if !connectSeen.hasTenant || connectSeen.tenant != tenantID {
		t.Errorf("Connect-injected tenant = %s (hasTenant=%t), want %s",
			connectSeen.tenant, connectSeen.hasTenant, tenantID)
	}

	// HTTP side: the guarded mount injects the same operator and tenant.
	readMount, _, seen := httpAdapter(t, authn, tenants, auth.TenantRequired)
	httpReq := httptest.NewRequest(http.MethodGet, "/read", nil)
	httpReq.Header.Set("Cookie", auth.SessionCookieName+"="+validToken)
	rec := httptest.NewRecorder()
	readMount.ServeHTTP(rec, httpReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("guarded mount status = %d, want 200", rec.Code)
	}
	if !seen.hasUser || seen.user.ID != op.ID {
		t.Errorf("HTTP-injected principal = %+v (hasUser=%t), want %s", seen.user, seen.hasUser, op.ID)
	}
	if !seen.hasTenant || seen.tenant != tenantID {
		t.Errorf("HTTP-injected tenant = %s (hasTenant=%t), want %s", seen.tenant, seen.hasTenant, tenantID)
	}

	// The two transports observed the SAME injection.
	if connectSeen.user.ID != seen.user.ID || connectSeen.tenant != seen.tenant {
		t.Errorf("INJECTION DRIFT: Connect saw (user=%s tenant=%s), HTTP saw (user=%s tenant=%s)",
			connectSeen.user.ID, connectSeen.tenant, seen.user.ID, seen.tenant)
	}
}

// TestAdapterTenantPosture documents the ONE deliberate divergence between
// the transports, now expressed as declared data instead of hand-synced code:
// the Connect stack runs TenantOptional (tenant-agnostic procedures proceed
// on a resolve failure), while the byte mounts declare TenantRequired (their
// handlers always need a tenant, so an unresolvable one fails fast with 401).
func TestAdapterTenantPosture(t *testing.T) {
	t.Parallel()
	op := operator()
	authn := fakeAuthN{users: map[string]storage.User{validToken: op}}
	broken := fakeTenant{err: storage.ErrNotFound}

	// Connect / TenantOptional: the call proceeds — authenticated but
	// tenantless, observed through the real stack.
	client, connectSeen := recordingConnectAdapter(t, authn, broken)
	req := connect.NewRequest(&managementv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.SessionCookieName+"="+validToken)
	if _, err := client.GetCurrentUser(context.Background(), req); err != nil {
		t.Errorf("TenantOptional (Connect): resolve failure must proceed tenantless, got %v", err)
	}
	if !connectSeen.hasUser || connectSeen.hasTenant {
		t.Errorf("TenantOptional (Connect): want principal without tenant, got hasUser=%t hasTenant=%t",
			connectSeen.hasUser, connectSeen.hasTenant)
	}

	// Guarded mount / TenantRequired: the same failure rejects 401 before the
	// handler runs.
	readMount, _, seen := httpAdapter(t, authn, broken, auth.TenantRequired)
	httpReq := httptest.NewRequest(http.MethodGet, "/read", nil)
	httpReq.Header.Set("Cookie", auth.SessionCookieName+"="+validToken)
	rec := httptest.NewRecorder()
	readMount.ServeHTTP(rec, httpReq)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("TenantRequired (mount): resolve failure status = %d, want 401", rec.Code)
	}
	if seen.hasUser || seen.hasTenant {
		t.Error("TenantRequired (mount): handler ran despite an unresolvable tenant")
	}

	// Guarded mount / TenantOptional mirrors the Connect posture exactly.
	optMount, _, optSeen := httpAdapter(t, authn, broken, auth.TenantOptional)
	rec = httptest.NewRecorder()
	optReq := httptest.NewRequest(http.MethodGet, "/read", nil)
	optReq.Header.Set("Cookie", auth.SessionCookieName+"="+validToken)
	optMount.ServeHTTP(rec, optReq)
	if rec.Code != http.StatusOK {
		t.Errorf("TenantOptional (mount): resolve failure must proceed tenantless, got %d", rec.Code)
	}
	if !optSeen.hasUser || optSeen.hasTenant {
		t.Errorf("TenantOptional (mount): want principal without tenant, got hasUser=%t hasTenant=%t",
			optSeen.hasUser, optSeen.hasTenant)
	}
}
