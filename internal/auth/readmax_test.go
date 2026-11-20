package auth_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

const echoProc = "/glyphoxa.test.Echo/Echo"

// echoHandler is a one-procedure Connect service in the shape of a generated
// service's Handler method, so it can be handed to Stack.UploadHandler.
func echoHandler(opts ...connect.HandlerOption) (string, http.Handler) {
	return echoProc, connect.NewUnaryHandler(echoProc,
		func(_ context.Context, _ *connect.Request[managementv1.CreateMapRequest]) (*connect.Response[managementv1.CreateMapResponse], error) {
			return connect.NewResponse(&managementv1.CreateMapResponse{}), nil
		}, opts...)
}

// echoClient serves h at the echo procedure and returns a client for it.
func echoClient(t *testing.T, h http.Handler) *connect.Client[managementv1.CreateMapRequest, managementv1.CreateMapResponse] {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(echoProc, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return connect.NewClient[managementv1.CreateMapRequest, managementv1.CreateMapResponse](srv.Client(), srv.URL+echoProc)
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
		_, h := (&auth.Stack{}).Handler(echoHandler)
		client := echoClient(t, h)
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
		// The large cap exists only behind the pre-body session check, so the
		// upload is made by a signed-in caller.
		stack := auth.NewPolicy(fakeAuthN{users: map[string]storage.User{validToken: operator()}}, fakeTenant{id: uuid.New()}).Stack()
		_, h := stack.UploadHandler(echoHandler)
		client := echoClient(t, h)
		signedIn := func(size int) *connect.Request[managementv1.CreateMapRequest] {
			req := connect.NewRequest(&managementv1.CreateMapRequest{ImageBytes: make([]byte, size)})
			req.Header().Set("Cookie", auth.SessionCookieName+"="+validToken+"; "+auth.CSRFCookieName+"="+csrfValue)
			req.Header().Set("X-CSRF-Token", csrfValue)
			return req
		}
		if _, err := client.CallUnary(context.Background(), signedIn(4<<20)); err != nil {
			t.Fatalf("4 MiB upload: %v, want success", err)
		}
		_, err := client.CallUnary(context.Background(), signedIn(auth.MaxUploadRequestBytes+1))
		if connect.CodeOf(err) != connect.CodeResourceExhausted {
			t.Fatalf("oversized upload: err = %v, want ResourceExhausted", err)
		}
	})
}

// TestHandler_StopsReadingAnOverCapBody: connect's read cap bounds what it
// BUFFERS, but past the cap it drains the remainder of the body to count it —
// so the cap alone still lets an unauthenticated caller push an arbitrarily
// long body through the server. Stack.Handler must end the read at the cap,
// while the answer stays CodeResourceExhausted.
func TestHandler_StopsReadingAnOverCapBody(t *testing.T) {
	t.Parallel()
	_, h := (&auth.Stack{}).Handler(echoHandler)

	body := &readCounter{Reader: strings.NewReader(`{"name":"` + strings.Repeat("x", 8<<20) + `"}`)}
	req := httptest.NewRequest(http.MethodPost, echoProc, body)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1 // chunked: nothing for the server to refuse up front
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"resource_exhausted"`)) {
		t.Errorf("status=%d body=%s, want a resource_exhausted Connect error", rec.Code, rec.Body.String())
	}
	if got, max := body.n.Load(), int64(auth.MaxRequestBytes+64<<10); got > max {
		t.Errorf("read %d bytes of an 8 MiB over-cap body, want at most %d", got, max)
	}
}

// readCounter counts the bytes read through it.
type readCounter struct {
	io.Reader
	n atomic.Int64
}

func (r *readCounter) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.n.Add(int64(n))
	return n, err
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

// TestUploadHandler_RejectsWithoutReadingBody is the R2 regression
// for the large-cap mount: a caller with no (or a dead) session must be turned
// away on the headers alone, with not one body byte read — otherwise the
// 48 MiB upload cap is an unauthenticated buffering lever. The denial is a
// well-formed Connect error with the code the interceptor would have used.
func TestUploadHandler_RejectsWithoutReadingBody(t *testing.T) {
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
			stack := auth.NewPolicy(tc.authn, fakeTenant{id: uuid.New()}).Stack()
			handlerRan := false
			_, h := stack.UploadHandler(func(...connect.HandlerOption) (string, http.Handler) {
				return echoProc, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handlerRan = true })
			})

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
			// The refused body is abandoned, not drained: without this net/http
			// waits for the unsent megabyte after the handler returns.
			if got := rec.Header().Get("Connection"); got != "close" {
				t.Errorf("Connection = %q, want close on a refused upload", got)
			}
			if !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"`+tc.wantCode.String()+`"`)) {
				t.Errorf("body = %s, want a Connect error with code %q", rec.Body.String(), tc.wantCode)
			}
		})
	}
}

// TestUploadHandler_ValidSessionAuthenticatesOnce proves the
// pre-body check is not a second database round trip per request: the
// principal it resolves rides the context, the policy interceptor reuses it,
// and the call still passes the full gate (CSRF, tenant) end to end.
func TestUploadHandler_ValidSessionAuthenticatesOnce(t *testing.T) {
	t.Parallel()
	op := operator()
	authn := &countingAuthN{inner: fakeAuthN{users: map[string]storage.User{validToken: op}}}
	stack := auth.NewPolicy(authn, fakeTenant{id: uuid.New()}).Stack()
	_, h := stack.UploadHandler(echoHandler)
	client := echoClient(t, h)

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

// shortBodyDeadline stands in for the web listener: it arms a short default
// body read deadline on every request, as web.NewServer does for the whole mux.
func shortBodyDeadline(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(d))
		next.ServeHTTP(w, r)
	})
}

