package web

import (
	"log/slog"
	"net/http"
	"strconv"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
)

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
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
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	sessionID := r.PathValue("id")

	exists, err := s.store.SessionExists(r.Context(), claims.TenantID, sessionID)
	if err != nil {
		slog.Error("web: check session exists", "session_id", sessionID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to check session")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "not_found", "session not found")
		return
	}

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

// handleStartSession starts a voice session via the gateway ManagementService.
func (s *Server) handleStartSession(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}
	if s.gwClient == nil {
		writeError(w, http.StatusServiceUnavailable, "no_gateway", "gateway gRPC not configured")
		return
	}

	var req struct {
		CampaignID string `json:"campaign_id"`
		GuildID    string `json:"guild_id"`
		ChannelID  string `json:"channel_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.GuildID == "" || req.ChannelID == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "guild_id and channel_id are required")
		return
	}

	resp, err := s.gwClient.StartWebSession(r.Context(), &pb.StartWebSessionRequest{
		TenantId:   claims.TenantID,
		CampaignId: req.CampaignID,
		GuildId:    req.GuildID,
		ChannelId:  req.ChannelID,
	})
	if err != nil {
		writeGRPCError(w, "start session", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"data": map[string]any{
			"session_id": resp.GetSessionId(),
		},
	})
}

// handleStopSession stops a running session via the gateway ManagementService.
func (s *Server) handleStopSession(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}
	if s.gwClient == nil {
		writeError(w, http.StatusServiceUnavailable, "no_gateway", "gateway gRPC not configured")
		return
	}

	sessionID := r.PathValue("id")

	// Verify the session belongs to the caller's tenant before allowing stop.
	exists, err := s.store.SessionExists(r.Context(), claims.TenantID, sessionID)
	if err != nil {
		slog.Error("web: check session exists", "session_id", sessionID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to check session")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "not_found", "session not found")
		return
	}

	if _, err := s.gwClient.StopWebSession(r.Context(), &pb.StopWebSessionRequest{
		SessionId: sessionID,
	}); err != nil {
		writeGRPCError(w, "stop session", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleListActiveSessions returns active (non-ended) sessions for the tenant
// via the gateway ManagementService.
func (s *Server) handleListActiveSessions(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}
	if s.gwClient == nil {
		writeError(w, http.StatusServiceUnavailable, "no_gateway", "gateway gRPC not configured")
		return
	}

	resp, err := s.gwClient.ListActiveSessions(r.Context(), &pb.ListActiveSessionsRequest{
		TenantId: claims.TenantID,
	})
	if err != nil {
		writeGRPCError(w, "list active sessions", err)
		return
	}

	sessions := make([]map[string]any, len(resp.GetSessions()))
	for i, sess := range resp.GetSessions() {
		sessions[i] = map[string]any{
			"session_id":   sess.GetSessionId(),
			"tenant_id":    sess.GetTenantId(),
			"campaign_id":  sess.GetCampaignId(),
			"guild_id":     sess.GetGuildId(),
			"channel_id":   sess.GetChannelId(),
			"license_tier": sess.GetLicenseTier(),
			"state":        sess.GetState().String(),
			"error":        sess.GetError(),
			"started_at":   sess.GetStartedAt().AsTime(),
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": sessions})
}
