package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandleListAuditLogs(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/audit-logs", auth(RequireRole("tenant_admin")(http.HandlerFunc(srv.handleListAuditLogs))))

	tenantID := "test-tenant"
	userID := "user-1"

	// Seed some audit log entries.
	ws.auditLogs = append(ws.auditLogs,
		AuditLogEntry{
			ID:           1,
			TenantID:     &tenantID,
			UserID:       &userID,
			Action:       "campaign.create",
			ResourceType: "campaign",
			ResourceID:   "camp-1",
			CreatedAt:    time.Now().UTC(),
		},
		AuditLogEntry{
			ID:           2,
			TenantID:     &tenantID,
			UserID:       &userID,
			Action:       "npc.update",
			ResourceType: "npc",
			ResourceID:   "npc-1",
			CreatedAt:    time.Now().UTC(),
		},
	)
	ws.users[userID] = &User{ID: userID, TenantID: tenantID, DisplayName: "Test", Role: "tenant_admin"}

	t.Run("returns audit logs for tenant admin", func(t *testing.T) {
		t.Parallel()

		req := authReq(t, http.MethodGet, "/api/v1/audit-logs", nil, secret, userID, tenantID, "tenant_admin")
		rr := httptest.NewRecorder()
		srv.mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body = %s", rr.Code, http.StatusOK, rr.Body.String())
		}

		var resp struct {
			Data  []AuditLogEntry `json:"data"`
			Total int             `json:"total"`
		}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Total != 2 {
			t.Errorf("total = %d, want 2", resp.Total)
		}
		if len(resp.Data) != 2 {
			t.Errorf("len(data) = %d, want 2", len(resp.Data))
		}
	})

	t.Run("filters by resource type", func(t *testing.T) {
		t.Parallel()

		req := authReq(t, http.MethodGet, "/api/v1/audit-logs?resource_type=campaign", nil, secret, userID, tenantID, "tenant_admin")
		rr := httptest.NewRecorder()
		srv.mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}

		var resp struct {
			Data  []AuditLogEntry `json:"data"`
			Total int             `json:"total"`
		}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Total != 1 {
			t.Errorf("total = %d, want 1", resp.Total)
		}
	})

	t.Run("requires tenant_admin role", func(t *testing.T) {
		t.Parallel()

		req := authReq(t, http.MethodGet, "/api/v1/audit-logs", nil, secret, userID, tenantID, "dm")
		rr := httptest.NewRecorder()
		srv.mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
		}
	})
}

func TestAuditLogHelper(t *testing.T) {
	t.Parallel()

	srv, ws, _, secret := testServerWithStores(t)
	auth := AuthMiddleware(secret)

	// Create a dummy handler that logs an audit entry.
	srv.mux.Handle("POST /api/v1/test-audit", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.auditLog(r, "test.action", "test_resource", "res-123", nil)
		w.WriteHeader(http.StatusOK)
	})))

	userID := "audit-user"
	tenantID := "audit-tenant"
	ws.users[userID] = &User{ID: userID, TenantID: tenantID, DisplayName: "Auditor", Role: "dm"}

	req := authReq(t, http.MethodPost, "/api/v1/test-audit", nil, secret, userID, tenantID, "dm")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	if len(ws.auditLogs) != 1 {
		t.Fatalf("audit log count = %d, want 1", len(ws.auditLogs))
	}

	entry := ws.auditLogs[0]
	if entry.Action != "test.action" {
		t.Errorf("action = %q, want %q", entry.Action, "test.action")
	}
	if entry.ResourceType != "test_resource" {
		t.Errorf("resource_type = %q, want %q", entry.ResourceType, "test_resource")
	}
	if entry.UserID == nil || *entry.UserID != userID {
		t.Errorf("user_id = %v, want %q", entry.UserID, userID)
	}
}
