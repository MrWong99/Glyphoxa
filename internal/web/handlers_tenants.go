package web

import (
	"encoding/json"
	"log/slog"
	"net/http"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// handleListTenants calls the gateway ManagementService via gRPC.
func (s *Server) handleListTenants(w http.ResponseWriter, r *http.Request) {
	if s.gwClient == nil {
		writeError(w, http.StatusServiceUnavailable, "no_gateway", "gateway gRPC not configured")
		return
	}

	resp, err := s.gwClient.ListTenants(r.Context(), &pb.ListTenantsRequest{})
	if err != nil {
		writeGRPCError(w, "list tenants", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": tenantsFromPB(resp.GetTenants())})
}

// handleGetTenant calls the gateway ManagementService via gRPC.
// tenant_admin can only access their own tenant; super_admin can access any.
func (s *Server) handleGetTenant(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	id := r.PathValue("id")
	if claims != nil && claims.Role != "super_admin" && claims.TenantID != id {
		writeError(w, http.StatusForbidden, "forbidden", "cannot access another tenant")
		return
	}

	if s.gwClient == nil {
		writeError(w, http.StatusServiceUnavailable, "no_gateway", "gateway gRPC not configured")
		return
	}

	resp, err := s.gwClient.GetTenant(r.Context(), &pb.GetTenantRequest{Id: id})
	if err != nil {
		writeGRPCError(w, "get tenant", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": tenantFromPB(resp.GetTenant())})
}

// handleCreateTenant calls the gateway ManagementService via gRPC.
func (s *Server) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	if s.gwClient == nil {
		writeError(w, http.StatusServiceUnavailable, "no_gateway", "gateway gRPC not configured")
		return
	}

	var req struct {
		ID          string   `json:"id"`
		LicenseTier string   `json:"license_tier"`
		BotToken    string   `json:"bot_token,omitempty"`
		GuildIDs    []string `json:"guild_ids,omitempty"`
		DMRoleID    string   `json:"dm_role_id,omitempty"`
		CampaignID  string   `json:"campaign_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}

	resp, err := s.gwClient.CreateTenant(r.Context(), &pb.CreateTenantRequest{
		Id:          req.ID,
		LicenseTier: req.LicenseTier,
		BotToken:    req.BotToken,
		GuildIds:    req.GuildIDs,
		DmRoleId:    req.DMRoleID,
		CampaignId:  req.CampaignID,
	})
	if err != nil {
		writeGRPCError(w, "create tenant", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"data": tenantFromPB(resp.GetTenant())})
}

// handleUpdateTenant calls the gateway ManagementService via gRPC.
// tenant_admin can only update their own tenant; super_admin can update any.
func (s *Server) handleUpdateTenant(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	id := r.PathValue("id")
	if claims != nil && claims.Role != "super_admin" && claims.TenantID != id {
		writeError(w, http.StatusForbidden, "forbidden", "cannot modify another tenant")
		return
	}

	if s.gwClient == nil {
		writeError(w, http.StatusServiceUnavailable, "no_gateway", "gateway gRPC not configured")
		return
	}

	var req struct {
		LicenseTier string   `json:"license_tier,omitempty"`
		BotToken    string   `json:"bot_token,omitempty"`
		GuildIDs    []string `json:"guild_ids,omitempty"`
		DMRoleID    *string  `json:"dm_role_id,omitempty"`
		CampaignID  *string  `json:"campaign_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}

	grpcReq := &pb.UpdateTenantRequest{
		Id:          id,
		LicenseTier: req.LicenseTier,
		BotToken:    req.BotToken,
		GuildIds:    req.GuildIDs,
	}
	if req.DMRoleID != nil {
		if *req.DMRoleID == "" {
			grpcReq.ClearDmRoleId = true
		} else {
			grpcReq.DmRoleId = *req.DMRoleID
		}
	}
	if req.CampaignID != nil {
		if *req.CampaignID == "" {
			grpcReq.ClearCampaignId = true
		} else {
			grpcReq.CampaignId = *req.CampaignID
		}
	}

	resp, err := s.gwClient.UpdateTenant(r.Context(), grpcReq)
	if err != nil {
		writeGRPCError(w, "update tenant", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": tenantFromPB(resp.GetTenant())})
}

// handleDeleteTenant calls the gateway ManagementService via gRPC.
func (s *Server) handleDeleteTenant(w http.ResponseWriter, r *http.Request) {
	if s.gwClient == nil {
		writeError(w, http.StatusServiceUnavailable, "no_gateway", "gateway gRPC not configured")
		return
	}

	id := r.PathValue("id")
	if _, err := s.gwClient.DeleteTenant(r.Context(), &pb.DeleteTenantRequest{Id: id}); err != nil {
		writeGRPCError(w, "delete tenant", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleCreateTenantSelfService creates a tenant via the gateway and then
// creates an associated user record. Used by the onboarding flow.
func (s *Server) handleCreateTenantSelfService(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	var req struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "tenant id is required")
		return
	}

	// Create tenant via gateway gRPC if configured.
	if s.gwClient != nil {
		if _, err := s.gwClient.CreateTenant(r.Context(), &pb.CreateTenantRequest{
			Id:          req.ID,
			LicenseTier: "shared",
		}); err != nil {
			st, _ := status.FromError(err)
			slog.Warn("web: self-service gateway create failed", "tenant_id", req.ID, "err", err)
			writeError(w, grpcStatusToHTTP(st.Code()), "gateway_error", "gateway rejected tenant creation")
			return
		}
	}

	slog.Info("web: self-service tenant created",
		"tenant_id", req.ID,
		"user_id", claims.Sub,
	)

	writeJSON(w, http.StatusCreated, map[string]any{
		"data": map[string]any{
			"id":           req.ID,
			"display_name": req.DisplayName,
		},
	})
}

// ── Conversion helpers ──────────────────────────────────────────────────────

func tenantFromPB(t *pb.TenantInfo) map[string]any {
	if t == nil {
		return nil
	}
	m := map[string]any{
		"id":                    t.GetId(),
		"license_tier":          t.GetLicenseTier(),
		"guild_ids":             t.GetGuildIds(),
		"dm_role_id":            t.GetDmRoleId(),
		"campaign_id":           t.GetCampaignId(),
		"monthly_session_hours": t.GetMonthlySessionHours(),
		"created_at":            t.GetCreatedAt().AsTime(),
		"updated_at":            t.GetUpdatedAt().AsTime(),
	}
	return m
}

func tenantsFromPB(ts []*pb.TenantInfo) []map[string]any {
	result := make([]map[string]any, len(ts))
	for i, t := range ts {
		result[i] = tenantFromPB(t)
	}
	return result
}

// writeGRPCError translates a gRPC error to an HTTP error response.
func writeGRPCError(w http.ResponseWriter, op string, err error) {
	st, _ := status.FromError(err)
	httpCode := grpcStatusToHTTP(st.Code())
	slog.Warn("web: gateway gRPC error", "op", op, "code", st.Code(), "msg", st.Message())
	writeError(w, httpCode, "gateway_error", st.Message())
}

// grpcStatusToHTTP maps gRPC status codes to HTTP status codes.
func grpcStatusToHTTP(c codes.Code) int {
	switch c {
	case codes.OK:
		return http.StatusOK
	case codes.InvalidArgument:
		return http.StatusBadRequest
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists:
		return http.StatusConflict
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.FailedPrecondition:
		return http.StatusPreconditionFailed
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}
