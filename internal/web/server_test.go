package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
)

func TestNewServer(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		JWTSecret:           "test-jwt-secret-for-new-server-x",
		DiscordClientID:     "id",
		DiscordClientSecret: "secret",
		DiscordRedirectURI:  "http://localhost/callback",
	}
	ws := newMockWebStore()
	ns := newMockNPCStore()

	srv := NewServer(cfg, ws, ns, nil)
	if srv == nil {
		t.Fatal("NewServer returned nil")
	}
	if srv.mux == nil {
		t.Error("mux should not be nil")
	}
	if srv.store != ws {
		t.Error("store not set correctly")
	}
	if srv.npcs != ns {
		t.Error("npcs not set correctly")
	}
}

func TestNewServer_WithVoicePreview(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		JWTSecret:           "test-jwt-secret-for-voice-preview",
		DiscordClientID:     "id",
		DiscordClientSecret: "secret",
		DiscordRedirectURI:  "http://localhost/callback",
	}
	ws := newMockWebStore()
	ns := newMockNPCStore()
	vp := &mockVoicePreviewProvider{}

	srv := NewServer(cfg, ws, ns, nil, WithVoicePreview(vp))
	if srv.voicePreview == nil {
		t.Error("voicePreview should be set")
	}
	if srv.voicePreviewRL == nil {
		t.Error("voicePreviewRL should be set")
	}
}

func TestServer_Handler(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		JWTSecret:           "test-jwt-secret-for-handler-test",
		DiscordClientID:     "id",
		DiscordClientSecret: "secret",
		DiscordRedirectURI:  "http://localhost/callback",
	}
	ws := newMockWebStore()
	ns := newMockNPCStore()

	srv := NewServer(cfg, ws, ns, nil)
	handler := srv.Handler()
	if handler == nil {
		t.Fatal("Handler() returned nil")
	}

	// Verify the full middleware chain works: health endpoint should respond.
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("healthz status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestServer_Handler_CORSPreflight(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		JWTSecret:           "test-jwt-secret-for-cors-preflight",
		DiscordClientID:     "id",
		DiscordClientSecret: "secret",
		DiscordRedirectURI:  "http://localhost/callback",
	}
	ws := newMockWebStore()
	ns := newMockNPCStore()

	srv := NewServer(cfg, ws, ns, nil)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/campaigns", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("OPTIONS status = %d, want %d", rr.Code, http.StatusNoContent)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got == "" {
		t.Error("missing CORS header")
	}
}

func TestServer_RegistrationIntegration(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		JWTSecret:           "test-jwt-secret-for-registration",
		DiscordClientID:     "id",
		DiscordClientSecret: "secret",
		DiscordRedirectURI:  "http://localhost/callback",
	}
	ws := newMockWebStore()
	ns := newMockNPCStore()

	srv := NewServer(cfg, ws, ns, nil)

	// Seed data.
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "t1", Name: "Camp"}
	ns.npcs["n1"] = &npcstore.NPCDefinition{ID: "n1", TenantID: "t1", CampaignID: "c1", Name: "NPC"}

	token := signTestToken(t, cfg.JWTSecret, "u1", "t1", "dm")

	// Verify routes registered correctly by hitting a few endpoints.
	paths := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/v1/campaigns", http.StatusOK},
		{http.MethodGet, "/api/v1/campaigns/c1", http.StatusOK},
		{http.MethodGet, "/api/v1/campaigns/c1/npcs", http.StatusOK},
		{http.MethodGet, "/api/v1/campaigns/c1/npcs/n1", http.StatusOK},
	}

	for _, p := range paths {
		t.Run(p.method+" "+p.path, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(p.method, p.path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != p.want {
				t.Errorf("%s %s: status = %d, want %d", p.method, p.path, rr.Code, p.want)
			}
		})
	}
}

func TestVoicePreviewRateLimiter(t *testing.T) {
	t.Parallel()

	t.Run("allows up to limit", func(t *testing.T) {
		t.Parallel()

		rl := newVoicePreviewRateLimiter(3, time.Minute)
		for i := 0; i < 3; i++ {
			if !rl.Allow("user-1") {
				t.Errorf("request %d should be allowed", i+1)
			}
		}
		if rl.Allow("user-1") {
			t.Error("4th request should be blocked")
		}
	})

	t.Run("different users are independent", func(t *testing.T) {
		t.Parallel()

		rl := newVoicePreviewRateLimiter(2, time.Minute)
		rl.Allow("user-1")
		rl.Allow("user-1")

		// user-1 is at limit but user-2 should still be allowed.
		if !rl.Allow("user-2") {
			t.Error("user-2 should be allowed")
		}
		if rl.Allow("user-1") {
			t.Error("user-1 should be blocked")
		}
	})

	t.Run("expired entries are pruned", func(t *testing.T) {
		t.Parallel()

		// Use a very short window so entries expire quickly.
		rl := newVoicePreviewRateLimiter(1, time.Millisecond)
		rl.Allow("user-1")

		// Wait for the window to expire.
		time.Sleep(5 * time.Millisecond)

		if !rl.Allow("user-1") {
			t.Error("should be allowed after window expires")
		}
	})
}
