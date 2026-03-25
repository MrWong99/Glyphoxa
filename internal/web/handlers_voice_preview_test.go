package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
)

func TestHandleVoicePreview_ReturnsAudio(t *testing.T) {
	t.Parallel()

	srv, ws, ns, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ns.npcs["npc-1"] = &npcstore.NPCDefinition{
		ID:         "npc-1",
		CampaignID: "c1",
		Name:       "TestNPC",
		Voice:      npcstore.VoiceConfig{Provider: "elevenlabs", VoiceID: "v1"},
	}

	body := `{"text":"Hello world"}`
	req := authReq(t, http.MethodPost, "/api/v1/npcs/npc-1/voice-preview",
		bytes.NewBufferString(body), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	ct := rr.Header().Get("Content-Type")
	if ct != "audio/mpeg" {
		t.Errorf("Content-Type = %q, want %q", ct, "audio/mpeg")
	}

	if rr.Body.Len() == 0 {
		t.Error("expected non-empty audio response body")
	}
}

func TestHandleVoicePreview_DefaultText(t *testing.T) {
	t.Parallel()

	srv, ws, ns, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ns.npcs["npc-1"] = &npcstore.NPCDefinition{
		ID:         "npc-1",
		CampaignID: "c1",
		Name:       "TestNPC",
		Voice:      npcstore.VoiceConfig{Provider: "elevenlabs", VoiceID: "v1"},
	}

	// Empty body should use default text.
	req := authReq(t, http.MethodPost, "/api/v1/npcs/npc-1/voice-preview",
		bytes.NewBufferString(`{}`), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestHandleVoicePreview_TextTooLong(t *testing.T) {
	t.Parallel()

	srv, ws, ns, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ns.npcs["npc-1"] = &npcstore.NPCDefinition{
		ID:         "npc-1",
		CampaignID: "c1",
		Name:       "TestNPC",
		Voice:      npcstore.VoiceConfig{Provider: "elevenlabs", VoiceID: "v1"},
	}

	body := `{"text":"` + strings.Repeat("x", 501) + `"}`
	req := authReq(t, http.MethodPost, "/api/v1/npcs/npc-1/voice-preview",
		bytes.NewBufferString(body), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleVoicePreview_NPCNotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	req := authReq(t, http.MethodPost, "/api/v1/npcs/nonexistent/voice-preview",
		bytes.NewBufferString(`{}`), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleVoicePreview_RateLimiting(t *testing.T) {
	t.Parallel()

	srv, ws, ns, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ns.npcs["npc-1"] = &npcstore.NPCDefinition{
		ID:         "npc-1",
		CampaignID: "c1",
		Name:       "TestNPC",
		Voice:      npcstore.VoiceConfig{Provider: "elevenlabs", VoiceID: "v1"},
	}

	// Exhaust the rate limit (5 requests).
	for i := 0; i < 5; i++ {
		req := authReq(t, http.MethodPost, "/api/v1/npcs/npc-1/voice-preview",
			bytes.NewBufferString(`{}`), secret, "user-1", "tenant-1", "dm")
		rr := httptest.NewRecorder()
		srv.mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want %d", i+1, rr.Code, http.StatusOK)
		}
	}

	// 6th request should be rate limited.
	req := authReq(t, http.MethodPost, "/api/v1/npcs/npc-1/voice-preview",
		bytes.NewBufferString(`{}`), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusTooManyRequests)
	}
}

func TestHandleVoicePreview_NoProvider(t *testing.T) {
	t.Parallel()

	srv, ws, ns, secret := testServerWithStores(t)
	// Explicitly set voicePreview to nil.
	srv.voicePreview = nil
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/npcs/{npc_id}/voice-preview", auth(http.HandlerFunc(srv.handleVoicePreview)))

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ns.npcs["npc-1"] = &npcstore.NPCDefinition{
		ID:         "npc-1",
		CampaignID: "c1",
		Name:       "TestNPC",
		Voice:      npcstore.VoiceConfig{Provider: "elevenlabs", VoiceID: "v1"},
	}

	req := authReq(t, http.MethodPost, "/api/v1/npcs/npc-1/voice-preview",
		bytes.NewBufferString(`{}`), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleVoicePreview_WrongTenant(t *testing.T) {
	t.Parallel()

	srv, ws, ns, secret := testServerWithStores(t)
	srv.registerRoutes()

	// Campaign belongs to tenant-1.
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ns.npcs["npc-1"] = &npcstore.NPCDefinition{
		ID:         "npc-1",
		CampaignID: "c1",
		Name:       "TestNPC",
		Voice:      npcstore.VoiceConfig{Provider: "elevenlabs", VoiceID: "v1"},
	}

	// Authenticate as tenant-2.
	req := authReq(t, http.MethodPost, "/api/v1/npcs/npc-1/voice-preview",
		bytes.NewBufferString(`{}`), secret, "user-2", "tenant-2", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (wrong tenant)", rr.Code, http.StatusNotFound)
	}
}

func TestHandleVoicePreview_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/npcs/{npc_id}/voice-preview", auth(http.HandlerFunc(srv.handleVoicePreview)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/npcs/npc-1/voice-preview", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleVoicePreview_ExactMaxLength(t *testing.T) {
	t.Parallel()

	srv, ws, ns, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ns.npcs["npc-1"] = &npcstore.NPCDefinition{
		ID:         "npc-1",
		CampaignID: "c1",
		Name:       "TestNPC",
		Voice:      npcstore.VoiceConfig{Provider: "elevenlabs", VoiceID: "v1"},
	}

	// Exactly 500 characters should be allowed.
	body := `{"text":"` + strings.Repeat("x", 500) + `"}`
	req := authReq(t, http.MethodPost, "/api/v1/npcs/npc-1/voice-preview",
		bytes.NewBufferString(body), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (500 chars should be allowed)", rr.Code, http.StatusOK)
	}
}
