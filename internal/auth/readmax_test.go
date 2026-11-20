package auth_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

const echoProc = "/glyphoxa.test.Echo/Echo"

// echoClient mounts a one-procedure Connect handler with opts (optionally
// behind wrap) and returns a client for it.
func echoClient(t *testing.T, opts []connect.HandlerOption, wrap func(http.Handler) http.Handler) (*connect.Client[managementv1.CreateMapRequest, managementv1.CreateMapResponse], *httptest.Server) {
	t.Helper()
	var h http.Handler = connect.NewUnaryHandler(echoProc,
		func(_ context.Context, _ *connect.Request[managementv1.CreateMapRequest]) (*connect.Response[managementv1.CreateMapResponse], error) {
			return connect.NewResponse(&managementv1.CreateMapResponse{}), nil
		}, opts...)
	if wrap != nil {
		h = wrap(h)
	}
	mux := http.NewServeMux()
	mux.Handle(echoProc, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return connect.NewClient[managementv1.CreateMapRequest, managementv1.CreateMapResponse](srv.Client(), srv.URL+echoProc), srv
}

// TestHandlerOptions_BoundRequestBodies pins the per-service read caps (R2).
// connect-go buffers the whole request message BEFORE any interceptor runs, so
// the cap is what an unauthenticated caller can make the server hold per
// request. The default every service inherits is MaxRequestBytes (1 MiB); only
// the upload options carry the 48 MiB image cap. Over a cap the message is
// refused with CodeResourceExhausted before any handler sees it. A zero Stack
// carries no interceptors, so the test isolates the caps themselves.
func TestHandlerOptions_BoundRequestBodies(t *testing.T) {
	t.Parallel()
	if auth.MaxRequestBytes != 1<<20 {
		t.Fatalf("MaxRequestBytes = %d, want 1 MiB — the pre-auth buffering bound on every non-upload service", auth.MaxRequestBytes)
	}

	t.Run("default cap", func(t *testing.T) {
		t.Parallel()
		client, _ := echoClient(t, (&auth.Stack{}).HandlerOptions(), nil)
		if _, err := client.CallUnary(context.Background(), connect.NewRequest(&managementv1.CreateMapRequest{
			ImageBytes: make([]byte, 512<<10),
		})); err != nil {
			t.Fatalf("512 KiB message: %v, want success", err)
		}
		_, err := client.CallUnary(context.Background(), connect.NewRequest(&managementv1.CreateMapRequest{
			ImageBytes: make([]byte, auth.MaxRequestBytes+1),
		}))
		if connect.CodeOf(err) != connect.CodeResourceExhausted {
			t.Fatalf("message over the default cap: err = %v, want ResourceExhausted", err)
		}
	})

	t.Run("upload cap", func(t *testing.T) {
		t.Parallel()
		client, _ := echoClient(t, (&auth.Stack{}).UploadHandlerOptions(), nil)
		if _, err := client.CallUnary(context.Background(), connect.NewRequest(&managementv1.CreateMapRequest{
			ImageBytes: make([]byte, 4<<20),
		})); err != nil {
			t.Fatalf("4 MiB upload: %v, want success", err)
		}
		_, err := client.CallUnary(context.Background(), connect.NewRequest(&managementv1.CreateMapRequest{
			ImageBytes: make([]byte, auth.MaxUploadRequestBytes+1),
		}))
		if connect.CodeOf(err) != connect.CodeResourceExhausted {
			t.Fatalf("oversized upload: err = %v, want ResourceExhausted", err)
		}
	})
}

// countingAuthN counts authenticator round trips.
type countingAuthN struct {
	inner auth.Authenticator
	calls atomic.Int64
}

func (c *countingAuthN) AuthenticateSession(ctx context.Context, token string) (storage.User, error) {
	c.calls.Add(1)
	return c.inner.AuthenticateSession(ctx, token)
}

// readSpy records whether anything read the request body.
type readSpy struct {
	io.Reader
	read atomic.Bool
}

func (r *readSpy) Read(p []byte) (int, error) {
	r.read.Store(true)
	return r.Reader.Read(p)
}

// TestRequireSessionBeforeBody_RejectsWithoutReadingBody is the R2 regression
// for the large-cap mount: a caller with no (or a dead) session must be turned
// away on the headers alone, with not one body byte read — otherwise the
// 48 MiB upload cap is an unauthenticated buffering lever. The denial is a
// well-formed Connect error with the code the interceptor would have used.
func TestRequireSessionBeforeBody_RejectsWithoutReadingBody(t *testing.T) {
	t.Parallel()
	op := operator()
	for _, tc := range []struct {
		name     string
		authn    auth.Authenticator
		cookie   string
		wantCode connect.Code
		wantHTTP int
	}{
		{"no cookie", fakeAuthN{users: map[string]storage.User{validToken: op}}, "", connect.CodeUnauthenticated, http.StatusUnauthorized},
		{"dead session", fakeAuthN{users: map[string]storage.User{validToken: op}}, "expired-token", connect.CodeUnauthenticated, http.StatusUnauthorized},
		{"session store down", erringAuthN{err: errors.New("connection refused")}, validToken, connect.CodeUnavailable, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stack := auth.NewStack(tc.authn, fakeTenant{id: uuid.New()})
			handlerRan := false
			h := stack.RequireSessionBeforeBody(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handlerRan = true }))

			body := &readSpy{Reader: bytes.NewReader(make([]byte, 1<<20))}
			req := httptest.NewRequest(http.MethodPost, echoProc, body)
			req.Header.Set("Content-Type", "application/json")
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tc.cookie})
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if handlerRan {
				t.Error("the Connect handler ran for a request with no valid session")
			}
			if body.read.Load() {
				t.Error("the request body was read before the session was rejected")
			}
			if rec.Code != tc.wantHTTP {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantHTTP)
			}
			if !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"`+tc.wantCode.String()+`"`)) {
				t.Errorf("body = %s, want a Connect error with code %q", rec.Body.String(), tc.wantCode)
			}
		})
	}
}

// TestRequireSessionBeforeBody_ValidSessionAuthenticatesOnce proves the
// pre-body check is not a second database round trip per request: the
// principal it resolves rides the context, the policy interceptor reuses it,
// and the call still passes the full gate (CSRF, tenant) end to end.
func TestRequireSessionBeforeBody_ValidSessionAuthenticatesOnce(t *testing.T) {
	t.Parallel()
	op := operator()
	authn := &countingAuthN{inner: fakeAuthN{users: map[string]storage.User{validToken: op}}}
	stack := auth.NewStack(authn, fakeTenant{id: uuid.New()})
	client, _ := echoClient(t, stack.UploadHandlerOptions(), stack.RequireSessionBeforeBody)

	req := connect.NewRequest(&managementv1.CreateMapRequest{ImageBytes: make([]byte, 2<<20)})
	req.Header().Set("Cookie", auth.SessionCookieName+"="+validToken+"; "+auth.CSRFCookieName+"="+csrfValue)
	req.Header().Set("X-CSRF-Token", csrfValue)
	if _, err := client.CallUnary(context.Background(), req); err != nil {
		t.Fatalf("authenticated 2 MiB upload: %v, want success", err)
	}
	if got := authn.calls.Load(); got != 1 {
		t.Errorf("AuthenticateSession ran %d times for one request, want 1", got)
	}

	// The CSRF gate still runs behind the pre-body check.
	noCSRF := connect.NewRequest(&managementv1.CreateMapRequest{})
	noCSRF.Header().Set("Cookie", auth.SessionCookieName+"="+validToken)
	if _, err := client.CallUnary(context.Background(), noCSRF); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("mutation without the CSRF pair: err = %v, want PermissionDenied", err)
	}
}

// TestSecureCookies pins the boot-time derivation of the cookie Secure
// attribute from the configured OAuth redirect URL (#662).
func TestSecureCookies(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]bool{
		"https://glyphoxa.example/auth/discord/callback": true,
		"HTTPS://glyphoxa.example/auth/discord/callback": true,
		" https://glyphoxa.example/cb ":                  true,
		"http://localhost:8080/auth/discord/callback":    false,
		"":            false,
		"not a url":   false,
		"//no-scheme": false,
	} {
		if got := auth.SecureCookies(in); got != want {
			t.Errorf("SecureCookies(%q) = %v, want %v", in, got, want)
		}
	}
}
