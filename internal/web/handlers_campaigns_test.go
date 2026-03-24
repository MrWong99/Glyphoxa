package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleCreateCampaign(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		role     string
		wantCode int
	}{
		{
			name:     "valid campaign",
			body:     `{"name":"Rabenheim","system":"D&D 5e","description":"A dark fantasy campaign"}`,
			role:     "dm",
			wantCode: http.StatusCreated,
		},
		{
			name:     "missing name",
			body:     `{"system":"D&D 5e"}`,
			role:     "dm",
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "name too long",
			body:     `{"name":"` + strings.Repeat("x", 256) + `"}`,
			role:     "dm",
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "description too long",
			body:     `{"name":"Test","description":"` + strings.Repeat("x", 4097) + `"}`,
			role:     "dm",
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "invalid json",
			body:     `{not json`,
			role:     "dm",
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "minimal valid",
			body:     `{"name":"Minimal"}`,
			role:     "dm",
			wantCode: http.StatusCreated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, _, _, secret := testServerWithStores(t)
			auth := AuthMiddleware(secret)
			srv.mux.Handle("POST /api/v1/campaigns", auth(RequireRole("dm")(http.HandlerFunc(srv.handleCreateCampaign))))

			req := authReq(t, http.MethodPost, "/api/v1/campaigns", bytes.NewBufferString(tt.body), secret, "user-1", "tenant-1", tt.role)
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}

			if tt.wantCode == http.StatusCreated {
				var body struct {
					Data Campaign `json:"data"`
				}
				if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if body.Data.ID == "" {
					t.Error("expected non-empty campaign ID")
				}
				if body.Data.TenantID != "tenant-1" {
					t.Errorf("tenant_id = %q, want %q", body.Data.TenantID, "tenant-1")
				}
			}
		})
	}
}

func TestHandleCreateCampaign_InsufficientRole(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/campaigns", auth(RequireRole("dm")(http.HandlerFunc(srv.handleCreateCampaign))))

	req := authReq(t, http.MethodPost, "/api/v1/campaigns",
		bytes.NewBufferString(`{"name":"Test"}`),
		secret, "user-1", "tenant-1", "viewer")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestHandleListCampaigns(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/campaigns", auth(http.HandlerFunc(srv.handleListCampaigns)))

	// Seed campaigns for two tenants.
	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Campaign A"}
	ws.campaigns["c2"] = &Campaign{ID: "c2", TenantID: "tenant-1", Name: "Campaign B"}
	ws.campaigns["c3"] = &Campaign{ID: "c3", TenantID: "tenant-2", Name: "Other Tenant"}

	req := authReq(t, http.MethodGet, "/api/v1/campaigns", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Data []Campaign `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 2 {
		t.Errorf("got %d campaigns, want 2", len(body.Data))
	}
	for _, c := range body.Data {
		if c.TenantID != "tenant-1" {
			t.Errorf("campaign %q has tenant_id %q, want %q", c.ID, c.TenantID, "tenant-1")
		}
	}
}

func TestHandleListCampaigns_Empty(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/campaigns", auth(http.HandlerFunc(srv.handleListCampaigns)))

	req := authReq(t, http.MethodGet, "/api/v1/campaigns", nil, secret, "user-1", "tenant-1", "viewer")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Data []Campaign `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data == nil {
		t.Error("data should be empty array, not null")
	}
	if len(body.Data) != 0 {
		t.Errorf("got %d campaigns, want 0", len(body.Data))
	}
}

func TestHandleGetCampaign(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		id       string
		tenantID string
		wantCode int
	}{
		{"found", "c1", "tenant-1", http.StatusOK},
		{"not found", "c999", "tenant-1", http.StatusNotFound},
		{"wrong tenant", "c1", "tenant-2", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, ws, _, secret := testServerWithStores(t)
			auth := AuthMiddleware(secret)
			srv.mux.Handle("GET /api/v1/campaigns/{id}", auth(http.HandlerFunc(srv.handleGetCampaign)))
			ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Found"}

			req := authReq(t, http.MethodGet, "/api/v1/campaigns/"+tt.id, nil, secret, "user-1", tt.tenantID, "dm")
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantCode)
			}
		})
	}
}

func TestHandleUpdateCampaign(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		id       string
		body     string
		wantCode int
		wantName string
	}{
		{
			name:     "partial update name only",
			id:       "c1",
			body:     `{"name":"Updated"}`,
			wantCode: http.StatusOK,
			wantName: "Updated",
		},
		{
			name:     "not found",
			id:       "c999",
			body:     `{"name":"X"}`,
			wantCode: http.StatusNotFound,
		},
		{
			name:     "invalid json",
			id:       "c1",
			body:     `{bad}`,
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, ws, _, secret := testServerWithStores(t)
			auth := AuthMiddleware(secret)
			srv.mux.Handle("PUT /api/v1/campaigns/{id}", auth(RequireRole("dm")(http.HandlerFunc(srv.handleUpdateCampaign))))

			ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Original", System: "D&D 5e", Description: "Original desc"}

			req := authReq(t, http.MethodPut, "/api/v1/campaigns/"+tt.id,
				bytes.NewBufferString(tt.body), secret, "user-1", "tenant-1", "dm")
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}

			if tt.wantCode == http.StatusOK && tt.wantName != "" {
				var body struct {
					Data Campaign `json:"data"`
				}
				if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if body.Data.Name != tt.wantName {
					t.Errorf("name = %q, want %q", body.Data.Name, tt.wantName)
				}
			}
		})
	}
}

func TestHandleUpdateCampaign_PartialFieldPreservation(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("PUT /api/v1/campaigns/{id}", auth(RequireRole("dm")(http.HandlerFunc(srv.handleUpdateCampaign))))

	ws.campaigns["c1"] = &Campaign{
		ID:          "c1",
		TenantID:    "tenant-1",
		Name:        "Original",
		System:      "D&D 5e",
		Description: "Keep this",
	}

	// Only update system, name and description should remain.
	body := `{"system":"Pathfinder 2e"}`
	req := authReq(t, http.MethodPut, "/api/v1/campaigns/c1",
		bytes.NewBufferString(body), secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Data Campaign `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Name != "Original" {
		t.Errorf("name = %q, want %q (should be preserved)", resp.Data.Name, "Original")
	}
	if resp.Data.System != "Pathfinder 2e" {
		t.Errorf("system = %q, want %q", resp.Data.System, "Pathfinder 2e")
	}
	if resp.Data.Description != "Keep this" {
		t.Errorf("description = %q, want %q (should be preserved)", resp.Data.Description, "Keep this")
	}
}

func TestHandleDeleteCampaign(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("DELETE /api/v1/campaigns/{id}", auth(RequireRole("dm")(http.HandlerFunc(srv.handleDeleteCampaign))))

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "ToDelete"}

	req := authReq(t, http.MethodDelete, "/api/v1/campaigns/c1", nil, secret, "user-1", "tenant-1", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}

	// Verify it's gone from the store.
	if _, ok := ws.campaigns["c1"]; ok {
		t.Error("campaign should have been deleted from store")
	}
}

func TestHandleDeleteCampaign_InsufficientRole(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("DELETE /api/v1/campaigns/{id}", auth(RequireRole("dm")(http.HandlerFunc(srv.handleDeleteCampaign))))

	ws.campaigns["c1"] = &Campaign{ID: "c1", TenantID: "tenant-1", Name: "Protected"}

	req := authReq(t, http.MethodDelete, "/api/v1/campaigns/c1", nil, secret, "user-1", "tenant-1", "viewer")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}
