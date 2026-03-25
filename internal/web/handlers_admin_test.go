package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleAdminDashboardStats(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/admin/stats", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleAdminDashboardStats))))

	userID := "admin-1"
	tenantID := "default"
	ws.users[userID] = &User{ID: userID, TenantID: tenantID, DisplayName: "Admin", Role: "super_admin"}
	ws.campaigns["camp-1"] = &Campaign{ID: "camp-1", TenantID: tenantID, Name: "Test Campaign"}

	t.Run("returns stats for super_admin", func(t *testing.T) {
		t.Parallel()

		req := authReq(t, http.MethodGet, "/api/v1/admin/stats", nil, secret, userID, tenantID, "super_admin")
		rr := httptest.NewRecorder()
		srv.mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body = %s", rr.Code, http.StatusOK, rr.Body.String())
		}

		var resp struct {
			Data AdminDashboardStats `json:"data"`
		}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Data.TotalUsers != 1 {
			t.Errorf("total_users = %d, want 1", resp.Data.TotalUsers)
		}
		if resp.Data.TotalCampaigns != 1 {
			t.Errorf("total_campaigns = %d, want 1", resp.Data.TotalCampaigns)
		}
	})

	t.Run("requires super_admin role", func(t *testing.T) {
		t.Parallel()

		req := authReq(t, http.MethodGet, "/api/v1/admin/stats", nil, secret, userID, tenantID, "tenant_admin")
		rr := httptest.NewRecorder()
		srv.mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
		}
	})
}

func TestHandleAdminListUsers(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/admin/users", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleAdminListUsers))))

	ws.users["u1"] = &User{ID: "u1", TenantID: "t1", DisplayName: "User1", Role: "dm"}
	ws.users["u2"] = &User{ID: "u2", TenantID: "t2", DisplayName: "User2", Role: "dm"}
	ws.users["admin"] = &User{ID: "admin", TenantID: "default", DisplayName: "Admin", Role: "super_admin"}

	req := authReq(t, http.MethodGet, "/api/v1/admin/users", nil, secret, "admin", "default", "super_admin")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp struct {
		Data  []User `json:"data"`
		Total int    `json:"total"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 3 {
		t.Errorf("total = %d, want 3", resp.Total)
	}
}

func TestHandleAuthProviders(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := testServerWithStores(t)
	srv.mux.HandleFunc("GET /api/v1/auth/providers", srv.handleAuthProviders)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Data map[string]bool `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Discord is configured in test config.
	if !resp.Data["discord"] {
		t.Error("discord should be true")
	}
	// Google and GitHub are not configured.
	if resp.Data["google"] {
		t.Error("google should be false")
	}
	if resp.Data["github"] {
		t.Error("github should be false")
	}
}
