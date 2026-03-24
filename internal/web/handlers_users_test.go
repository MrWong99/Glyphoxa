package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleListUsers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		role      string
		wantCode  int
		wantCount int
	}{
		{"admin sees users", "tenant_admin", http.StatusOK, 2},
		{"viewer forbidden", "viewer", http.StatusForbidden, 0},
		{"dm forbidden", "dm", http.StatusForbidden, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, ws, _, secret := testServerWithStores(t)
			auth := AuthMiddleware(secret)
			srv.mux.Handle("GET /api/v1/users", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleListUsers))))

			ws.users["u1"] = &User{ID: "u1", TenantID: "tenant-1", DisplayName: "Alice", Role: "dm"}
			ws.users["u2"] = &User{ID: "u2", TenantID: "tenant-1", DisplayName: "Bob", Role: "viewer"}
			ws.users["u3"] = &User{ID: "u3", TenantID: "tenant-2", DisplayName: "Eve", Role: "dm"}

			req := authReq(t, http.MethodGet, "/api/v1/users", nil, secret, "u1", "tenant-1", tt.role)
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}

			if tt.wantCode == http.StatusOK {
				var body struct {
					Data  []User `json:"data"`
					Total int    `json:"total"`
				}
				if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if len(body.Data) != tt.wantCount {
					t.Errorf("got %d users, want %d", len(body.Data), tt.wantCount)
				}
				if body.Total != tt.wantCount {
					t.Errorf("total = %d, want %d", body.Total, tt.wantCount)
				}
			}
		})
	}
}

func TestHandleGetUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		targetID string
		callerID string
		role     string
		wantCode int
	}{
		{"self access", "u1", "u1", "viewer", http.StatusOK},
		{"admin access other", "u2", "u1", "tenant_admin", http.StatusOK},
		{"viewer cannot access other", "u2", "u1", "viewer", http.StatusForbidden},
		{"not found", "u999", "u1", "tenant_admin", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, ws, _, secret := testServerWithStores(t)
			auth := AuthMiddleware(secret)
			srv.mux.Handle("GET /api/v1/users/{id}", auth(http.HandlerFunc(srv.handleGetUser)))

			ws.users["u1"] = &User{ID: "u1", TenantID: "tenant-1", DisplayName: "Alice", Role: "dm"}
			ws.users["u2"] = &User{ID: "u2", TenantID: "tenant-1", DisplayName: "Bob", Role: "viewer"}

			req := authReq(t, http.MethodGet, "/api/v1/users/"+tt.targetID, nil, secret, tt.callerID, "tenant-1", tt.role)
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}
		})
	}
}

func TestHandleUpdateUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		targetID string
		callerID string
		role     string
		body     string
		wantCode int
	}{
		{"admin updates role", "u2", "u1", "tenant_admin", `{"role":"dm"}`, http.StatusOK},
		{"self updates name", "u1", "u1", "dm", `{"display_name":"Alice B"}`, http.StatusOK},
		{"viewer cannot update other", "u2", "u1", "viewer", `{"display_name":"X"}`, http.StatusForbidden},
		{"dm cannot change role", "u1", "u1", "dm", `{"role":"tenant_admin"}`, http.StatusForbidden},
		{"invalid role value", "u2", "u1", "tenant_admin", `{"role":"hacker"}`, http.StatusBadRequest},
		{"invalid json", "u1", "u1", "dm", `{bad}`, http.StatusBadRequest},
		{"cross-tenant update blocked", "u3", "u1", "tenant_admin", `{"role":"dm"}`, http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, ws, _, secret := testServerWithStores(t)
			auth := AuthMiddleware(secret)
			srv.mux.Handle("PUT /api/v1/users/{id}", auth(http.HandlerFunc(srv.handleUpdateUser)))

			ws.users["u1"] = &User{ID: "u1", TenantID: "tenant-1", DisplayName: "Alice", Role: "dm"}
			ws.users["u2"] = &User{ID: "u2", TenantID: "tenant-1", DisplayName: "Bob", Role: "viewer"}
			ws.users["u3"] = &User{ID: "u3", TenantID: "tenant-2", DisplayName: "Eve", Role: "viewer"}

			req := authReq(t, http.MethodPut, "/api/v1/users/"+tt.targetID,
				bytes.NewBufferString(tt.body), secret, tt.callerID, "tenant-1", tt.role)
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}
		})
	}
}

func TestHandleDeleteUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		targetID string
		callerID string
		tenant   string
		wantCode int
	}{
		{"delete other user", "u2", "u1", "tenant-1", http.StatusNoContent},
		{"cannot delete self", "u1", "u1", "tenant-1", http.StatusBadRequest},
		{"delete nonexistent", "u999", "u1", "tenant-1", http.StatusNotFound},
		{"cross-tenant delete blocked", "u3", "u1", "tenant-1", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, ws, _, secret := testServerWithStores(t)
			auth := AuthMiddleware(secret)
			srv.mux.Handle("DELETE /api/v1/users/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleDeleteUser))))

			ws.users["u1"] = &User{ID: "u1", TenantID: "tenant-1", DisplayName: "Alice", Role: "tenant_admin"}
			ws.users["u2"] = &User{ID: "u2", TenantID: "tenant-1", DisplayName: "Bob", Role: "viewer"}
			ws.users["u3"] = &User{ID: "u3", TenantID: "tenant-2", DisplayName: "Eve", Role: "viewer"}

			req := authReq(t, http.MethodDelete, "/api/v1/users/"+tt.targetID, nil, secret, tt.callerID, tt.tenant, "tenant_admin")
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}

			// Verify cross-tenant user was not deleted.
			if tt.name == "cross-tenant delete blocked" {
				if _, ok := ws.users["u3"]; !ok {
					t.Error("cross-tenant user u3 was deleted — tenant isolation breached")
				}
			}
		})
	}
}

func TestHandleCreateInvite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		role     string
		body     string
		wantCode int
	}{
		{"valid invite", "tenant_admin", `{"role":"viewer"}`, http.StatusCreated},
		{"default role", "tenant_admin", `{}`, http.StatusCreated},
		{"invalid role", "tenant_admin", `{"role":"hacker"}`, http.StatusBadRequest},
		{"viewer forbidden", "viewer", `{"role":"viewer"}`, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, _, _, secret := testServerWithStores(t)
			auth := AuthMiddleware(secret)
			srv.mux.Handle("POST /api/v1/users/invite", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleCreateInvite))))

			req := authReq(t, http.MethodPost, "/api/v1/users/invite",
				bytes.NewBufferString(tt.body), secret, "u1", "tenant-1", tt.role)
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}

			if tt.wantCode == http.StatusCreated {
				var body struct {
					Data Invite `json:"data"`
				}
				if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if body.Data.Token == "" {
					t.Error("expected non-empty invite token")
				}
				if body.Data.TenantID != "tenant-1" {
					t.Errorf("tenant_id = %q, want %q", body.Data.TenantID, "tenant-1")
				}
			}
		})
	}
}

func TestHandleUpdateMe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"valid update", `{"display_name":"New Name"}`, http.StatusOK},
		{"empty name", `{"display_name":""}`, http.StatusBadRequest},
		{"invalid json", `{bad}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, ws, _, secret := testServerWithStores(t)
			auth := AuthMiddleware(secret)
			srv.mux.Handle("PUT /api/v1/auth/me", auth(http.HandlerFunc(srv.handleUpdateMe)))

			ws.users["u1"] = &User{ID: "u1", TenantID: "tenant-1", DisplayName: "Old Name", Role: "dm"}

			req := authReq(t, http.MethodPut, "/api/v1/auth/me",
				bytes.NewBufferString(tt.body), secret, "u1", "tenant-1", "dm")
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}

			if tt.wantCode == http.StatusOK {
				var body struct {
					Data User `json:"data"`
				}
				if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if body.Data.DisplayName != "New Name" {
					t.Errorf("display_name = %q, want %q", body.Data.DisplayName, "New Name")
				}
			}
		})
	}
}

func TestHandleUpdatePreferences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"valid preferences", `{"theme":"dark","locale":"en"}`, http.StatusOK},
		{"invalid json", `not json`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, ws, _, secret := testServerWithStores(t)
			auth := AuthMiddleware(secret)
			srv.mux.Handle("PATCH /api/v1/auth/me/preferences", auth(http.HandlerFunc(srv.handleUpdatePreferences)))

			ws.users["u1"] = &User{ID: "u1", TenantID: "tenant-1", DisplayName: "Alice", Role: "dm", Preferences: json.RawMessage(`{}`)}

			req := authReq(t, http.MethodPatch, "/api/v1/auth/me/preferences",
				bytes.NewBufferString(tt.body), secret, "u1", "tenant-1", "dm")
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantCode, rr.Body.String())
			}

			if tt.wantCode == http.StatusOK {
				var body struct {
					Data User `json:"data"`
				}
				if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
					t.Fatalf("decode: %v", err)
				}
				var prefs map[string]any
				if err := json.Unmarshal(body.Data.Preferences, &prefs); err != nil {
					t.Fatalf("unmarshal preferences: %v", err)
				}
				if prefs["theme"] != "dark" {
					t.Errorf("theme = %v, want dark", prefs["theme"])
				}
			}
		})
	}
}
