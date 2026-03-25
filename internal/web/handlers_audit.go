package web

import (
	"log/slog"
	"net/http"
	"strconv"
)

// handleListAuditLogs returns audit log entries for the current tenant,
// or for all tenants if the user is a super_admin.
func (s *Server) handleListAuditLogs(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	tenantID := claims.TenantID
	// Super admins can view all tenants' audit logs.
	if claims.Role == "super_admin" {
		if qTenant := r.URL.Query().Get("tenant_id"); qTenant != "" {
			tenantID = qTenant
		} else {
			tenantID = "" // Empty means all tenants.
		}
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	resourceType := r.URL.Query().Get("resource_type")
	action := r.URL.Query().Get("action")

	entries, total, err := s.store.ListAuditLogs(r.Context(), tenantID, limit, offset, resourceType, action)
	if err != nil {
		slog.Error("web: list audit logs", "tenant_id", tenantID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to load audit logs")
		return
	}

	if entries == nil {
		entries = []AuditLogEntry{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":  entries,
		"total": total,
	})
}

// auditLog is a helper to record an audit log entry from a handler.
func (s *Server) auditLog(r *http.Request, action, resourceType, resourceID string, changes any) {
	claims := ClaimsFromContext(r.Context())
	entry := &AuditLogEntry{
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
	}

	ip := clientIP(r)
	entry.IPAddress = &ip
	ua := r.UserAgent()
	entry.UserAgent = &ua

	if claims != nil {
		entry.UserID = &claims.Sub
		if claims.TenantID != "" {
			entry.TenantID = &claims.TenantID
		}
	}

	if changes != nil {
		if raw, ok := changes.([]byte); ok {
			entry.Changes = raw
		}
	}

	if err := s.store.CreateAuditLog(r.Context(), entry); err != nil {
		slog.Warn("web: failed to write audit log", "action", action, "resource", resourceType+"/"+resourceID, "err", err)
	}
}
