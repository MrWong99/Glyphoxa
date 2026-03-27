package web

import (
	"log/slog"
	"net/http"
)

// LoreCreateRequest is the JSON body for creating a lore document.
// Accepts both "content" and "content_markdown" for the body text.
type LoreCreateRequest struct {
	Title           string `json:"title"`
	Content         string `json:"content"`
	ContentMarkdown string `json:"content_markdown"`
	SortOrder       int    `json:"sort_order"`
}

// LoreUpdateRequest is the JSON body for updating a lore document.
type LoreUpdateRequest struct {
	Title           *string `json:"title"`
	ContentMarkdown *string `json:"content_markdown"`
	SortOrder       *int    `json:"sort_order"`
}

func (s *Server) handleCreateLoreDocument(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	_, campaignID := s.requireCampaign(w, r, claims.TenantID)
	if campaignID == "" {
		return
	}

	var req LoreCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "missing_title", "title is required")
		return
	}

	// Accept "content" as an alias for "content_markdown".
	if req.ContentMarkdown == "" && req.Content != "" {
		req.ContentMarkdown = req.Content
	}

	doc := &LoreDocument{
		CampaignID:      campaignID,
		Title:           req.Title,
		ContentMarkdown: req.ContentMarkdown,
		SortOrder:       req.SortOrder,
	}

	if err := s.store.CreateLoreDocument(r.Context(), claims.TenantID, doc); err != nil {
		slog.Error("web: create lore document", "campaign_id", campaignID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to create lore document")
		return
	}

	slog.Info("web: lore document created",
		"lore_id", doc.ID,
		"campaign_id", campaignID,
	)
	writeJSON(w, http.StatusCreated, map[string]any{"data": doc})
}

func (s *Server) handleListLoreDocuments(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	_, campaignID := s.requireCampaign(w, r, claims.TenantID)
	if campaignID == "" {
		return
	}

	docs, err := s.store.ListLoreDocuments(r.Context(), claims.TenantID, campaignID)
	if err != nil {
		slog.Error("web: list lore documents", "campaign_id", campaignID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to list lore documents")
		return
	}
	if docs == nil {
		docs = []LoreDocument{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": docs})
}

func (s *Server) handleGetLoreDocument(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	_, campaignID := s.requireCampaign(w, r, claims.TenantID)
	if campaignID == "" {
		return
	}

	loreID := r.PathValue("lore_id")
	doc, err := s.store.GetLoreDocument(r.Context(), claims.TenantID, campaignID, loreID)
	if err != nil {
		slog.Error("web: get lore document", "lore_id", loreID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to get lore document")
		return
	}
	if doc == nil {
		writeError(w, http.StatusNotFound, "not_found", "lore document not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": doc})
}

func (s *Server) handleUpdateLoreDocument(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	_, campaignID := s.requireCampaign(w, r, claims.TenantID)
	if campaignID == "" {
		return
	}

	loreID := r.PathValue("lore_id")
	existing, err := s.store.GetLoreDocument(r.Context(), claims.TenantID, campaignID, loreID)
	if err != nil {
		slog.Error("web: get lore document for update", "lore_id", loreID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to get lore document")
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "lore document not found")
		return
	}

	var req LoreUpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Title != nil {
		existing.Title = *req.Title
	}
	if req.ContentMarkdown != nil {
		existing.ContentMarkdown = *req.ContentMarkdown
	}
	if req.SortOrder != nil {
		existing.SortOrder = *req.SortOrder
	}

	if err := s.store.UpdateLoreDocument(r.Context(), claims.TenantID, existing); err != nil {
		slog.Error("web: update lore document", "lore_id", loreID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to update lore document")
		return
	}

	slog.Info("web: lore document updated", "lore_id", loreID, "campaign_id", campaignID)
	writeJSON(w, http.StatusOK, map[string]any{"data": existing})
}

func (s *Server) handleDeleteLoreDocument(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	_, campaignID := s.requireCampaign(w, r, claims.TenantID)
	if campaignID == "" {
		return
	}

	loreID := r.PathValue("lore_id")
	if err := s.store.DeleteLoreDocument(r.Context(), claims.TenantID, campaignID, loreID); err != nil {
		slog.Error("web: delete lore document", "lore_id", loreID, "err", err)
		writeError(w, http.StatusNotFound, "not_found", "lore document not found")
		return
	}

	slog.Info("web: lore document deleted", "lore_id", loreID, "campaign_id", campaignID)
	w.WriteHeader(http.StatusNoContent)
}
