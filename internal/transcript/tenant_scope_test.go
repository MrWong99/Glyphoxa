package transcript_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/transcript"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// scopeCall records one TenantScope invocation so tests can assert the relay
// passed the injected tenant and the requested session through unchanged.
type scopeCall struct {
	tenantID  uuid.UUID
	sessionID uuid.UUID
}

// tenantMux mounts the relay routes with tenantID injected into every request
// context — standing in for the auth.RequireSession + auth.RequireTenant chain
// the production mounts compose (#439).
func tenantMux(r *transcript.Relay, tenantID uuid.UUID) http.Handler {
	inject := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			next(w, req.WithContext(auth.WithTenant(req.Context(), tenantID)))
		}
	}
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/sessions/{id}/events", inject(r.ServeEvents))
	m.HandleFunc("GET /api/v1/sessions/{id}", inject(r.ServeSnapshot))
	return m
}

// bareMux mounts the routes with NO tenant in the context — the miswired-mount
// shape RequireTenant exists to prevent (#408).
func bareMux(r *transcript.Relay) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/sessions/{id}/events", r.ServeEvents)
	m.HandleFunc("GET /api/v1/sessions/{id}", r.ServeSnapshot)
	return m
}

func newScopedRelay(t *testing.T, owns bool, scopeErr error) (*transcript.Relay, *[]scopeCall) {
	t.Helper()
	bus := voiceevent.NewBus()
	r := transcript.NewRelay(bus, &fakeSessions{}, nil, nil)
	calls := &[]scopeCall{}
	r.SetTenantScope(func(_ context.Context, tenantID, sessionID uuid.UUID) (bool, error) {
		*calls = append(*calls, scopeCall{tenantID: tenantID, sessionID: sessionID})
		return owns, scopeErr
	})
	return r, calls
}

// TestSnapshot_TenantScope_OwnSession pins #439: a session in the caller's
// Tenant serves the snapshot exactly as before scoping existed.
func TestSnapshot_TenantScope_OwnSession(t *testing.T) {
	tenantID, sessionID := uuid.New(), uuid.New()
	r, calls := newScopedRelay(t, true, nil)
	srv := httptest.NewServer(tenantMux(r, tenantID))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + sessionID.String())
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own-tenant snapshot status = %d, want 200", resp.StatusCode)
	}
	var v transcript.View
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if v.Status != "idle" || v.Lines == nil {
		t.Fatalf("own-tenant snapshot view = %+v, want idle view with non-nil lines", v)
	}
	if len(*calls) != 1 || (*calls)[0] != (scopeCall{tenantID: tenantID, sessionID: sessionID}) {
		t.Fatalf("scope calls = %+v, want exactly one with the injected tenant + requested session", *calls)
	}
}

// TestSnapshot_TenantScope_ForeignSession pins the #439 posture: a session
// outside the caller's Tenant is 404 — existence is never leaked (the same
// don't-reveal posture as the Highlight mounts).
func TestSnapshot_TenantScope_ForeignSession(t *testing.T) {
	r, _ := newScopedRelay(t, false, nil)
	srv := httptest.NewServer(tenantMux(r, uuid.New()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + uuid.NewString())
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign-tenant snapshot status = %d, want 404", resp.StatusCode)
	}
}

// TestSnapshot_TenantScope_MissingTenant pins the fail-closed posture: with a
// scope installed but no tenant in the context (a miswired mount, the #408
// class), the request is rejected 401 rather than served unscoped.
func TestSnapshot_TenantScope_MissingTenant(t *testing.T) {
	r, calls := newScopedRelay(t, true, nil)
	srv := httptest.NewServer(bareMux(r))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + uuid.NewString())
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tenantless snapshot status = %d, want 401", resp.StatusCode)
	}
	if len(*calls) != 0 {
		t.Fatalf("scope must not be consulted without a tenant, got %d calls", len(*calls))
	}
}

// TestSnapshot_TenantScope_Error pins that a scope-check failure is a 500, not
// an open door (fail closed) and not a silent 404 (an infra error must not
// masquerade as absence).
func TestSnapshot_TenantScope_Error(t *testing.T) {
	r, _ := newScopedRelay(t, false, errors.New("db down"))
	srv := httptest.NewServer(tenantMux(r, uuid.New()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + uuid.NewString())
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("scope-error snapshot status = %d, want 500", resp.StatusCode)
	}
}

// TestServeEvents_TenantScope_ForeignSession pins #439's SSE acceptance
// criterion: the foreign-tenant rejection happens BEFORE the stream opens —
// the response is a plain 404, never a half-opened event stream.
func TestServeEvents_TenantScope_ForeignSession(t *testing.T) {
	r, _ := newScopedRelay(t, false, nil)
	srv := httptest.NewServer(tenantMux(r, uuid.New()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + uuid.NewString() + "/events")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign-tenant SSE status = %d, want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "text/event-stream") {
		t.Fatalf("foreign-tenant SSE opened a stream (Content-Type %q); rejection must precede the stream", ct)
	}
}

// TestServeEvents_TenantScope_OwnSession pins that an own-tenant SSE connect
// still opens the stream normally under scoping.
func TestServeEvents_TenantScope_OwnSession(t *testing.T) {
	tenantID, sessionID := uuid.New(), uuid.New()
	r, _ := newScopedRelay(t, true, nil)
	srv := httptest.NewServer(tenantMux(r, tenantID))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+"/api/v1/sessions/"+sessionID.String()+"/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own-tenant SSE status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("own-tenant SSE Content-Type = %q, want text/event-stream", ct)
	}
}

// TestSnapshot_NoTenantScope_Unscoped pins backwards compatibility: with no
// scope installed (nil — a voice-standalone build or a unit-test relay), the
// endpoints stay unscoped exactly as before #439.
func TestSnapshot_NoTenantScope_Unscoped(t *testing.T) {
	bus := voiceevent.NewBus()
	r := transcript.NewRelay(bus, &fakeSessions{}, nil, nil)
	srv := httptest.NewServer(bareMux(r))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + uuid.NewString())
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unscoped snapshot status = %d, want 200", resp.StatusCode)
	}
}
