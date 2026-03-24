package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTenantHandlers_NoGateway(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"list tenants", http.MethodGet, "/api/v1/tenants", ""},
		{"get tenant", http.MethodGet, "/api/v1/tenants/t1", ""},
		{"create tenant", http.MethodPost, "/api/v1/tenants", `{"id":"t1"}`},
		{"update tenant", http.MethodPut, "/api/v1/tenants/t1", `{"display_name":"New"}`},
		{"delete tenant", http.MethodDelete, "/api/v1/tenants/t1", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, _, _, secret := testServerWithStores(t)
			// No GatewayURL configured.
			auth := AuthMiddleware(secret)

			// Register all tenant routes.
			srv.mux.Handle("GET /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleListTenants))))
			srv.mux.Handle("GET /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleGetTenant))))
			srv.mux.Handle("POST /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleCreateTenant))))
			srv.mux.Handle("PUT /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleUpdateTenant))))
			srv.mux.Handle("DELETE /api/v1/tenants/{id}", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleDeleteTenant))))

			var body io.Reader
			if tt.body != "" {
				body = bytes.NewBufferString(tt.body)
			}
			req := httptest.NewRequest(tt.method, tt.path, body)
			token := signTestToken(t, secret, "user-1", "t1", "super_admin")
			req.Header.Set("Authorization", "Bearer "+token)

			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want %d (no gateway configured)", rr.Code, http.StatusServiceUnavailable)
			}
		})
	}
}

func TestTenantHandlers_ProxyToGateway(t *testing.T) {
	t.Parallel()

	// Start a fake gateway server. Use t.Cleanup so it stays alive for parallel subtests.
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/tenants":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "t1"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/tenants/t1":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"id": "t1"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/tenants":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"id": "new-t"}})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/tenants/t1":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"id": "t1"}})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/tenants/t1":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(gateway.Close)

	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		wantCode int
	}{
		{"list tenants", http.MethodGet, "/api/v1/tenants", "", http.StatusOK},
		{"get tenant", http.MethodGet, "/api/v1/tenants/t1", "", http.StatusOK},
		{"create tenant", http.MethodPost, "/api/v1/tenants", `{"id":"new-t"}`, http.StatusCreated},
		{"update tenant", http.MethodPut, "/api/v1/tenants/t1", `{"display_name":"Upd"}`, http.StatusOK},
		{"delete tenant", http.MethodDelete, "/api/v1/tenants/t1", "", http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, _, _, secret := testServerWithStores(t)
			srv.cfg.GatewayURL = gateway.URL
			srv.gatewayHC = &http.Client{Timeout: 5 * time.Second}

			auth := AuthMiddleware(secret)
			srv.mux.Handle("GET /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleListTenants))))
			srv.mux.Handle("GET /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleGetTenant))))
			srv.mux.Handle("POST /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleCreateTenant))))
			srv.mux.Handle("PUT /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleUpdateTenant))))
			srv.mux.Handle("DELETE /api/v1/tenants/{id}", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleDeleteTenant))))

			var body io.Reader
			if tt.body != "" {
				body = bytes.NewBufferString(tt.body)
			}
			req := httptest.NewRequest(tt.method, tt.path, body)
			token := signTestToken(t, secret, "user-1", "t1", "super_admin")
			req.Header.Set("Authorization", "Bearer "+token)

			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}
		})
	}
}

func TestHandleGetTenant_TenantIsolation(t *testing.T) {
	t.Parallel()

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"id": "t1"}})
	}))
	t.Cleanup(gateway.Close)

	tests := []struct {
		name     string
		userTID  string
		role     string
		targetID string
		wantCode int
	}{
		{"tenant_admin own tenant", "t1", "tenant_admin", "t1", http.StatusOK},
		{"tenant_admin other tenant", "t1", "tenant_admin", "t2", http.StatusForbidden},
		{"super_admin any tenant", "t1", "super_admin", "t2", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, _, _, secret := testServerWithStores(t)
			srv.cfg.GatewayURL = gateway.URL
			srv.gatewayHC = &http.Client{Timeout: 5 * time.Second}

			auth := AuthMiddleware(secret)
			srv.mux.Handle("GET /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleGetTenant))))

			req := authReq(t, http.MethodGet, "/api/v1/tenants/"+tt.targetID, nil, secret, "user-1", tt.userTID, tt.role)
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantCode)
			}
		})
	}
}

