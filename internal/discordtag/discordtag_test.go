package discordtag_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/discordtag"
)

// TestResolve_EmptyToken_FailsFastNoNetwork pins the guard path: an empty token
// is rejected before any REST call, so the default `go test` makes no live
// Discord call (ADR-0021). The call against the real API is exercised by an
// operator run, behind the RPC layer's seam.
func TestResolve_EmptyToken_FailsFastNoNetwork(t *testing.T) {
	t.Parallel()
	_, err := discordtag.Resolve(context.Background(), "", nil)
	if err == nil {
		t.Fatal("Resolve with empty token returned nil error")
	}
	if !strings.Contains(err.Error(), "empty bot token") {
		t.Errorf("error %q does not mention the empty token", err)
	}
}

// TestResolve_RESTSelfUserNoGateway pins the #150 fix: the resolver proves the
// token via a plain REST `GET /users/@me` (Bot auth) — no gateway dial, no
// IDENTIFY, no session-start budget consumed. The fake HTTP server IS the whole
// Discord surface the resolver may touch; a gateway login would try to dial a
// websocket and fail this offline test.
func TestResolve_RESTSelfUserNoGateway(t *testing.T) {
	t.Parallel()

	var (
		mu   sync.Mutex
		reqs []string // "METHOD path AuthorizationHeader"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"123","username":"Glyphoxa","discriminator":"4823"}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tag, err := discordtag.ResolveAt(ctx, "tok-abc", srv.URL, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if tag != "Glyphoxa#4823" {
		t.Errorf("tag = %q, want Glyphoxa#4823", tag)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 1 {
		t.Fatalf("requests = %v, want exactly one", reqs)
	}
	if want := "GET /users/@me Bot tok-abc"; reqs[0] != want {
		t.Errorf("request = %q, want %q", reqs[0], want)
	}
}
