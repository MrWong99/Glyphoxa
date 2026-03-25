package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleCreateLoreDocument(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	body := `{"title":"History of Rabenheim","content_markdown":"# Chapter 1\nDark times...","sort_order":1}`
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
	if resp.Data.ID == "" {
		t.Error("expected non-empty lore document ID")
	}
	if resp.Data.Title != "History of Rabenheim" {
		t.Errorf("title = %q, want %q", resp.Data.Title, "History of Rabenheim")
	}
	if resp.Data.SortOrder != 1 {
		t.Errorf("sort_order = %d, want 1", resp.Data.SortOrder)
	}
}

func TestHandleCreateLoreDocument_RequiresTitle(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	body := `{"content_markdown":"no title here"}`
	req := authReq(t, http.MethodPost, "/api/v1/campaigns/c1/lore",
		bytes.NewBufferString(body), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleListLoreDocuments(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	ws.loreDocs["l1"] = &LoreDocument{ID: "l1", CampaignID: "c1", Title: "Doc A", SortOrder: 0}
	ws.loreDocs["l2"] = &LoreDocument{ID: "l2", CampaignID: "c1", Title: "Doc B", SortOrder: 1}
	ws.loreDocs["l3"] = &LoreDocument{ID: "l3", CampaignID: "c2", Title: "Other Campaign"}

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/c1/lore", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Data []LoreDocument `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Errorf("got %d lore docs, want 2", len(resp.Data))
	}
}

func TestHandleGetLoreDocument(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ws.loreDocs["l1"] = &LoreDocument{ID: "l1", CampaignID: "c1", Title: "Found"}

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/c1/lore/l1", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Data LoreDocument `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Title != "Found" {
		t.Errorf("title = %q, want %q", resp.Data.Title, "Found")
	}
}

func TestHandleGetLoreDocument_NotFound(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/c1/lore/nonexistent", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleUpdateLoreDocument(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ws.loreDocs["l1"] = &LoreDocument{ID: "l1", CampaignID: "c1", Title: "Original", ContentMarkdown: "Keep this", SortOrder: 0}

	body := `{"title":"Updated"}`
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
	if resp.Data.Title != "Updated" {
		t.Errorf("title = %q, want %q", resp.Data.Title, "Updated")
	}
	if resp.Data.ContentMarkdown != "Keep this" {
		t.Errorf("content_markdown = %q, want %q (should be preserved)", resp.Data.ContentMarkdown, "Keep this")
	}
}

func TestHandleDeleteLoreDocument(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ws.loreDocs["l1"] = &LoreDocument{ID: "l1", CampaignID: "c1", Title: "ToDelete"}

	req := authReq(t, http.MethodDelete, "/api/v1/campaigns/c1/lore/l1", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}

	if _, ok := ws.loreDocs["l1"]; ok {
		t.Error("lore document should have been deleted from store")
	}
}

func TestHandleCreateLoreDocument_InvalidJSON(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	req := authReq(t, http.MethodPost, "/api/v1/campaigns/c1/lore",
		bytes.NewBufferString(`{bad json`), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleCreateLoreDocument_CampaignNotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	req := authReq(t, http.MethodPost, "/api/v1/campaigns/nonexistent/lore",
		bytes.NewBufferString(`{"title":"Ghost"}`), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleCreateLoreDocument_ContentAlias(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	// Use "content" instead of "content_markdown".
	body := `{"title":"Alias Test","content":"# Content via alias"}`
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
	if resp.Data.ContentMarkdown != "# Content via alias" {
		t.Errorf("content_markdown = %q, want %q", resp.Data.ContentMarkdown, "# Content via alias")
	}
}

func TestHandleListLoreDocuments_CampaignNotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/nonexistent/lore", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleListLoreDocuments_Empty(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/c1/lore", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Data []LoreDocument `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data == nil {
		t.Error("data should be empty array, not null")
	}
}

func TestHandleUpdateLoreDocument_NotFound(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	req := authReq(t, http.MethodPut, "/api/v1/campaigns/c1/lore/nonexistent",
		bytes.NewBufferString(`{"title":"X"}`), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleUpdateLoreDocument_InvalidJSON(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ws.loreDocs["l1"] = &LoreDocument{ID: "l1", CampaignID: "c1", Title: "Original"}

	req := authReq(t, http.MethodPut, "/api/v1/campaigns/c1/lore/l1",
		bytes.NewBufferString(`{bad`), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleUpdateLoreDocument_CampaignNotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	req := authReq(t, http.MethodPut, "/api/v1/campaigns/nonexistent/lore/l1",
		bytes.NewBufferString(`{"title":"X"}`), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleDeleteLoreDocument_NotFound(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}

	req := authReq(t, http.MethodDelete, "/api/v1/campaigns/c1/lore/nonexistent", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleDeleteLoreDocument_CampaignNotFound(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	req := authReq(t, http.MethodDelete, "/api/v1/campaigns/nonexistent/lore/l1", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleUpdateLoreDocument_PartialFieldPreservation(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ws.loreDocs["l1"] = &LoreDocument{
		ID:              "l1",
		CampaignID:      "c1",
		Title:           "Original Title",
		ContentMarkdown: "Original Content",
		SortOrder:       5,
	}

	// Only update sort_order.
	intPtr := func(i int) *int { return &i }
	_ = intPtr
	body := `{"sort_order":10}`
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
	if resp.Data.Title != "Original Title" {
		t.Errorf("title = %q, want %q (should be preserved)", resp.Data.Title, "Original Title")
	}
	if resp.Data.ContentMarkdown != "Original Content" {
		t.Errorf("content = %q, want %q (should be preserved)", resp.Data.ContentMarkdown, "Original Content")
	}
	if resp.Data.SortOrder != 10 {
		t.Errorf("sort_order = %d, want 10", resp.Data.SortOrder)
	}
}

func TestHandleLoreDocument_WrongCampaign(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.registerRoutes()
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "TestCampaign"}
	ws.loreDocs["l1"] = &LoreDocument{ID: "l1", CampaignID: "c2", Title: "Wrong Campaign"}

	req := authReq(t, http.MethodGet, "/api/v1/campaigns/c1/lore/l1", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}
