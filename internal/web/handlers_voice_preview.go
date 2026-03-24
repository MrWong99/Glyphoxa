package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

const (
	defaultPreviewText = "Greetings, adventurer. What brings you to these lands?"
	maxPreviewTextLen  = 500
)

// VoicePreviewRequest is the JSON body for a voice preview request.
type VoicePreviewRequest struct {
	Text string `json:"text"`
}

func (s *Server) handleVoicePreview(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	if s.voicePreview == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "voice preview not available")
		return
	}

	npcID := r.PathValue("npc_id")
	npc, err := s.npcs.Get(r.Context(), npcID)
	if err != nil {
		slog.Error("web: get npc for voice preview", "npc_id", npcID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to get NPC")
		return
	}
	if npc == nil {
		writeError(w, http.StatusNotFound, "not_found", "NPC not found")
		return
	}

	// Verify NPC belongs to a campaign owned by this tenant.
	campaign, err := s.store.GetCampaign(r.Context(), claims.TenantID, npc.CampaignID)
	if err != nil || campaign == nil {
		writeError(w, http.StatusNotFound, "not_found", "NPC not found")
		return
	}

	// Rate limit.
	if s.voicePreviewRL != nil && !s.voicePreviewRL.Allow(claims.Sub) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many voice preview requests")
		return
	}

	// Parse optional text.
	text := defaultPreviewText
	var req VoicePreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Text != "" {
		text = req.Text
	}
	if len(text) > maxPreviewTextLen {
		writeError(w, http.StatusBadRequest, "text_too_long", "preview text must be 500 characters or fewer")
		return
	}

	audio, contentType, err := s.voicePreview.SynthesizePreview(r.Context(), text, npc.Voice)
	if err != nil {
		slog.Error("web: synthesize voice preview", "npc_id", npcID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to synthesize preview")
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(audio); err != nil {
		slog.Error("web: write voice preview response", "err", err)
	}
}
