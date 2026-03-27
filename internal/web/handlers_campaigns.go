package web

import (
	"log/slog"
	"net/http"
)

// CampaignCreateRequest is the JSON body for creating a campaign.
type CampaignCreateRequest struct {
	Name        string `json:"name"`
	System      string `json:"game_system"`
	Language    string `json:"language"`
	Description string `json:"description"`
}

// CampaignUpdateRequest is the JSON body for updating a campaign.
type CampaignUpdateRequest struct {
	Name        *string `json:"name"`
	System      *string `json:"game_system"`
	Language    *string `json:"language"`
	Description *string `json:"description"`
}

func (s *Server) handleCreateCampaign(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	var req CampaignCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "missing_name", "name is required")
		return
	}
	if len(req.Name) > 255 {
		writeError(w, http.StatusBadRequest, "name_too_long", "name must be 255 characters or fewer")
		return
	}
	if len(req.Description) > 4096 {
		writeError(w, http.StatusBadRequest, "description_too_long", "description must be 4096 characters or fewer")
		return
	}

	campaign := &Campaign{
		TenantID:    claims.TenantID,
		Name:        req.Name,
		System:      req.System,
		Language:    req.Language,
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
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	page := ParseCursorPage(r)

	campaigns, err := s.store.ListCampaigns(r.Context(), claims.TenantID, page)
	if err != nil {
		slog.Error("web: list campaigns", "tenant_id", claims.TenantID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to list campaigns")
		return
	}
	if campaigns == nil {
		campaigns = []Campaign{}
	}

	meta := PageMeta{Limit: page.Limit}
	if len(campaigns) > page.Limit {
		meta.HasMore = true
		last := campaigns[page.Limit-1]
		meta.NextCursor = EncodeCursor(last.CreatedAt, last.ID)
		campaigns = campaigns[:page.Limit]
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": campaigns, "pagination": meta})
}

func (s *Server) handleGetCampaign(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	campaign, _ := s.requireCampaign(w, r, claims.TenantID)
	if campaign == nil {
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": campaign})
}

func (s *Server) handleUpdateCampaign(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	existing, _ := s.requireCampaign(w, r, claims.TenantID)
	if existing == nil {
		return
	}

	id := r.PathValue("id")

	var req CampaignUpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.System != nil {
		existing.System = *req.System
	}
	if req.Language != nil {
		existing.Language = *req.Language
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
	claims := requireClaims(w, r)
	if claims == nil {
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
