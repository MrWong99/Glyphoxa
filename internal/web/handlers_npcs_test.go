package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
)

func TestHandleCreateNPC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{
			name:     "valid NPC",
			body:     `{"name":"Greymantle","personality":"wise sage","engine":"cascaded"}`,
			wantCode: http.StatusCreated,
		},
		{
			name:     "missing name",
			body:     `{"personality":"mysterious"}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "name too long",
			body:     `{"name":"` + strings.Repeat("x", 256) + `"}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "invalid json",
			body:     `{bad`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "minimal valid",
			body:     `{"name":"MinNPC"}`,
			wantCode: http.StatusCreated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, ws, _, secret := testServerWithStores(t)
			auth := AuthMiddleware(secret)
			srv.mux.Handle("POST /api/v1/campaigns/{id}/npcs", auth(RequireRole("dm")(http.HandlerFunc(srv.handleCreateNPC))))

			// Seed a campaign.
			ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

			req := authReq(t, http.MethodPost, "/api/v1/campaigns/c1/npcs",
				bytes.NewBufferString(tt.body), secret, "user-1", "tenant-1", "dm")
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}

			if tt.wantCode == http.StatusCreated {
				var body struct {
					Data npcstore.NPCDefinition `json:"data"`
				}
				if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if body.Data.ID == "" {
					t.Error("expected non-empty NPC ID")
				}
				if body.Data.CampaignID != "c1" {
					t.Errorf("campaign_id = %q, want %q", body.Data.CampaignID, "c1")
				}
			}
		})
	}
}

func TestHandleCreateNPC_CampaignNotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/campaigns/{id}/npcs", auth(RequireRole("dm")(http.HandlerFunc(srv.handleCreateNPC))))

	// No campaign seeded.
	req := authReq(t, http.MethodPost, "/api/v1/campaigns/nonexistent/npcs",
		bytes.NewBufferString(`{"name":"Ghost"}`), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleCreateNPC_WrongTenant(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/campaigns/{id}/npcs", auth(RequireRole("dm")(http.HandlerFunc(srv.handleCreateNPC))))

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	// Authenticate as tenant-2.
	req := authReq(t, http.MethodPost, "/api/v1/campaigns/c1/npcs",
		bytes.NewBufferString(`{"name":"Intruder"}`), secret, "user-2", "tenant-2", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (campaign belongs to different tenant)", rr.Code, http.StatusNotFound)
	}
}

func TestHandleListNPCs(t *testing.T) {
	t.Parallel()

	srv, ws, ns, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/campaigns/{id}/npcs", auth(http.HandlerFunc(srv.handleListNPCs)))

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	// Seed NPCs.
	ns.npcs["npc-1"] = &npcstore.NPCDefinition{ID: "npc-1", TenantID: "tenant-1", CampaignID: "c1", Name: "Heinrich"}
	ns.npcs["npc-2"] = &npcstore.NPCDefinition{ID: "npc-2", TenantID: "tenant-1", CampaignID: "c1", Name: "Mathilde"}
	ns.npcs["npc-3"] = &npcstore.NPCDefinition{ID: "npc-3", TenantID: "tenant-1", CampaignID: "c2", Name: "OtherCampaign"}

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/c1/npcs", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Data []npcstore.NPCDefinition `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 2 {
		t.Errorf("got %d NPCs, want 2", len(body.Data))
	}
}

func TestHandleListNPCs_EmptyReturnsArray(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/campaigns/{id}/npcs", auth(http.HandlerFunc(srv.handleListNPCs)))

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Empty"}

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/c1/npcs", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Data []npcstore.NPCDefinition `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data == nil {
		t.Error("data should be empty array, not null")
	}
}

func TestHandleGetNPC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		npcID    string
		wantCode int
	}{
		{"found", "npc-1", http.StatusOK},
		{"not found", "npc-999", http.StatusNotFound},
		{"wrong campaign", "npc-other", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, ws, ns, secret := testServerWithStores(t)
			auth := AuthMiddleware(secret)
			srv.mux.Handle("GET /api/v1/campaigns/{id}/npcs/{npc_id}", auth(http.HandlerFunc(srv.handleGetNPC)))

			ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
			ns.npcs["npc-1"] = &npcstore.NPCDefinition{ID: "npc-1", TenantID: "tenant-1", CampaignID: "c1", Name: "Heinrich"}
			ns.npcs["npc-other"] = &npcstore.NPCDefinition{ID: "npc-other", TenantID: "tenant-1", CampaignID: "c2", Name: "WrongCampaign"}

			req := authReq(t, http.MethodGet, "/api/v1/campaigns/c1/npcs/"+tt.npcID, nil, secret, "user-1", "tenant-1", "dm")
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantCode)
			}
		})
	}
}