func TestHandleUpdateTenant_TenantIsolation(t *testing.T) {
	t.Parallel()

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"id": "t1"}})
	}))
	t.Cleanup(gateway.Close)

	tests := []struct {
		name     string
		userTID  string
		role     string
		targetID string
		wantCode int
	}{
		{"tenant_admin own tenant", "t1", "tenant_admin", "t1", http.StatusOK},
		{"tenant_admin other tenant", "t1", "tenant_admin", "t2", http.StatusForbidden},
		{"super_admin any tenant", "t1", "super_admin", "t2", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, _, _, secret := testServerWithStores(t)
			srv.cfg.GatewayURL = gateway.URL
			srv.gatewayHC = &http.Client{Timeout: 5 * time.Second}

			auth := AuthMiddleware(secret)
			srv.mux.Handle("PUT /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleUpdateTenant))))

			req := authReq(t, http.MethodPut, "/api/v1/tenants/"+tt.targetID,
				bytes.NewBufferString(`{"display_name":"Test"}`), secret, "user-1", tt.userTID, tt.role)
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantCode)
			}
		})
	}
}

func TestHandleCreateTenantSelfService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{
			name:     "valid creation",
			body:     `{"id":"new-tenant","display_name":"New Tenant"}`,
			wantCode: http.StatusCreated,
		},
		{
			name:     "missing id",
			body:     `{"display_name":"NoID"}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "invalid json",
			body:     `{bad`,
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, _, _, secret := testServerWithStores(t)
			// No GatewayURL — self-service should still work, just skip the gateway proxy.
			auth := AuthMiddleware(secret)
			srv.mux.Handle("POST /api/v1/tenants/self-service", auth(http.HandlerFunc(srv.handleCreateTenantSelfService)))

			req := authReq(t, http.MethodPost, "/api/v1/tenants/self-service",
				bytes.NewBufferString(tt.body), secret, "user-1", "default", "dm")
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}
		})
	}
}

func TestHandleCreateTenantSelfService_WithGateway(t *testing.T) {
	t.Parallel()

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/tenants" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(gateway.Close)

	srv, _, _, secret := testServerWithStores(t)
	srv.cfg.GatewayURL = gateway.URL

	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/tenants/self-service", auth(http.HandlerFunc(srv.handleCreateTenantSelfService)))

	req := authReq(t, http.MethodPost, "/api/v1/tenants/self-service",
		bytes.NewBufferString(`{"id":"gw-tenant","display_name":"Gateway Tenant"}`), secret, "user-1", "default", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusCreated)
	}

	var body struct {
		Data struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.ID != "gw-tenant" {
		t.Errorf("id = %q, want %q", body.Data.ID, "gw-tenant")
	}
}

func TestTenantHandlers_InsufficientRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		path   string
		role   string
	}{
		{"list requires super_admin", http.MethodGet, "/api/v1/tenants", "tenant_admin"},
		{"create requires super_admin", http.MethodPost, "/api/v1/tenants", "tenant_admin"},
		{"delete requires super_admin", http.MethodDelete, "/api/v1/tenants/t1", "tenant_admin"},
		{"get requires tenant_admin", http.MethodGet, "/api/v1/tenants/t1", "dm"},
		{"update requires tenant_admin", http.MethodPut, "/api/v1/tenants/t1", "dm"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, _, _, secret := testServerWithStores(t)
			auth := AuthMiddleware(secret)

			srv.mux.Handle("GET /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleListTenants))))
			srv.mux.Handle("GET /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleGetTenant))))
			srv.mux.Handle("POST /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleCreateTenant))))
			srv.mux.Handle("PUT /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleUpdateTenant))))
			srv.mux.Handle("DELETE /api/v1/tenants/{id}", auth(RequireRole("super_admin")(http.HandlerFunc(srv.handleDeleteTenant))))

			// For POST/PUT/DELETE, use the correct method.
			req := httptest.NewRequest(tt.method, tt.path, nil)
			token := signTestToken(t, secret, "user-1", "t1", tt.role)
			req.Header.Set("Authorization", "Bearer "+token)

			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusForbidden {
				t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
			}
		})
	}
}
