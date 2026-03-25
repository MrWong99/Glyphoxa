package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
)

func TestHandleLinkNPCToCampaign(t *testing.T) {
	t.Parallel()

	srv, ws, ns, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Campaign A"}
	ws.campaigns["c2"] = &Campaign{ID: "c2", TenantID: "tenant-1", Name: "Campaign B"}
	ns.npcs["npc-1"] = &npcstore.NPCDefinition{ID: "npc-1", CampaignID: "c1", Name: "Traveler"}

	// Link npc-1 (home: c1) to c2.
	req := authReq(t, http.MethodPost, "/api/v1/campaigns/c2/npcs/npc-1/link",
		nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	// Verify link exists.
	if len(ws.campaignNPCLinks["c2"]) != 1 {
		t.Errorf("expected 1 link in campaign c2, got %d", len(ws.campaignNPCLinks["c2"]))
	}
}

func TestHandleLinkNPCToCampaign_HomeFails(t *testing.T) {
	t.Parallel()

	srv, ws, ns, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Campaign A"}
	ns.npcs["npc-1"] = &npcstore.NPCDefinition{ID: "npc-1", CampaignID: "c1", Name: "HomeNPC"}

	// Linking to home campaign should fail.
	req := authReq(t, http.MethodPost, "/api/v1/campaigns/c1/npcs/npc-1/link",
		nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestHandleLinkNPCToCampaign_NonexistentNPC(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Campaign A"}

	req := authReq(t, http.MethodPost, "/api/v1/campaigns/c1/npcs/nonexistent/link",
		nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleListLinkedNPCs(t *testing.T) {
	t.Parallel()

	srv, ws, ns, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c2"] = &Campaign{ID: "c2", TenantID: "tenant-1", Name: "Campaign B"}
	ns.npcs["npc-1"] = &npcstore.NPCDefinition{ID: "npc-1", CampaignID: "c1", Name: "Traveler"}

	// Seed a link.
	ws.campaignNPCLinks["c2"] = []CampaignNPCLink{
		{CampaignID: "c2", NPCID: "npc-1"},
	}

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/c2/linked-npcs",
		nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Data []struct {
			CampaignID string                  `json:"campaign_id"`
			NPCID      string                  `json:"npc_id"`
			NPC        *npcstore.NPCDefinition `json:"npc"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("got %d linked NPCs, want 1", len(resp.Data))
	}
	if resp.Data[0].NPC == nil {
		t.Error("expected resolved NPC definition, got nil")
	}
	if resp.Data[0].NPC.Name != "Traveler" {
		t.Errorf("NPC name = %q, want %q", resp.Data[0].NPC.Name, "Traveler")
	}
}

func TestHandleUnlinkNPCFromCampaign(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c2"] = &Campaign{ID: "c2", TenantID: "tenant-1", Name: "Campaign B"}
	ws.campaignNPCLinks["c2"] = []CampaignNPCLink{
		{CampaignID: "c2", NPCID: "npc-1"},
	}

	req := authReq(t, http.MethodDelete, "/api/v1/campaigns/c2/npcs/npc-1/link",
		nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusNoContent, rr.Body.String())
	}

	if len(ws.campaignNPCLinks["c2"]) != 0 {
		t.Error("link should have been removed")
	}
}

func TestHandleLinkNPCToCampaign_CampaignNotFound(t *testing.T) {
	t.Parallel()

	srv, _, ns, secret := testServerWithStores(t)
	srv.registerRoutes()

	ns.npcs["npc-1"] = &npcstore.NPCDefinition{ID: "npc-1", CampaignID: "c1", Name: "Traveler"}

	// Campaign does not exist.
	req := authReq(t, http.MethodPost, "/api/v1/campaigns/nonexistent/npcs/npc-1/link",
		nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleLinkNPCToCampaign_CrossTenantBlocked(t *testing.T) {
	t.Parallel()

	srv, ws, ns, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Campaign A"}
	ws.campaigns["c2"] = &Campaign{ID: "c2", TenantID: "tenant-1", Name: "Campaign B"}
	// NPC belongs to campaign owned by a different tenant.
	ws.campaigns["c-other"] = &Campaign{ID: "c-other", TenantID: "tenant-2", Name: "Other"}
	ns.npcs["npc-other"] = &npcstore.NPCDefinition{ID: "npc-other", CampaignID: "c-other", Name: "Cross-Tenant"}

	req := authReq(t, http.MethodPost, "/api/v1/campaigns/c1/npcs/npc-other/link",
		nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (cross-tenant NPC)", rr.Code, http.StatusNotFound)
	}
}

func TestHandleListLinkedNPCs_CampaignNotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/nonexistent/linked-npcs",
		nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleListLinkedNPCs_Empty(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Campaign A"}
	// No links seeded.

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/c1/linked-npcs",
		nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 0 {
		t.Errorf("got %d linked NPCs, want 0", len(resp.Data))
	}
}

func TestHandleListLinkedNPCs_UnresolvedNPC(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c2"] = &Campaign{ID: "c2", TenantID: "tenant-1", Name: "Campaign B"}

	// Link references an NPC that no longer exists.
	ws.campaignNPCLinks["c2"] = []CampaignNPCLink{
		{CampaignID: "c2", NPCID: "deleted-npc"},
	}

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/c2/linked-npcs",
		nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Data []struct {
			NPCID string                  `json:"npc_id"`
			NPC   *npcstore.NPCDefinition `json:"npc"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("got %d items, want 1", len(resp.Data))
	}
	if resp.Data[0].NPC != nil {
		t.Error("NPC should be nil for deleted/missing NPC")
	}
}

func TestHandleUnlinkNPCFromCampaign_CampaignNotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	req := authReq(t, http.MethodDelete, "/api/v1/campaigns/nonexistent/npcs/npc-1/link",
		nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleUnlinkNPCFromCampaign_NotLinked(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c2"] = &Campaign{ID: "c2", TenantID: "tenant-1", Name: "Campaign B"}
	// No links seeded.

	req := authReq(t, http.MethodDelete, "/api/v1/campaigns/c2/npcs/npc-1/link",
		nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}
