package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// CampaignCreateRequest is the JSON body for creating a campaign.
type CampaignCreateRequest struct {
	Name        string `json:"name"`
	System      string `json:"system"`
	Description string `json:"description"`
}

// CampaignUpdateRequest is the JSON body for updating a campaign.
type CampaignUpdateRequest struct {
	Name        *string `json:"name"`
	System      *string `json:"system"`
	Description *string `json:"description"`
}

func (s *Server) handleCreateCampaign(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	var req CampaignCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "missing_name", "name is required")
		return
	}

	campaign := &Campaign{
		TenantID:    claims.TenantID,
		Name:        req.Name,
		System:      req.System,
		Description: req.Description,
	}

	if err := s.store.CreateCampaign(r.Context(), campaign); err != nil {
		slog.Error("web: create campaign", "tenant_id", claims.TenantID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to create campaign")
		return
	}

	slog.Info("web: campaign created",
		"campaign_id", campaign.ID,
		"tenant_id", claims.TenantID,
		"name", campaign.Name,
	)
	writeJSON(w, http.StatusCreated, map[string]any{"data": campaign})
}

func (s *Server) handleListCampaigns(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	campaigns, err := s.store.ListCampaigns(r.Context(), claims.TenantID)
	if err != nil {
		slog.Error("web: list campaigns", "tenant_id", claims.TenantID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to list campaigns")
		return
	}
	if campaigns == nil {
		campaigns = []Campaign{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": campaigns})
}

func (s *Server) handleGetCampaign(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	id := r.PathValue("id")
	campaign, err := s.store.GetCampaign(r.Context(), claims.TenantID, id)
	if err != nil {
		slog.Error("web: get campaign", "campaign_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to get campaign")
		return
	}
	if campaign == nil {
		writeError(w, http.StatusNotFound, "not_found", "campaign not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": campaign})
}

func (s *Server) handleUpdateCampaign(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	id := r.PathValue("id")

	// Fetch existing to merge partial updates.
	existing, err := s.store.GetCampaign(r.Context(), claims.TenantID, id)
	if err != nil {
		slog.Error("web: get campaign for update", "campaign_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to get campaign")
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "campaign not found")
		return
	}

	var req CampaignUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}

	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.System != nil {
		existing.System = *req.System
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}

	if err := s.store.UpdateCampaign(r.Context(), existing); err != nil {
		slog.Error("web: update campaign", "campaign_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to update campaign")
		return
	}

	slog.Info("web: campaign updated", "campaign_id", id, "tenant_id", claims.TenantID)
	writeJSON(w, http.StatusOK, map[string]any{"data": existing})
}

func (s *Server) handleDeleteCampaign(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	id := r.PathValue("id")
	if err := s.store.DeleteCampaign(r.Context(), claims.TenantID, id); err != nil {
		slog.Error("web: delete campaign", "campaign_id", id, "err", err)
		writeError(w, http.StatusNotFound, "not_found", "campaign not found")
		return
	}

	slog.Info("web: campaign deleted", "campaign_id", id, "tenant_id", claims.TenantID)
	w.WriteHeader(http.StatusNoContent)
}
