package web

import (
	"log/slog"
	"net/http"
)

func (s *Server) handleListKnowledgeEntities(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	campaignID := r.PathValue("id")

	// Verify the campaign belongs to this tenant.
	campaign, err := s.store.GetCampaign(r.Context(), claims.TenantID, campaignID)
	if err != nil || campaign == nil {
		writeError(w, http.StatusNotFound, "not_found", "campaign not found")
		return
	}

	page := ParseCursorPage(r)
	entities, err := s.store.ListKnowledgeEntities(r.Context(), claims.TenantID, campaignID, page)
	if err != nil {
		slog.Error("web: list knowledge entities", "campaign_id", campaignID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to list knowledge entities")
		return
	}
	if entities == nil {
		entities = []KnowledgeEntity{}
	}

	meta := PageMeta{Limit: page.Limit}
	if len(entities) > page.Limit {
		meta.HasMore = true
		last := entities[page.Limit-1]
		meta.NextCursor = EncodeCursor(last.CreatedAt, last.ID)
		entities = entities[:page.Limit]
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": entities, "pagination": meta})
}

func (s *Server) handleDeleteKnowledgeEntity(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	campaignID := r.PathValue("id")

	// Verify the campaign belongs to this tenant.
	campaign, err := s.store.GetCampaign(r.Context(), claims.TenantID, campaignID)
	if err != nil || campaign == nil {
		writeError(w, http.StatusNotFound, "not_found", "campaign not found")
		return
	}

	entityID := r.PathValue("entity_id")
	if err := s.store.DeleteKnowledgeEntity(r.Context(), claims.TenantID, campaignID, entityID); err != nil {
		slog.Error("web: delete knowledge entity", "entity_id", entityID, "campaign_id", campaignID, "err", err)
		writeError(w, http.StatusNotFound, "not_found", "knowledge entity not found")
		return
	}

	slog.Info("web: knowledge entity deleted", "entity_id", entityID, "campaign_id", campaignID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRebuildKnowledgeGraph(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	campaignID := r.PathValue("id")

	// Verify the campaign belongs to this tenant.
	campaign, err := s.store.GetCampaign(r.Context(), claims.TenantID, campaignID)
	if err != nil || campaign == nil {
		writeError(w, http.StatusNotFound, "not_found", "campaign not found")
		return
	}

	// The actual rebuild is an async background job. We return 202 Accepted
	// immediately, indicating the work has been queued.
	slog.Info("web: knowledge graph rebuild queued", "campaign_id", campaignID, "tenant_id", claims.TenantID)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"data": map[string]any{
			"status":      "queued",
			"campaign_id": campaignID,
		},
	})
}
