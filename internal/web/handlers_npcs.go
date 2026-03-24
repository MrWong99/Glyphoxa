package web

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
)

// NPCCreateRequest is the JSON body for creating an NPC.
type NPCCreateRequest struct {
	ID              string               `json:"id"`
	Name            string               `json:"name"`
	Personality     string               `json:"personality"`
	Engine          string               `json:"engine"`
	Voice           npcstore.VoiceConfig `json:"voice"`
	KnowledgeScope  []string             `json:"knowledge_scope"`
	SecretKnowledge []string             `json:"secret_knowledge"`
	BehaviorRules   []string             `json:"behavior_rules"`
	Tools           []string             `json:"tools"`
	BudgetTier      string               `json:"budget_tier"`
	GMHelper        bool                 `json:"gm_helper"`
	AddressOnly     bool                 `json:"address_only"`
	Attributes      map[string]any       `json:"attributes"`
}

func (s *Server) handleCreateNPC(w http.ResponseWriter, r *http.Request) {
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

	var req NPCCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
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

	if req.ID == "" {
		req.ID = uuid.NewString()
	}

	def := &npcstore.NPCDefinition{
		ID:              req.ID,
		CampaignID:      campaignID,
		Name:            req.Name,
		Personality:     req.Personality,
		Engine:          req.Engine,
		Voice:           req.Voice,
		KnowledgeScope:  req.KnowledgeScope,
		SecretKnowledge: req.SecretKnowledge,
		BehaviorRules:   req.BehaviorRules,
		Tools:           req.Tools,
		BudgetTier:      req.BudgetTier,
		GMHelper:        req.GMHelper,
		AddressOnly:     req.AddressOnly,
		Attributes:      req.Attributes,
	}

	if err := s.npcs.Create(r.Context(), def); err != nil {
		slog.Error("web: create npc", "campaign_id", campaignID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to create NPC")
		return
	}

	slog.Info("web: npc created",
		"npc_id", def.ID,
		"campaign_id", campaignID,
		"name", def.Name,
	)
	writeJSON(w, http.StatusCreated, map[string]any{"data": def})
}

func (s *Server) handleListNPCs(w http.ResponseWriter, r *http.Request) {
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

	npcs, err := s.npcs.List(r.Context(), campaignID)
	if err != nil {
		slog.Error("web: list npcs", "campaign_id", campaignID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to list NPCs")
		return
	}
	if npcs == nil {
		npcs = []npcstore.NPCDefinition{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": npcs})
}

func (s *Server) handleGetNPC(w http.ResponseWriter, r *http.Request) {
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

	npcID := r.PathValue("npc_id")
	npc, err := s.npcs.Get(r.Context(), npcID)
	if err != nil {
		slog.Error("web: get npc", "npc_id", npcID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to get NPC")
		return
	}
	if npc == nil || npc.CampaignID != campaignID {
		writeError(w, http.StatusNotFound, "not_found", "NPC not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": npc})
}

func (s *Server) handleUpdateNPC(w http.ResponseWriter, r *http.Request) {
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

	npcID := r.PathValue("npc_id")
	existing, err := s.npcs.Get(r.Context(), npcID)
	if err != nil || existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "NPC not found")
		return
	}
	if existing.CampaignID != campaignID {
		writeError(w, http.StatusNotFound, "not_found", "NPC not found in this campaign")
		return
	}

	// Decode full replacement body.
	var req NPCCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}

	existing.Name = req.Name
	existing.Personality = req.Personality
	existing.Engine = req.Engine
	existing.Voice = req.Voice
	existing.KnowledgeScope = req.KnowledgeScope
	existing.SecretKnowledge = req.SecretKnowledge
	existing.BehaviorRules = req.BehaviorRules
	existing.Tools = req.Tools
	existing.BudgetTier = req.BudgetTier
	existing.GMHelper = req.GMHelper
	existing.AddressOnly = req.AddressOnly
	existing.Attributes = req.Attributes

	if err := s.npcs.Update(r.Context(), existing); err != nil {
		slog.Error("web: update npc", "npc_id", npcID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to update NPC")
		return
	}

	slog.Info("web: npc updated", "npc_id", npcID, "campaign_id", campaignID)
	writeJSON(w, http.StatusOK, map[string]any{"data": existing})
}

func (s *Server) handleDeleteNPC(w http.ResponseWriter, r *http.Request) {
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

	npcID := r.PathValue("npc_id")

	// Verify the NPC belongs to this campaign before deleting.
	existing, err := s.npcs.Get(r.Context(), npcID)
	if err != nil || existing == nil || existing.CampaignID != campaignID {
		writeError(w, http.StatusNotFound, "not_found", "NPC not found")
		return
	}

	if err := s.npcs.Delete(r.Context(), npcID); err != nil {
		slog.Error("web: delete npc", "npc_id", npcID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to delete NPC")
		return
	}

	slog.Info("web: npc deleted", "npc_id", npcID, "campaign_id", campaignID)
	w.WriteHeader(http.StatusNoContent)
}
