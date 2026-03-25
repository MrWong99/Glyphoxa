package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleOnboardingComplete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tenantID string // JWT tenant_id (empty means user needs onboarding)
		body     string
		wantCode int
	}{
		{
			name:     "valid onboarding",
			tenantID: "",
			body:     `{"tenant_id":"new-tenant","display_name":"My Org"}`,
			wantCode: http.StatusCreated,
		},
		{
			name:     "already onboarded",
			tenantID: "existing-tenant",
			body:     `{"tenant_id":"new-tenant"}`,
			wantCode: http.StatusConflict,
		},
		{
			name:     "missing tenant_id",
			tenantID: "",
			body:     `{"display_name":"NoTenant"}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "invalid tenant_id format",
			tenantID: "",
			body:     `{"tenant_id":"bad tenant!@#"}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "invalid json",
			tenantID: "",
			body:     `{bad`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "valid with default display_name",
			tenantID: "",
			body:     `{"tenant_id":"auto-name"}`,
			wantCode: http.StatusCreated,
		},
		{
			name:     "valid with license_tier",
			tenantID: "",
			body:     `{"tenant_id":"tier-test","license_tier":"dedicated"}`,
			wantCode: http.StatusCreated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, ws, _, secret := testServerWithStores(t)
			auth := AuthMiddleware(secret)
			srv.mux.Handle("POST /api/v1/onboarding/complete", auth(http.HandlerFunc(srv.handleOnboardingComplete)))

			// Seed user so UpdateUserTenant can find it.
			ws.users["user-1"] = &User{ID: "user-1", TenantID: tt.tenantID, DisplayName: "Alice", Role: "dm"}

			req := authReq(t, http.MethodPost, "/api/v1/onboarding/complete",
				bytes.NewBufferString(tt.body), secret, "user-1", tt.tenantID, "dm")
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}

			if tt.wantCode == http.StatusCreated {
				var resp struct {
					Data struct {
						AccessToken string `json:"access_token"`
						TokenType   string `json:"token_type"`
						ExpiresIn   int    `json:"expires_in"`
						TenantID    string `json:"tenant_id"`
					} `json:"data"`
				}
				if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if resp.Data.AccessToken == "" {
					t.Error("expected non-empty access_token")
				}
				if resp.Data.TokenType != "Bearer" {
					t.Errorf("token_type = %q, want %q", resp.Data.TokenType, "Bearer")
				}
			}
		})
	}
}

func TestHandleOnboardingComplete_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/onboarding/complete", auth(http.HandlerFunc(srv.handleOnboardingComplete)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/onboarding/complete",
		bytes.NewBufferString(`{"tenant_id":"test"}`))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleOnboardingComplete_WithGateway(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient()

	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/onboarding/complete", auth(http.HandlerFunc(srv.handleOnboardingComplete)))

	ws.users["user-1"] = &User{ID: "user-1", TenantID: "", DisplayName: "Alice", Role: "dm"}

	req := authReq(t, http.MethodPost, "/api/v1/onboarding/complete",
		bytes.NewBufferString(`{"tenant_id":"gw-onboard"}`), secret, "user-1", "", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}
}

func TestHandleOnboardingComplete_GatewayDuplicate(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	srv.gwClient = newMockMgmtClient() // t1 already exists in mock

	auth := AuthMiddleware(secret)
	srv.mux.Handle("POST /api/v1/onboarding/complete", auth(http.HandlerFunc(srv.handleOnboardingComplete)))

	ws.users["user-1"] = &User{ID: "user-1", TenantID: "", DisplayName: "Alice", Role: "dm"}

	// Attempting to create an existing tenant should fail.
	req := authReq(t, http.MethodPost, "/api/v1/onboarding/complete",
		bytes.NewBufferString(`{"tenant_id":"t1"}`), secret, "user-1", "", "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d (duplicate tenant via gateway); body: %s", rr.Code, http.StatusConflict, rr.Body.String())
	}
}

func TestHandleValidateInvite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		query    string
		seedInv  bool
		wantCode int
	}{
		{
			name:     "valid invite",
			query:    "?token=VALID_TOKEN",
			seedInv:  true,
			wantCode: http.StatusOK,
		},
		{
			name:     "missing token",
			query:    "",
			seedInv:  false,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "unknown token",
			query:    "?token=nonexistent",
			seedInv:  false,
			wantCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, ws, _, _ := testServerWithStores(t)
			srv.mux.HandleFunc("GET /api/v1/invites/validate", srv.handleValidateInvite)

			if tt.seedInv {
				ws.invites["inv-1"] = &Invite{
					ID:       "inv-1",
					TenantID: "tenant-1",
					Role:     "viewer",
					Token:    "VALID_TOKEN",
				}
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/invites/validate"+tt.query, nil)
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}

			if tt.wantCode == http.StatusOK {
				var resp struct {
					Data struct {
						Valid    bool   `json:"valid"`
						Role     string `json:"role"`
						TenantID string `json:"tenant_id"`
					} `json:"data"`
				}
				if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if !resp.Data.Valid {
					t.Error("expected valid = true")
				}
				if resp.Data.TenantID != "tenant-1" {
					t.Errorf("tenant_id = %q, want %q", resp.Data.TenantID, "tenant-1")
				}
			}
		})
	}
}
