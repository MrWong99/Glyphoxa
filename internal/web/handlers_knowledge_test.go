package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandleListKnowledgeEntities(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ws.knowledgeEntities["c1"] = []KnowledgeEntity{
		{CampaignID: "c1", ID: "e1", Type: "person", Name: "Greymantle", CreatedAt: time.Now().UTC()},
		{CampaignID: "c1", ID: "e2", Type: "location", Name: "Rabenheim", CreatedAt: time.Now().UTC()},
	}

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/c1/knowledge", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Data       []KnowledgeEntity `json:"data"`
		Pagination PageMeta          `json:"pagination"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Errorf("got %d entities, want 2", len(resp.Data))
	}
}

func TestHandleDeleteKnowledgeEntity(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ws.knowledgeEntities["c1"] = []KnowledgeEntity{
		{CampaignID: "c1", ID: "e1", Type: "person", Name: "ToDelete"},
	}

	req := authReq(t, http.MethodDelete, "/api/v1/campaigns/c1/knowledge/e1", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusNoContent, rr.Body.String())
	}

	if len(ws.knowledgeEntities["c1"]) != 0 {
		t.Error("entity should have been deleted")
	}
}

func TestHandleDeleteKnowledgeEntity_NotFound(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	// No entities seeded.

	req := authReq(t, http.MethodDelete, "/api/v1/campaigns/c1/knowledge/nonexistent", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleRebuildKnowledgeGraph(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	req := authReq(t, http.MethodPost, "/api/v1/campaigns/c1/knowledge/rebuild", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusAccepted, rr.Body.String())
	}

	var resp struct {
		Data struct {
			Status     string `json:"status"`
			CampaignID string `json:"campaign_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Status != "queued" {
		t.Errorf("status = %q, want %q", resp.Data.Status, "queued")
	}
	if resp.Data.CampaignID != "c1" {
		t.Errorf("campaign_id = %q, want %q", resp.Data.CampaignID, "c1")
	}
}

func TestHandleKnowledge_WrongCampaign(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	// Campaign c1 belongs to tenant-1; authenticate as tenant-2.
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/c1/knowledge", nil, secret, "user-2", "tenant-2", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (wrong tenant)", rr.Code, http.StatusNotFound)
	}
}