// slowUpload sends a request head declaring a 4-byte body, half the body, waits
// pause, then the rest, and returns the response status line ("" if the server
// hung up without answering). cookie is sent verbatim when non-empty.
func slowUpload(t *testing.T, srv *httptest.Server, path, cookie string, pause time.Duration) (status string, elapsed time.Duration) {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	head := "POST " + path + " HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 4\r\n"
	if cookie != "" {
		head += "Cookie: " + cookie + "\r\nX-CSRF-Token: " + csrfValue + "\r\n"
	}
	start := time.Now()
	if _, err := io.WriteString(conn, head+"\r\n{ "); err != nil {
		t.Fatalf("write head: %v", err)
	}
	// Collect whatever the server says while we stall.
	type reply struct {
		line string
		at   time.Duration
	}
	got := make(chan reply, 1)
	go func() {
		line, _ := bufio.NewReader(conn).ReadString('\n')
		got <- reply{strings.TrimSpace(line), time.Since(start)}
	}()
	select {
	case r := <-got:
		return r.line, r.at // answered (or hung up) without waiting for the body
	case <-time.After(pause):
	}
	_, _ = io.WriteString(conn, " }")
	select {
	case r := <-got:
		return r.line, r.at
	case <-time.After(5 * time.Second):
		t.Fatal("no response to a completed upload after 5s")
		return "", 0
	}
}

// TestUploadRoutes_LongDeadlineOnlyAfterAuth is the slow-body regression for
// the two upload routes. An earlier version of this fix armed the 30-minute
// upload deadline on the mount itself — BEFORE the session check — so a caller
// with no session could declare a body, send nothing, and hold a connection and
// a goroutine for half an hour. The long deadline must be armed only once auth
// has passed: a sessionless caller is answered immediately (and the connection
// closed, not drained), while a signed-in caller whose body arrives after the
// listener's short default would have fired still completes.
func TestUploadRoutes_LongDeadlineOnlyAfterAuth(t *testing.T) {
	t.Parallel()
	const listenerDefault = 150 * time.Millisecond
	policy := auth.NewPolicy(fakeAuthN{users: map[string]storage.User{validToken: operator()}}, fakeTenant{id: uuid.New()})

	_, upload := policy.Stack().UploadHandler(echoHandler)
	importHit := make(chan string, 4)
	guarded := auth.MustGuardMounts(policy, []auth.GuardedMount{{
		Pattern:         "POST /import",
		Tenant:          auth.TenantNone,
		BodyReadTimeout: auth.UploadBodyReadTimeout,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				importHit <- "read error: " + err.Error()
				http.Error(w, "read", http.StatusRequestTimeout)
				return
			}
			importHit <- string(body)
		}),
	}})

	mux := http.NewServeMux()
	mux.Handle(echoProc, upload)
	mux.Handle(guarded[0].Pattern, guarded[0].Handler)
	srv := httptest.NewServer(shortBodyDeadline(listenerDefault, mux))
	t.Cleanup(srv.Close)

	signedIn := auth.SessionCookieName + "=" + validToken + "; " + auth.CSRFCookieName + "=" + csrfValue
	for name, path := range map[string]string{"Connect upload service": echoProc, "guarded import mount": "/import"} {
		// No session: refused at once, without waiting out any body deadline.
		status, elapsed := slowUpload(t, srv, path, "", 2*time.Second)
		if !strings.Contains(status, "401") {
			t.Errorf("%s, no session: response %q, want an immediate 401", name, status)
		}
		if elapsed > time.Second {
			t.Errorf("%s, no session: answered after %v — the server waited on a body it was never going to accept", name, elapsed)
		}

		// Signed in, body arriving well after the listener default: completes.
		status, _ = slowUpload(t, srv, path, signedIn, 4*listenerDefault)
		if !strings.Contains(status, "200") {
			t.Errorf("%s, signed in, slow body: response %q, want 200 — the post-auth deadline did not replace the listener default", name, status)
		}
	}
	select {
	case body := <-importHit:
		if body != "{  }" {
			t.Errorf("import handler read %q, want the whole slow body", body)
		}
	default:
		t.Error("the import handler never ran for the signed-in upload")
	}
}

// TestGuardedMount_DefaultDeadlineUnlessDeclared: a guarded mount that does not
// declare BodyReadTimeout keeps the listener's short default even for a
// signed-in caller — the long deadline is opt-in per row.
func TestGuardedMount_DefaultDeadlineUnlessDeclared(t *testing.T) {
	t.Parallel()
	policy := auth.NewPolicy(fakeAuthN{users: map[string]storage.User{validToken: operator()}}, fakeTenant{id: uuid.New()})
	readErr := make(chan error, 1)
	guarded := auth.MustGuardMounts(policy, []auth.GuardedMount{{
		Pattern: "POST /small",
		Tenant:  auth.TenantNone,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			readErr <- err
			http.Error(w, "late", http.StatusRequestTimeout)
		}),
	}})
	mux := http.NewServeMux()
	mux.Handle(guarded[0].Pattern, guarded[0].Handler)
	srv := httptest.NewServer(shortBodyDeadline(100*time.Millisecond, mux))
	t.Cleanup(srv.Close)

	signedIn := auth.SessionCookieName + "=" + validToken + "; " + auth.CSRFCookieName + "=" + csrfValue
	slowUpload(t, srv, "/small", signedIn, 2*time.Second)
	select {
	case err := <-readErr:
		if err == nil {
			t.Error("a stalled body was read to the end on a mount that declared no upload deadline")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler still blocked on a stalled body")
	}
}
