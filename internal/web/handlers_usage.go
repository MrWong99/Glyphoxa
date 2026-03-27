package web

import (
	"log/slog"
	"net/http"
	"time"
)

func (s *Server) handleGetUsage(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	// Default: last 6 months of usage.
	to := time.Now().UTC()
	from := to.AddDate(0, -6, 0)

	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			from = t
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			to = t
		}
	}

	records, err := s.store.GetUsage(r.Context(), claims.TenantID, from, to)
	if err != nil {
		slog.Error("web: get usage", "tenant_id", claims.TenantID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to get usage data")
		return
	}
	if records == nil {
		records = []UsageRecord{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": records})
}
