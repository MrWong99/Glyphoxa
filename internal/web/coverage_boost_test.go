package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
)

// --- handleListCampaigns pagination (has_more branch) ---

func TestHandleListCampaigns_Pagination_HasMore(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/campaigns", auth(http.HandlerFunc(srv.handleListCampaigns)))

	// Seed 3 campaigns so that with limit=2, has_more=true.
	now := time.Now().UTC()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "A", CreatedAt: now}
	ws.campaigns["c2"] = &Campaign{ID: "c2", TenantID: "tenant-1", Name: "B", CreatedAt: now.Add(time.Second)}
	ws.campaigns["c3"] = &Campaign{ID: "c3", TenantID: "tenant-1", Name: "C", CreatedAt: now.Add(2 * time.Second)}

	req := authReq(t, http.MethodGet, "/api/v1/campaigns?limit=2", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var body struct {
		Data       []Campaign `json:"data"`
		Pagination PageMeta   `json:"pagination"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Pagination.Limit != 2 {
		t.Errorf("pagination.limit = %d, want 2", body.Pagination.Limit)
	}
	if !body.Pagination.HasMore {
		t.Error("pagination.has_more should be true when more campaigns exist than limit")
	}
	if body.Pagination.NextCursor == "" {
		t.Error("pagination.next_cursor should be set when has_more is true")
	}
	if len(body.Data) != 2 {
		t.Errorf("got %d campaigns, want 2 (trimmed to limit)", len(body.Data))
	}
}

func TestHandleListCampaigns_Pagination_NoMore(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/campaigns", auth(http.HandlerFunc(srv.handleListCampaigns)))

	// Seed 2 campaigns with limit=25 (default), so has_more=false.
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "A"}
	ws.campaigns["c2"] = &Campaign{ID: "c2", TenantID: "tenant-1", Name: "B"}

	req := authReq(t, http.MethodGet, "/api/v1/campaigns", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Pagination PageMeta `json:"pagination"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Pagination.HasMore {
		t.Error("pagination.has_more should be false when all campaigns fit in one page")
	}
	if body.Pagination.NextCursor != "" {
		t.Errorf("pagination.next_cursor should be empty, got %q", body.Pagination.NextCursor)
	}
}

// --- handleDeleteCampaign error path ---

func TestHandleDeleteCampaign_NotFoundInStore(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("DELETE /api/v1/campaigns/{id}", auth(RequireRole("dm")(http.HandlerFunc(srv.handleDeleteCampaign))))

	// No campaign seeded — delete should trigger the store error path.
	req := authReq(t, http.MethodDelete, "/api/v1/campaigns/nonexistent", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	// The mock store returns nil error for deleting nonexistent campaigns
	// (delete from map silently). The handler just calls w.WriteHeader(StatusNoContent).
	// But the actual handler doesn't check for "not found" from store; it always returns 204.
	// This test confirms the handler behavior.
	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}
}

// --- handleUpdateUser: self-update with display_name ---

func TestHandleUpdateUser_SelfUpdateName(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("PUT /api/v1/users/{id}", auth(http.HandlerFunc(srv.handleUpdateUser)))

	ws.users["u1"] = &User{ID: "u1", TenantID: "tenant-1", DisplayName: "Alice", Role: "viewer"}

	req := authReq(t, http.MethodPut, "/api/v1/users/u1",
		bytes.NewBufferString(`{"display_name":"Alice B"}`), secret, "u1", "tenant-1", "viewer")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var body struct {
		Data User `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.DisplayName != "Alice B" {
		t.Errorf("display_name = %q, want %q", body.Data.DisplayName, "Alice B")
	}
}

// --- handleUpdateMe: unauthenticated and user not found paths ---

func TestHandleUpdateMe_UserNotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("PUT /api/v1/auth/me", auth(http.HandlerFunc(srv.handleUpdateMe)))

	// No user seeded — UpdateUser will fail.
	req := authReq(t, http.MethodPut, "/api/v1/auth/me",
		bytes.NewBufferString(`{"display_name":"Ghost"}`), secret, "nonexistent", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
	}
}

// --- handleUpdatePreferences: unauthenticated ---

func TestHandleUpdatePreferences_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("PATCH /api/v1/auth/me/preferences", auth(http.HandlerFunc(srv.handleUpdatePreferences)))

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/auth/me/preferences",
		bytes.NewBufferString(`{"theme":"dark"}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleCreateNPC with custom ID ---

func TestHandleCreateNPC_WithCustomID(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/campaigns/{id}/npcs", auth(RequireRole("dm")(http.HandlerFunc(srv.handleCreateNPC))))

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	body := `{"id":"custom-npc-id","name":"CustomNPC","personality":"Wise"}`
	req := authReq(t, http.MethodPost, "/api/v1/campaigns/c1/npcs",
		bytes.NewBufferString(body), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	var resp struct {
		Data npcstore.NPCDefinition `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.ID != "custom-npc-id" {
		t.Errorf("NPC ID = %q, want %q", resp.Data.ID, "custom-npc-id")
	}
}

// --- handleCreateNPC with full voice config ---

func TestHandleCreateNPC_FullConfig(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/campaigns/{id}/npcs", auth(RequireRole("dm")(http.HandlerFunc(srv.handleCreateNPC))))

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	body := `{
		"name":"FullNPC",
		"personality":"Brave",
		"engine":"cascaded",
		"voice":{"provider":"elevenlabs","voice_id":"v1"},
		"knowledge_scope":["local history"],
		"secret_knowledge":["hidden treasure"],
		"behavior_rules":["be friendly"],
		"tools":["dice_roll"],
		"budget_tier":"standard",
		"gm_helper":true,
		"address_only":false,
		"attributes":{"level":5}
	}`
	req := authReq(t, http.MethodPost, "/api/v1/campaigns/c1/npcs",
		bytes.NewBufferString(body), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	var resp struct {
		Data npcstore.NPCDefinition `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Personality != "Brave" {
		t.Errorf("personality = %q, want %q", resp.Data.Personality, "Brave")
	}
	if !resp.Data.GMHelper {
		t.Error("gm_helper should be true")
	}
	if len(resp.Data.KnowledgeScope) != 1 {
		t.Errorf("knowledge_scope length = %d, want 1", len(resp.Data.KnowledgeScope))
	}
}

// --- handleGetCampaign: wrong tenant ---

func TestHandleGetCampaign_WrongTenant(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/campaigns/{id}", auth(http.HandlerFunc(srv.handleGetCampaign)))

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Found"}

	// Authenticate as tenant-2.
	req := authReq(t, http.MethodGet, "/api/v1/campaigns/c1", nil, secret, "user-2", "tenant-2", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (wrong tenant)", rr.Code, http.StatusNotFound)
	}
}

// --- handleUpdateCampaign: wrong tenant ---

func TestHandleUpdateCampaign_WrongTenant(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("PUT /api/v1/campaigns/{id}", auth(RequireRole("dm")(http.HandlerFunc(srv.handleUpdateCampaign))))

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Original"}

	// Authenticate as tenant-2 — should not be able to update.
	req := authReq(t, http.MethodPut, "/api/v1/campaigns/c1",
		bytes.NewBufferString(`{"name":"Hacked"}`), secret, "user-2", "tenant-2", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

// --- handleUpdateCampaign: update all fields ---

func TestHandleUpdateCampaign_AllFields(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("PUT /api/v1/campaigns/{id}", auth(RequireRole("dm")(http.HandlerFunc(srv.handleUpdateCampaign))))

	ws.campaigns["c1"] = &Campaign{
		ID:          "c1",
		TenantID:    "tenant-1",
		Name:        "Old",
		System:      "D&D 5e",
		Language:    "en",
		Description: "Old desc",
	}

	body := `{"name":"New","game_system":"PF2e","language":"de","description":"New desc"}`
	req := authReq(t, http.MethodPut, "/api/v1/campaigns/c1",
		bytes.NewBufferString(body), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp struct {
		Data Campaign `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Name != "New" {
		t.Errorf("name = %q, want %q", resp.Data.Name, "New")
	}
	if resp.Data.System != "PF2e" {
		t.Errorf("system = %q, want %q", resp.Data.System, "PF2e")
	}
	if resp.Data.Language != "de" {
		t.Errorf("language = %q, want %q", resp.Data.Language, "de")
	}
	if resp.Data.Description != "New desc" {
		t.Errorf("description = %q, want %q", resp.Data.Description, "New desc")
	}
}

// --- handleMe: error from store (simulate by overriding mock behavior) ---

func TestHandleMe_NoClaims(t *testing.T) {
	t.Parallel()

	// Call handleMe directly without claims in context to hit the first branch.
	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /me-direct", srv.handleMe)

	req := httptest.NewRequest(http.MethodGet, "/me-direct", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleRefresh: no claims (direct call without auth middleware) ---

func TestHandleRefresh_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("POST /refresh-direct", srv.handleRefresh)

	req := httptest.NewRequest(http.MethodPost, "/refresh-direct", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleListSessions: no claims (direct call without auth middleware) ---

func TestHandleListSessions_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /sessions-direct", srv.handleListSessions)

	req := httptest.NewRequest(http.MethodGet, "/sessions-direct", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleGetTranscript: no claims (direct call without auth middleware) ---

func TestHandleGetTranscript_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /transcript-direct/{id}", srv.handleGetTranscript)

	req := httptest.NewRequest(http.MethodGet, "/transcript-direct/s1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleStartSession: no claims (direct call without auth middleware) ---

func TestHandleStartSession_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("POST /start-direct", srv.handleStartSession)

	req := httptest.NewRequest(http.MethodPost, "/start-direct", bytes.NewBufferString(`{}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleStopSession: no claims (direct call without auth middleware) ---

func TestHandleStopSession_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("POST /stop-direct/{id}", srv.handleStopSession)

	req := httptest.NewRequest(http.MethodPost, "/stop-direct/s1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleListActiveSessions: no claims (direct call without auth middleware) ---

func TestHandleListActiveSessions_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /active-direct", srv.handleListActiveSessions)

	req := httptest.NewRequest(http.MethodGet, "/active-direct", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleDashboardStats: no claims ---

func TestHandleDashboardStats_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /stats-direct", srv.handleDashboardStats)

	req := httptest.NewRequest(http.MethodGet, "/stats-direct", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleDashboardActivity: no claims ---

func TestHandleDashboardActivity_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /activity-direct", srv.handleDashboardActivity)

	req := httptest.NewRequest(http.MethodGet, "/activity-direct", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleGetUsage: no claims ---

func TestHandleGetUsage_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /usage-direct", srv.handleGetUsage)

	req := httptest.NewRequest(http.MethodGet, "/usage-direct", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleCreateCampaign: no claims ---

func TestHandleCreateCampaign_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("POST /campaign-direct", srv.handleCreateCampaign)

	req := httptest.NewRequest(http.MethodPost, "/campaign-direct",
		bytes.NewBufferString(`{"name":"X"}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleListCampaigns: no claims ---

func TestHandleListCampaigns_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /campaigns-direct", srv.handleListCampaigns)

	req := httptest.NewRequest(http.MethodGet, "/campaigns-direct", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleGetCampaign: no claims ---

func TestHandleGetCampaign_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /campaign-direct/{id}", srv.handleGetCampaign)

	req := httptest.NewRequest(http.MethodGet, "/campaign-direct/c1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleUpdateCampaign: no claims ---

func TestHandleUpdateCampaign_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("PUT /campaign-direct/{id}", srv.handleUpdateCampaign)

	req := httptest.NewRequest(http.MethodPut, "/campaign-direct/c1",
		bytes.NewBufferString(`{"name":"X"}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleDeleteCampaign: no claims ---

func TestHandleDeleteCampaign_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("DELETE /campaign-direct/{id}", srv.handleDeleteCampaign)

	req := httptest.NewRequest(http.MethodDelete, "/campaign-direct/c1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleListUsers: no claims ---

func TestHandleListUsers_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /users-direct", srv.handleListUsers)

	req := httptest.NewRequest(http.MethodGet, "/users-direct", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleGetUser: no claims ---

func TestHandleGetUser_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /users-direct/{id}", srv.handleGetUser)

	req := httptest.NewRequest(http.MethodGet, "/users-direct/u1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleUpdateUser: no claims ---

func TestHandleUpdateUser_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("PUT /users-direct/{id}", srv.handleUpdateUser)

	req := httptest.NewRequest(http.MethodPut, "/users-direct/u1",
		bytes.NewBufferString(`{"display_name":"X"}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleDeleteUser: no claims ---

func TestHandleDeleteUser_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("DELETE /users-direct/{id}", srv.handleDeleteUser)

	req := httptest.NewRequest(http.MethodDelete, "/users-direct/u1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleCreateInvite: no claims ---

func TestHandleCreateInvite_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("POST /invite-direct", srv.handleCreateInvite)

	req := httptest.NewRequest(http.MethodPost, "/invite-direct",
		bytes.NewBufferString(`{"role":"viewer"}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleUpdateMe: no claims ---

func TestHandleUpdateMe_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("PUT /me-direct", srv.handleUpdateMe)

	req := httptest.NewRequest(http.MethodPut, "/me-direct",
		bytes.NewBufferString(`{"display_name":"X"}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleUpdatePreferences: no claims ---

func TestHandleUpdatePreferences_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("PATCH /prefs-direct", srv.handleUpdatePreferences)

	req := httptest.NewRequest(http.MethodPatch, "/prefs-direct",
		bytes.NewBufferString(`{"theme":"dark"}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleOnboardingComplete: no claims ---

func TestHandleOnboardingComplete_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("POST /onboard-direct", srv.handleOnboardingComplete)

	req := httptest.NewRequest(http.MethodPost, "/onboard-direct",
		bytes.NewBufferString(`{"tenant_id":"test"}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleCreateNPC: no claims ---

func TestHandleCreateNPC_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("POST /npc-direct/{id}", srv.handleCreateNPC)

	req := httptest.NewRequest(http.MethodPost, "/npc-direct/c1",
		bytes.NewBufferString(`{"name":"X"}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleListNPCs: no claims ---

func TestHandleListNPCs_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /npcs-direct/{id}", srv.handleListNPCs)

	req := httptest.NewRequest(http.MethodGet, "/npcs-direct/c1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleGetNPC: no claims ---

func TestHandleGetNPC_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /npc-direct/{id}/{npc_id}", srv.handleGetNPC)

	req := httptest.NewRequest(http.MethodGet, "/npc-direct/c1/n1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleUpdateNPC: no claims ---

func TestHandleUpdateNPC_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("PUT /npc-update-direct/{id}/{npc_id}", srv.handleUpdateNPC)

	req := httptest.NewRequest(http.MethodPut, "/npc-update-direct/c1/n1",
		bytes.NewBufferString(`{"name":"X"}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleDeleteNPC: no claims ---

func TestHandleDeleteNPC_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("DELETE /npc-del-direct/{id}/{npc_id}", srv.handleDeleteNPC)

	req := httptest.NewRequest(http.MethodDelete, "/npc-del-direct/c1/n1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleVoicePreview: no claims ---

func TestHandleVoicePreview_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("POST /vp-direct/{npc_id}", srv.handleVoicePreview)

	req := httptest.NewRequest(http.MethodPost, "/vp-direct/n1",
		bytes.NewBufferString(`{}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleCreateLoreDocument: no claims ---

func TestHandleCreateLoreDocument_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("POST /lore-direct/{id}", srv.handleCreateLoreDocument)

	req := httptest.NewRequest(http.MethodPost, "/lore-direct/c1",
		bytes.NewBufferString(`{"title":"X"}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleListLoreDocuments: no claims ---

func TestHandleListLoreDocuments_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /lore-list-direct/{id}", srv.handleListLoreDocuments)

	req := httptest.NewRequest(http.MethodGet, "/lore-list-direct/c1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleGetLoreDocument: no claims ---

func TestHandleGetLoreDocument_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /lore-get-direct/{id}/{lore_id}", srv.handleGetLoreDocument)

	req := httptest.NewRequest(http.MethodGet, "/lore-get-direct/c1/l1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleUpdateLoreDocument: no claims ---

func TestHandleUpdateLoreDocument_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("PUT /lore-upd-direct/{id}/{lore_id}", srv.handleUpdateLoreDocument)

	req := httptest.NewRequest(http.MethodPut, "/lore-upd-direct/c1/l1",
		bytes.NewBufferString(`{"title":"X"}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleDeleteLoreDocument: no claims ---

func TestHandleDeleteLoreDocument_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("DELETE /lore-del-direct/{id}/{lore_id}", srv.handleDeleteLoreDocument)

	req := httptest.NewRequest(http.MethodDelete, "/lore-del-direct/c1/l1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleLinkNPCToCampaign: no claims ---

func TestHandleLinkNPC_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("POST /link-direct/{id}/{npc_id}", srv.handleLinkNPCToCampaign)

	req := httptest.NewRequest(http.MethodPost, "/link-direct/c1/n1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleUnlinkNPCFromCampaign: no claims ---

func TestHandleUnlinkNPC_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("DELETE /unlink-direct/{id}/{npc_id}", srv.handleUnlinkNPCFromCampaign)

	req := httptest.NewRequest(http.MethodDelete, "/unlink-direct/c1/n1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleListLinkedNPCs: no claims ---

func TestHandleListLinkedNPCs_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /linked-direct/{id}", srv.handleListLinkedNPCs)

	req := httptest.NewRequest(http.MethodGet, "/linked-direct/c1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleListKnowledgeEntities: no claims ---

func TestHandleListKnowledge_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /knowledge-direct/{id}", srv.handleListKnowledgeEntities)

	req := httptest.NewRequest(http.MethodGet, "/knowledge-direct/c1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleDeleteKnowledgeEntity: no claims ---

func TestHandleDeleteKnowledge_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("DELETE /knowledge-del-direct/{id}/{entity_id}", srv.handleDeleteKnowledgeEntity)

	req := httptest.NewRequest(http.MethodDelete, "/knowledge-del-direct/c1/e1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleRebuildKnowledgeGraph: no claims ---

func TestHandleRebuildKnowledge_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("POST /rebuild-direct/{id}", srv.handleRebuildKnowledgeGraph)

	req := httptest.NewRequest(http.MethodPost, "/rebuild-direct/c1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleListNPCTemplates: no claims ---

func TestHandleListNPCTemplates_NoClaims(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /templates-direct", srv.handleListNPCTemplates)

	req := httptest.NewRequest(http.MethodGet, "/templates-direct", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// --- handleListUsers: pagination with custom limit ---

func TestHandleListUsers_CustomPagination(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/users", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleListUsers))))

	for i := 0; i < 5; i++ {
		id := "u" + string(rune('1'+i))
		ws.users[id] = &User{ID: id, TenantID: "tenant-1", DisplayName: "User " + id, Role: "viewer"}
	}

	req := authReq(t, http.MethodGet, "/api/v1/users?limit=2&offset=1", nil, secret, "u1", "tenant-1", "tenant_admin")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var body struct {
		Data  []User `json:"data"`
		Total int    `json:"total"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 2 {
		t.Errorf("got %d users, want 2", len(body.Data))
	}
	if body.Total != 5 {
		t.Errorf("total = %d, want 5", body.Total)
	}
}

// --- handleListUsers: empty result returns array not null ---

func TestHandleListUsers_EmptyReturnsArray(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/users", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleListUsers))))

	req := authReq(t, http.MethodGet, "/api/v1/users", nil, secret, "u1", "tenant-1", "tenant_admin")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Data []User `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data == nil {
		t.Error("data should be empty array, not null")
	}
}

// --- handleCreateLoreDocument: "content" field alias when content_markdown is also present ---

func TestHandleCreateLoreDocument_ContentMarkdownTakesPrecedence(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	// When both "content" and "content_markdown" are present, content_markdown wins.
	body := `{"title":"Both","content":"via content","content_markdown":"via markdown"}`
	req := authReq(t, http.MethodPost, "/api/v1/campaigns/c1/lore",
		bytes.NewBufferString(body), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	var resp struct {
		Data LoreDocument `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.ContentMarkdown != "via markdown" {
		t.Errorf("content_markdown = %q, want %q", resp.Data.ContentMarkdown, "via markdown")
	}
}

// --- handleUpdateLoreDocument: update content_markdown field ---

func TestHandleUpdateLoreDocument_UpdateContent(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ws.loreDocs["l1"] = &LoreDocument{ID: "l1", CampaignID: "c1", Title: "Original", ContentMarkdown: "Old content"}

	body := `{"content_markdown":"New content"}`
	req := authReq(t, http.MethodPut, "/api/v1/campaigns/c1/lore/l1",
		bytes.NewBufferString(body), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp struct {
		Data LoreDocument `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.ContentMarkdown != "New content" {
		t.Errorf("content_markdown = %q, want %q", resp.Data.ContentMarkdown, "New content")
	}
	if resp.Data.Title != "Original" {
		t.Errorf("title = %q, want %q (should be preserved)", resp.Data.Title, "Original")
	}
}

// --- ParseCursorPage: negative and zero limits ---

func TestParseCursorPage_ZeroLimit(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/test?limit=0", nil)
	page := ParseCursorPage(req)

	// limit=0 is not > 0, so it falls through to default 25.
	if page.Limit != 25 {
		t.Errorf("limit = %d, want 25 (default for zero)", page.Limit)
	}
}

func TestParseCursorPage_NegativeLimit(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/test?limit=-5", nil)
	page := ParseCursorPage(req)

	// Negative is not > 0, so it falls through to default 25.
	if page.Limit != 25 {
		t.Errorf("limit = %d, want 25 (default for negative)", page.Limit)
	}
}

// --- ClaimsFromContext with non-claims value ---

func TestClaimsFromContext_NilContext(t *testing.T) {
	t.Parallel()

	// Context with no claims should return nil.
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	claims := ClaimsFromContext(req.Context())
	if claims != nil {
		t.Errorf("expected nil claims, got %+v", claims)
	}
}

// --- handleDiscordLogin: basic redirect (without invite) ---

func TestHandleDiscordLogin_BasicRedirect(t *testing.T) {
	t.Parallel()

	srv, _ := testServer(t)
	srv.mux.HandleFunc("GET /api/v1/auth/discord", srv.handleDiscordLogin)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/discord", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}

	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "discord.com/oauth2/authorize") {
		t.Errorf("Location = %q, want discord OAuth URL", loc)
	}
	if !strings.Contains(loc, "scope=identify+email") && !strings.Contains(loc, "scope=identify%20email") {
		t.Errorf("Location should contain scope param, got %q", loc)
	}

	// Should set only state cookie (no invite cookie).
	cookies := rr.Result().Cookies()
	var hasState, hasInvite bool
	for _, c := range cookies {
		if c.Name == "glyphoxa_oauth_state" {
			hasState = true
		}
		if c.Name == "glyphoxa_invite" {
			hasInvite = true
		}
	}
	if !hasState {
		t.Error("missing glyphoxa_oauth_state cookie")
	}
	if hasInvite {
		t.Error("should not set invite cookie without invite param")
	}
}

// --- handleDiscordCallback: state cookie with empty value ---

func TestHandleDiscordCallback_EmptyStateCookie(t *testing.T) {
	t.Parallel()

	srv, _ := testServer(t)
	srv.mux.HandleFunc("GET /api/v1/auth/discord/callback", srv.handleDiscordCallback)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/discord/callback?code=test&state=abc", nil)
	req.AddCookie(&http.Cookie{Name: "glyphoxa_oauth_state", Value: ""})
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (empty state cookie)", rr.Code, http.StatusBadRequest)
	}
}
