package gateway_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/gateway"
)

const testAPIKey = "test-secret-key"

func newTestAdminAPI(t *testing.T) (*gateway.AdminAPI, *gateway.MemAdminStore) {
	t.Helper()
	store := gateway.NewMemAdminStore()
	api := gateway.NewAdminAPI(store, testAPIKey, nil)
	return api, store
}

func doRequest(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestAdminAPI_AuthMiddleware(t *testing.T) {
	t.Parallel()

	api, _ := newTestAdminAPI(t)
	handler := api.Handler()

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{"no auth header", "", http.StatusUnauthorized},
		{"wrong key", "Bearer wrong-key", http.StatusForbidden},
		{"bare wrong key", "wrong-key", http.StatusForbidden},
		{"valid bearer", "Bearer " + testAPIKey, http.StatusOK},
		{"valid bare", testAPIKey, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("GET", "/api/v1/tenants", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", rr.Code, tt.wantStatus)
			}
		})
	}
}

func TestAdminAPI_CreateTenant(t *testing.T) {
	t.Parallel()

	api, _ := newTestAdminAPI(t)
	handler := api.Handler()

	tests := []struct {
		name       string
		body       gateway.TenantCreateRequest
		wantStatus int
	}{
		{
			name:       "valid shared tenant",
			body:       gateway.TenantCreateRequest{ID: "acme", LicenseTier: "shared"},
			wantStatus: http.StatusCreated,
		},
		{
			name:       "valid dedicated tenant",
			body:       gateway.TenantCreateRequest{ID: "bigcorp", LicenseTier: "dedicated", BotToken: "tok"},
			wantStatus: http.StatusCreated,
		},
		{
			name:       "missing id",
			body:       gateway.TenantCreateRequest{LicenseTier: "shared"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid tier",
			body:       gateway.TenantCreateRequest{ID: "badtier", LicenseTier: "enterprise"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid tenant id format",
			body:       gateway.TenantCreateRequest{ID: "UPPERCASE", LicenseTier: "shared"},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: shared store state for duplicate test below.
			rr := doRequest(t, handler, "POST", "/api/v1/tenants", tt.body)
			if rr.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d; body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}

			// Verify bot token is not in response.
			if rr.Code == http.StatusCreated && tt.body.BotToken != "" {
				body := rr.Body.String()
				if bytes.Contains([]byte(body), []byte(tt.body.BotToken)) {
					t.Error("response should not contain bot token")
				}
			}
		})
	}

	// Duplicate creation.
	t.Run("duplicate", func(t *testing.T) {
		rr := doRequest(t, handler, "POST", "/api/v1/tenants",
			gateway.TenantCreateRequest{ID: "acme", LicenseTier: "shared"})
		if rr.Code != http.StatusConflict {
			t.Errorf("got status %d, want %d", rr.Code, http.StatusConflict)
		}
	})
}

func TestAdminAPI_GetTenant(t *testing.T) {
	t.Parallel()

	api, _ := newTestAdminAPI(t)
	handler := api.Handler()

	// Create a tenant first.
	doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{ID: "getme", LicenseTier: "shared"})

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		rr := doRequest(t, handler, "GET", "/api/v1/tenants/getme", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200", rr.Code)
		}
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		rr := doRequest(t, handler, "GET", "/api/v1/tenants/nonexistent", nil)
		if rr.Code != http.StatusNotFound {
			t.Errorf("got status %d, want 404", rr.Code)
		}
	})
}

func TestAdminAPI_ListTenants(t *testing.T) {
	t.Parallel()

	api, _ := newTestAdminAPI(t)
	handler := api.Handler()

	// Empty list.
	rr := doRequest(t, handler, "GET", "/api/v1/tenants", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rr.Code)
	}

	// Create two tenants.
	doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{ID: "alpha", LicenseTier: "shared"})
	doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{ID: "beta", LicenseTier: "dedicated"})

	rr = doRequest(t, handler, "GET", "/api/v1/tenants", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rr.Code)
	}

	var tenants []json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &tenants); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(tenants) != 2 {
		t.Errorf("got %d tenants, want 2", len(tenants))
	}
}

func TestAdminAPI_UpdateTenant(t *testing.T) {
	t.Parallel()

	api, _ := newTestAdminAPI(t)
	handler := api.Handler()

	doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{ID: "updateme", LicenseTier: "shared"})

	t.Run("update tier", func(t *testing.T) {
		rr := doRequest(t, handler, "PUT", "/api/v1/tenants/updateme",
			gateway.TenantUpdateRequest{LicenseTier: "dedicated"})
		if rr.Code != http.StatusOK {
			t.Errorf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		rr := doRequest(t, handler, "PUT", "/api/v1/tenants/nonexistent",
			gateway.TenantUpdateRequest{LicenseTier: "shared"})
		if rr.Code != http.StatusNotFound {
			t.Errorf("got status %d, want 404", rr.Code)
		}
	})
}

func TestAdminAPI_DeleteTenant(t *testing.T) {
	t.Parallel()

	api, _ := newTestAdminAPI(t)
	handler := api.Handler()

	doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{ID: "deleteme", LicenseTier: "shared"})

	t.Run("success", func(t *testing.T) {
		rr := doRequest(t, handler, "DELETE", "/api/v1/tenants/deleteme", nil)
		if rr.Code != http.StatusNoContent {
			t.Errorf("got status %d, want 204", rr.Code)
		}
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		rr := doRequest(t, handler, "DELETE", "/api/v1/tenants/nonexistent", nil)
		if rr.Code != http.StatusNotFound {
			t.Errorf("got status %d, want 404", rr.Code)
		}
	})
}
