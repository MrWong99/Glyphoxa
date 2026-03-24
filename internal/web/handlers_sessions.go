package web

import (
	"log/slog"
	"net/http"
	"strconv"
)

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if limit <= 0 {
		limit = 25
	}
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * limit

	sessions, err := s.store.ListSessions(r.Context(), claims.TenantID, limit, offset)
	if err != nil {
		slog.Error("web: list sessions", "tenant_id", claims.TenantID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to list sessions")
		return
	}
	if sessions == nil {
		sessions = []SessionSummary{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": sessions,
		"meta": map[string]any{
			"page":     page,
			"per_page": limit,
		},
	})
}

func (s *Server) handleGetTranscript(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	sessionID := r.PathValue("id")
	entries, err := s.store.GetTranscript(r.Context(), claims.TenantID, sessionID)
	if err != nil {
		slog.Error("web: get transcript", "session_id", sessionID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to get transcript")
		return
	}
	if entries == nil {
		entries = []TranscriptEntry{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": entries})
}
