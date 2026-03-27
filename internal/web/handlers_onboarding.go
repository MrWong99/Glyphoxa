package web

import (
	"log/slog"
	"net/http"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
)

func (s *Server) handleOnboardingComplete(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}
	if claims.TenantID != "" {
		writeError(w, http.StatusConflict, "already_onboarded", "user already belongs to a tenant")
		return
	}
	var req struct {
		TenantID    string `json:"tenant_id"`
		DisplayName string `json:"display_name"`
		LicenseTier string `json:"license_tier"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.TenantID == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "tenant_id is required")
		return
	}
	if !validTenantID.MatchString(req.TenantID) {
		writeError(w, http.StatusBadRequest, "invalid_id", "tenant_id must start with a lowercase letter and contain only lowercase alphanumeric and underscores")
		return
	}
	if req.DisplayName == "" {
		req.DisplayName = req.TenantID
	}
	tier := req.LicenseTier
	if tier == "" {
		tier = "shared"
	}
	if s.gwClient != nil {
		if _, err := s.gwClient.CreateTenant(r.Context(), &pb.CreateTenantRequest{Id: req.TenantID, LicenseTier: tier}); err != nil {
			writeGRPCError(w, "onboarding create tenant", err)
			return
		}
	}
	if err := s.store.UpdateUserTenant(r.Context(), claims.Sub, req.TenantID, "tenant_admin"); err != nil {
		slog.Error("web: onboarding assign tenant", "user_id", claims.Sub, "tenant_id", req.TenantID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to assign tenant")
		return
	}
	token, err := SignJWT(s.cfg.JWTSecret, Claims{Sub: claims.Sub, TenantID: req.TenantID, Role: "tenant_admin"})
	if err != nil {
		slog.Error("web: onboarding sign jwt", "user_id", claims.Sub, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to issue token")
		return
	}
	slog.Info("web: onboarding complete", "user_id", claims.Sub, "tenant_id", req.TenantID, "license_tier", tier)
	writeJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 86400, "tenant_id": req.TenantID}})
}

func (s *Server) handleValidateInvite(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "missing_token", "token query parameter is required")
		return
	}
	inv, err := s.store.GetInviteByToken(r.Context(), token)
	if err != nil {
		slog.Error("web: validate invite", "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to validate invite")
		return
	}
	if inv == nil {
		writeError(w, http.StatusNotFound, "invalid_invite", "invite not found, expired, or already used")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"valid": true, "role": inv.Role, "tenant_id": inv.TenantID, "expires_at": inv.ExpiresAt}})
}
