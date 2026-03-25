package web

import (
	"log/slog"
	"net/http"
	"time"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
)

// DashboardStats holds aggregate dashboard statistics for a tenant.
type DashboardStats struct {
	CampaignCount      int     `json:"campaign_count"`
	ActiveSessionCount int     `json:"active_session_count"`
	HoursUsed          float64 `json:"hours_used"`
	HoursLimit         float64 `json:"hours_limit"`
}

// ActivityItem represents a single activity event in the tenant's feed.
type ActivityItem struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	Description string    `json:"description"`
	Timestamp   time.Time `json:"timestamp"`
	CampaignID  string    `json:"campaign_id,omitempty"`
}

// handleDashboardStats returns aggregate stats for the current user's tenant.
func (s *Server) handleDashboardStats(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	stats, err := s.store.GetDashboardStats(r.Context(), claims.TenantID)
	if err != nil {
		slog.Error("web: dashboard stats", "tenant_id", claims.TenantID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to load dashboard stats")
		return
	}

	// Try to get hours_limit from the gateway tenant config.
	if s.gwClient != nil {
		resp, err := s.gwClient.GetTenant(r.Context(), &pb.GetTenantRequest{
			Id: claims.TenantID,
		})
		if err == nil && resp.GetTenant() != nil {
			stats.HoursLimit = float64(resp.GetTenant().GetMonthlySessionHours())
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": stats})
}

// handleDashboardActivity returns recent activity for the tenant.
func (s *Server) handleDashboardActivity(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	items, err := s.store.GetRecentActivity(r.Context(), claims.TenantID, 10)
	if err != nil {
		slog.Error("web: dashboard activity", "tenant_id", claims.TenantID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to load activity")
		return
	}
	if items == nil {
		items = []ActivityItem{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": items})
}

