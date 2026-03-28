package web

import (
	"log/slog"
	"net/http"

	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
)

func (s *Server) handleLinkNPCToCampaign(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	_, campaignID := s.requireCampaign(w, r, claims.TenantID)
	if campaignID == "" {
		return
	}

	npcID := r.PathValue("npc_id")

	// Verify NPC exists — scoped to the calling tenant, pass empty campaign
	// to look up across campaigns within the tenant.
	npc, err := s.npcs.Get(r.Context(), claims.TenantID, npcID, "")
	if err != nil || npc == nil {
		writeError(w, http.StatusNotFound, "not_found", "NPC not found")
		return
	}

	// Verify the NPC's home campaign belongs to this tenant (prevent cross-tenant linking).
	npcCampaign, err := s.store.GetCampaign(r.Context(), claims.TenantID, npc.CampaignID)
	if err != nil || npcCampaign == nil {
		writeError(w, http.StatusNotFound, "not_found", "NPC not found")
		return
	}

	// Reject linking to the NPC's home campaign.
	if npc.CampaignID == campaignID {
		writeError(w, http.StatusBadRequest, "same_campaign", "cannot link NPC to its home campaign")
		return
	}

	if err := s.store.LinkNPCToCampaign(r.Context(), campaignID, npcID); err != nil {
		slog.Error("web: link NPC to campaign", "npc_id", npcID, "campaign_id", campaignID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to link NPC")
		return
	}

	slog.Info("web: NPC linked to campaign", "npc_id", npcID, "campaign_id", campaignID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"data": map[string]any{
			"campaign_id": campaignID,
			"npc_id":      npcID,
		},
	})
}

func (s *Server) handleUnlinkNPCFromCampaign(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	_, campaignID := s.requireCampaign(w, r, claims.TenantID)
	if campaignID == "" {
		return
	}

	npcID := r.PathValue("npc_id")

	if err := s.store.UnlinkNPCFromCampaign(r.Context(), campaignID, npcID); err != nil {
		slog.Error("web: unlink NPC from campaign", "npc_id", npcID, "campaign_id", campaignID, "err", err)
		writeError(w, http.StatusNotFound, "not_found", "link not found")
		return
	}

	slog.Info("web: NPC unlinked from campaign", "npc_id", npcID, "campaign_id", campaignID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListLinkedNPCs(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	_, campaignID := s.requireCampaign(w, r, claims.TenantID)
	if campaignID == "" {
		return
	}

	links, err := s.store.ListCampaignNPCLinks(r.Context(), campaignID)
	if err != nil {
		slog.Error("web: list linked NPCs", "campaign_id", campaignID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to list linked NPCs")
		return
	}

	// Resolve NPC definitions.
	type linkedNPC struct {
		CampaignNPCLink
		NPC *npcstore.NPCDefinition `json:"npc,omitempty"`
	}
	result := make([]linkedNPC, 0, len(links))
	for _, link := range links {
		ln := linkedNPC{CampaignNPCLink: link}
		npc, err := s.npcs.Get(r.Context(), claims.TenantID, link.NPCID, "")
		if err == nil && npc != nil {
			ln.NPC = npc
		}
		result = append(result, ln)
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}
