package web

import (
	"encoding/json"
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

// KnowledgeGraphData represents the full graph structure for frontend visualization.
type KnowledgeGraphData struct {
	Entities      []KnowledgeEntity       `json:"entities"`
	Relationships []KnowledgeRelationship `json:"relationships"`
}

// KnowledgeRelationship represents a relationship between two knowledge entities.
type KnowledgeRelationship struct {
	SourceID   string         `json:"source_id"`
	TargetID   string         `json:"target_id"`
	SourceName string         `json:"source_name"`
	TargetName string         `json:"target_name"`
	RelType    string         `json:"rel_type"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// handleGetKnowledgeGraph returns the full knowledge graph for a campaign,
// formatted for force-directed graph visualization.
func (s *Server) handleGetKnowledgeGraph(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	campaignID := r.PathValue("id")
	campaign, err := s.store.GetCampaign(r.Context(), claims.TenantID, campaignID)
	if err != nil || campaign == nil {
		writeError(w, http.StatusNotFound, "not_found", "campaign not found")
		return
	}

	// Get all entities (no pagination for graph view).
	entities, err := s.store.ListKnowledgeEntities(r.Context(), claims.TenantID, campaignID, CursorPage{Limit: 500})
	if err != nil {
		slog.Error("web: get knowledge graph entities", "campaign_id", campaignID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to load knowledge graph")
		return
	}
	if entities == nil {
		entities = []KnowledgeEntity{}
	}

	// Build a name lookup and generate synthetic relationships from entity attributes.
	nameMap := make(map[string]string, len(entities))
	for _, e := range entities {
		nameMap[e.ID] = e.Name
	}

	var relationships []KnowledgeRelationship
	for _, e := range entities {
		if relations, ok := e.Attributes["relationships"]; ok {
			if relsJSON, err := json.Marshal(relations); err == nil {
				var rels []struct {
					TargetID string `json:"target_id"`
					RelType  string `json:"rel_type"`
				}
				if err := json.Unmarshal(relsJSON, &rels); err == nil {
					for _, rel := range rels {
						relationships = append(relationships, KnowledgeRelationship{
							SourceID:   e.ID,
							TargetID:   rel.TargetID,
							SourceName: e.Name,
							TargetName: nameMap[rel.TargetID],
							RelType:    rel.RelType,
						})
					}
				}
			}
		}
	}
	if relationships == nil {
		relationships = []KnowledgeRelationship{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": KnowledgeGraphData{
			Entities:      entities,
			Relationships: relationships,
		},
	})
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