func TestHandleUpdateNPC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		npcID    string
		body     string
		seedNPC  bool
		wantCode int
	}{
		{
			name:     "valid update",
			npcID:    "npc-1",
			body:     `{"name":"NewName","personality":"Cunning"}`,
			seedNPC:  true,
			wantCode: http.StatusOK,
		},
		{
			name:     "not found",
			npcID:    "npc-999",
			body:     `{"name":"X"}`,
			seedNPC:  true,
			wantCode: http.StatusNotFound,
		},
		{
			name:     "invalid json",
			npcID:    "npc-1",
			body:     `{bad`,
			seedNPC:  true,
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, ws, ns, secret := testServerWithStores(t)
			auth := AuthMiddleware(secret)
			srv.mux.Handle("PUT /api/v1/campaigns/{id}/npcs/{npc_id}", auth(RequireRole("dm")(http.HandlerFunc(srv.handleUpdateNPC))))

			ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
			if tt.seedNPC {
				ns.npcs["npc-1"] = &npcstore.NPCDefinition{ID: "npc-1", TenantID: "tenant-1", CampaignID: "c1", Name: "OldName", Personality: "Wise"}
			}

			req := authReq(t, http.MethodPut, "/api/v1/campaigns/c1/npcs/"+tt.npcID,
				bytes.NewBufferString(tt.body), secret, "user-1", "tenant-1", "dm")
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}

			if tt.wantCode == http.StatusOK {
				var body struct {
					Data npcstore.NPCDefinition `json:"data"`
				}
				if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if body.Data.Name != "NewName" {
					t.Errorf("name = %q, want %q", body.Data.Name, "NewName")
				}
			}
		})
	}
}

func TestHandleUpdateNPC_WrongCampaign(t *testing.T) {
	t.Parallel()

	srv, ws, ns, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("PUT /api/v1/campaigns/{id}/npcs/{npc_id}", auth(RequireRole("dm")(http.HandlerFunc(srv.handleUpdateNPC))))

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Campaign1"}
	ns.npcs["npc-1"] = &npcstore.NPCDefinition{ID: "npc-1", TenantID: "tenant-1", CampaignID: "c2", Name: "WrongCampaign"}

	req := authReq(t, http.MethodPut, "/api/v1/campaigns/c1/npcs/npc-1",
		bytes.NewBufferString(`{"name":"Hack"}`), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (NPC belongs to different campaign)", rr.Code, http.StatusNotFound)
	}
}

func TestHandleDeleteNPC(t *testing.T) {
	t.Parallel()

	srv, ws, ns, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("DELETE /api/v1/campaigns/{id}/npcs/{npc_id}", auth(RequireRole("dm")(http.HandlerFunc(srv.handleDeleteNPC))))

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Campaign"}
	ns.npcs["npc-1"] = &npcstore.NPCDefinition{ID: "npc-1", TenantID: "tenant-1", CampaignID: "c1", Name: "ToDelete"}

	req := authReq(t, http.MethodDelete, "/api/v1/campaigns/c1/npcs/npc-1", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}

	if _, ok := ns.npcs["npc-1"]; ok {
		t.Error("NPC should have been deleted from store")
	}
}

func TestHandleDeleteNPC_NotFound(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("DELETE /api/v1/campaigns/{id}/npcs/{npc_id}", auth(RequireRole("dm")(http.HandlerFunc(srv.handleDeleteNPC))))

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Campaign"}

	req := authReq(t, http.MethodDelete, "/api/v1/campaigns/c1/npcs/npc-999", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleDeleteNPC_WrongCampaign(t *testing.T) {
	t.Parallel()

	srv, ws, ns, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("DELETE /api/v1/campaigns/{id}/npcs/{npc_id}", auth(RequireRole("dm")(http.HandlerFunc(srv.handleDeleteNPC))))

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Campaign"}
	ns.npcs["npc-1"] = &npcstore.NPCDefinition{ID: "npc-1", TenantID: "tenant-1", CampaignID: "c-other", Name: "WrongCampaign"}

	req := authReq(t, http.MethodDelete, "/api/v1/campaigns/c1/npcs/npc-1", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}

	// NPC should NOT have been deleted.
	if _, ok := ns.npcs["npc-1"]; !ok {
		t.Error("NPC should not have been deleted (wrong campaign)")
	}
}

func TestHandleListNPCs_CampaignNotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/campaigns/{id}/npcs", auth(http.HandlerFunc(srv.handleListNPCs)))

	// No campaign seeded.
	req := authReq(t, http.MethodGet, "/api/v1/campaigns/nonexistent/npcs", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleListNPCs_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/campaigns/{id}/npcs", auth(http.HandlerFunc(srv.handleListNPCs)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns/c1/npcs", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleGetNPC_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/campaigns/{id}/npcs/{npc_id}", auth(http.HandlerFunc(srv.handleGetNPC)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns/c1/npcs/npc-1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleGetNPC_CampaignNotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/campaigns/{id}/npcs/{npc_id}", auth(http.HandlerFunc(srv.handleGetNPC)))

	// No campaign seeded.
	req := authReq(t, http.MethodGet, "/api/v1/campaigns/nonexistent/npcs/npc-1", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleDeleteNPC_CampaignNotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("DELETE /api/v1/campaigns/{id}/npcs/{npc_id}", auth(RequireRole("dm")(http.HandlerFunc(srv.handleDeleteNPC))))

	req := authReq(t, http.MethodDelete, "/api/v1/campaigns/nonexistent/npcs/npc-1", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleUpdateNPC_CampaignNotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("PUT /api/v1/campaigns/{id}/npcs/{npc_id}", auth(RequireRole("dm")(http.HandlerFunc(srv.handleUpdateNPC))))

	req := authReq(t, http.MethodPut, "/api/v1/campaigns/nonexistent/npcs/npc-1",
		bytes.NewBufferString(`{"name":"X"}`), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleCreateNPC_InsufficientRole(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/campaigns/{id}/npcs", auth(RequireRole("dm")(http.HandlerFunc(srv.handleCreateNPC))))

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Campaign"}

	req := authReq(t, http.MethodPost, "/api/v1/campaigns/c1/npcs",
		bytes.NewBufferString(`{"name":"Blocked"}`), secret, "user-1", "tenant-1", "viewer")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}
