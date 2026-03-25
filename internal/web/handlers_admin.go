package web

import (
	"log/slog"
	"net/http"
	"strconv"
)

// handleAdminDashboardStats returns system-wide aggregate stats for super admins.
func (s *Server) handleAdminDashboardStats(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	stats, err := s.store.GetAdminDashboardStats(r.Context())
	if err != nil {
		slog.Error("web: admin dashboard stats", "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to load admin stats")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": stats})
}

// handleAdminListUsers lists all users across all tenants (super_admin only).
func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	users, total, err := s.store.ListAllTenantUsers(r.Context(), limit, offset)
	if err != nil {
		slog.Error("web: admin list users", "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to list users")
		return
	}

	if users == nil {
		users = []User{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":  users,
		"total": total,
	})
}

// handleAuthProviders returns which auth providers are configured.
// This is used by the frontend login page to show available login options.
func (s *Server) handleAuthProviders(w http.ResponseWriter, _ *http.Request) {
	providers := map[string]bool{
		"discord": s.cfg.DiscordClientID != "" && s.cfg.DiscordClientSecret != "",
		"google":  s.cfg.GoogleClientID != "" && s.cfg.GoogleClientSecret != "",
		"github":  s.cfg.GitHubClientID != "" && s.cfg.GitHubClientSecret != "",
		"apikey":  s.cfg.AdminAPIKey != "",
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": providers})
}
