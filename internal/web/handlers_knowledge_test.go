package web

import (
	"encoding/json"
	"fmt"
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

func TestHandleGetKnowledgeGraph(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ws.knowledgeEntities["c1"] = []KnowledgeEntity{
		{CampaignID: "c1", ID: "e1", Type: "npc", Name: "Heinrich", CreatedAt: time.Now().UTC(), Attributes: map[string]any{"occupation": "guard"}},
		{CampaignID: "c1", ID: "e2", Type: "location", Name: "Rabenheim", CreatedAt: time.Now().UTC(), Attributes: map[string]any{}},
	}

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/c1/knowledge/graph", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp struct {
		Data KnowledgeGraphData `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Entities) != 2 {
		t.Errorf("entities = %d, want 2", len(resp.Data.Entities))
	}
	// No relationships without explicit relationship attributes.
	if resp.Data.Relationships == nil {
		t.Error("relationships should not be nil (should be empty array)")
	}
}

func TestHandleGetKnowledgeGraph_EmptyCampaign(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c2"] = &Campaign{ID: "c2", TenantID: "tenant-1", Name: "EmptyCampaign"}

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/c2/knowledge/graph", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Data KnowledgeGraphData `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Entities) != 0 {
		t.Errorf("entities = %d, want 0", len(resp.Data.Entities))
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

func TestHandleListKnowledgeEntities_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/campaigns/{id}/knowledge", auth(http.HandlerFunc(srv.handleListKnowledgeEntities)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns/c1/knowledge", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleDeleteKnowledgeEntity_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("DELETE /api/v1/campaigns/{id}/knowledge/{entity_id}", auth(http.HandlerFunc(srv.handleDeleteKnowledgeEntity)))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/campaigns/c1/knowledge/e1", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleRebuildKnowledgeGraph_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/campaigns/{id}/knowledge/rebuild", auth(http.HandlerFunc(srv.handleRebuildKnowledgeGraph)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/c1/knowledge/rebuild", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleRebuildKnowledgeGraph_WrongCampaign(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	req := authReq(t, http.MethodPost, "/api/v1/campaigns/c1/knowledge/rebuild", nil, secret, "user-2", "tenant-2", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (wrong tenant)", rr.Code, http.StatusNotFound)
	}
}

func TestHandleDeleteKnowledgeEntity_WrongCampaign(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ws.knowledgeEntities["c1"] = []KnowledgeEntity{
		{CampaignID: "c1", ID: "e1", Type: "person", Name: "Greymantle", CreatedAt: time.Now().UTC()},
	}

	req := authReq(t, http.MethodDelete, "/api/v1/campaigns/c1/knowledge/e1", nil, secret, "user-2", "tenant-2", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (wrong tenant)", rr.Code, http.StatusNotFound)
	}
}

func TestHandleListKnowledgeEntities_Empty(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	// No entities seeded.

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
	if resp.Data == nil {
		t.Error("data should be empty array, not null")
	}
	if len(resp.Data) != 0 {
		t.Errorf("got %d entities, want 0", len(resp.Data))
	}
}

func TestHandleListKnowledgeEntities_NonexistentCampaign(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	// No campaigns seeded.
	req := authReq(t, http.MethodGet, "/api/v1/campaigns/nonexistent/knowledge", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleListKnowledgeEntities_Pagination(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	// Seed more entities than the limit to trigger pagination metadata.
	// Default limit is 25; create 27 entities so has_more is true when limit=2.
	now := time.Now().UTC()
	entities := make([]KnowledgeEntity, 3)
	for i := range entities {
		entities[i] = KnowledgeEntity{
			CampaignID: "c1",
			ID:         fmt.Sprintf("e%d", i+1),
			Type:       "person",
			Name:       fmt.Sprintf("Entity %d", i+1),
			CreatedAt:  now.Add(time.Duration(i) * time.Second),
		}
	}
	ws.knowledgeEntities["c1"] = entities

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/c1/knowledge?limit=2", nil, secret, "user-1", "tenant-1", "dm")
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
	if resp.Pagination.Limit != 2 {
		t.Errorf("pagination.limit = %d, want 2", resp.Pagination.Limit)
	}
	if !resp.Pagination.HasMore {
		t.Error("pagination.has_more should be true when there are more entities than limit")
	}
	if resp.Pagination.NextCursor == "" {
		t.Error("pagination.next_cursor should be set when has_more is true")
	}
	if len(resp.Data) != 2 {
		t.Errorf("got %d entities, want 2 (trimmed to limit)", len(resp.Data))
	}
}
