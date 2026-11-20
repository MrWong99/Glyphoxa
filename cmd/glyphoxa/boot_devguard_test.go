package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// devURL is how a developer's browser addresses a dev-mode instance: the
// loopback bind [forceLoopback] forces. httptest.NewRequest with a bare path
// would say `Host: example.com`, which dev mode now refuses (R13).
const devURL = "http://127.0.0.1:8080"

func devMiddlewareUnderTest(reached *bool) http.Handler {
	inner := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { *reached = true })
	d := &devSessions{
		store: &fakeOAuthStore{}, authn: anyTokenAuth{}, now: time.Now,
		session: "sess-tok", csrf: "csrf-tok",
	}
	return devAuthMiddleware(d, slog.New(slog.DiscardHandler), inner)
}

// TestDevAuthMiddleware_RequiresLoopbackHost is the DNS-rebinding half of R13.
// Dev mode auto-authenticates every request and overwrites the cookies, so the
// loopback bind is its only perimeter — and a browser is inside it: a page on
// evil.example whose name re-resolves to 127.0.0.1 is same-origin with the dev
// instance and could drive the whole operator API. Its requests still carry
// `Host: evil.example`, which is what the middleware must refuse.
func TestDevAuthMiddleware_RequiresLoopbackHost(t *testing.T) {
	for host, allowed := range map[string]bool{
		"127.0.0.1:8080":          true,
		"127.0.0.1":               true,
		"localhost:8080":          true,
		"LOCALHOST:8080":          true,
		"glyphoxa.localhost:8080": true,
		"[::1]:8080":              true,
		"127.8.9.10:8080":         true,

		"evil.example:8080":           false,
		"evil.example":                false,
		"localhost.evil.example:8080": false,
		"127.0.0.1.evil.example:8080": false,
		"192.168.178.50:8080":         false,
		"0.0.0.0:8080":                false,
		"":                            false,
	} {
		reached := false
		h := devMiddlewareUnderTest(&reached)
		req := httptest.NewRequest(http.MethodGet, devURL+"/api/anything", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if allowed && (!reached || rec.Code != http.StatusOK) {
			t.Errorf("Host %q: status=%d reached=%v, want the request served", host, rec.Code, reached)
		}
		if !allowed && (reached || rec.Code != http.StatusForbidden) {
			t.Errorf("Host %q: status=%d reached=%v, want 403 before the auto-auth handler", host, rec.Code, reached)
		}
	}
}

// TestDevAuthMiddleware_RefusesCrossOriginRequests is the CSRF half of R13.
// The middleware supplies the CSRF header itself, so the double-submit check
// downstream can never fail in dev mode: any page the developer has open could
// POST to http://127.0.0.1:8080 (the campaign import, a delete) and have it
// land authenticated. Browsers attach Origin to every such request, so a
// non-loopback Origin is refused. A loopback Origin — the Vite dev server
// proxying /api — and an Origin-less request (curl, a same-origin GET) pass.
func TestDevAuthMiddleware_RefusesCrossOriginRequests(t *testing.T) {
	for origin, allowed := range map[string]bool{
		"":                      true,
		"http://127.0.0.1:8080": true,
		"http://localhost:5173": true, // vite dev server, proxying to :8080
		"http://[::1]:5173":     true,

		"https://evil.example":                 false,
		"http://evil.example:8080":             false,
		"http://localhost.evil.example":        false,
		"null":                                 false, // sandboxed frame, file:// page
		"http://192.168.178.50:8080":           false,
		"chrome-extension://abcdefghijklmnop":  false,
		"not a url":                            false,
		"http://127.0.0.1.evil.example:8080/x": false,
	} {
		for _, method := range []string{http.MethodPost, http.MethodGet} {
			reached := false
			h := devMiddlewareUnderTest(&reached)
			req := httptest.NewRequest(method, devURL+"/api/v1/campaigns/import", nil)
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if allowed && (!reached || rec.Code != http.StatusOK) {
				t.Errorf("%s Origin %q: status=%d reached=%v, want the request served", method, origin, rec.Code, reached)
			}
			if !allowed && (reached || rec.Code != http.StatusForbidden) {
				t.Errorf("%s Origin %q: status=%d reached=%v, want 403 before the auto-auth handler", method, origin, rec.Code, reached)
			}
		}
	}
}

// fakePurger scripts PurgeExpiredSessions and signals each call.
type fakePurger struct {
	calls   atomic.Int64
	results []error // per call; past the end is success
	called  chan struct{}
}

func (f *fakePurger) PurgeExpiredSessions(context.Context) (int64, error) {
	n := int(f.calls.Add(1))
	var err error
	if n <= len(f.results) {
		err = f.results[n-1]
	}
	f.called <- struct{}{}
	return 3, err
}

// TestPurgeExpiredSessions_RunsAtBootAndOnTheCadence pins the #643 schedule:
// one purge immediately (the boot pass — an instance restarted daily must
// still purge), then one per interval; a failed pass does not end the loop;
// and cancelling the context stops it.
func TestPurgeExpiredSessions_RunsAtBootAndOnTheCadence(t *testing.T) {
	purger := &fakePurger{called: make(chan struct{}, 16), results: []error{nil, errors.New("connection refused")}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		purgeExpiredSessions(ctx, purger, 20*time.Millisecond, slog.New(slog.DiscardHandler))
	}()

	// The boot pass, the failing second pass, and a third proving the failure
	// did not stop the loop.
	for i := 1; i <= 3; i++ {
		select {
		case <-purger.called:
		case <-time.After(5 * time.Second):
			t.Fatalf("purge #%d never ran", i)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the purge loop did not stop when its context was cancelled")
	}
}
