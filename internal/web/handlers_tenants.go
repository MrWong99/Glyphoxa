package web

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// handleListTenants proxies to the gateway admin API.
func (s *Server) handleListTenants(w http.ResponseWriter, r *http.Request) {
	s.proxyToGateway(w, r, http.MethodGet, "/api/v1/tenants")
}

// handleGetTenant proxies to the gateway admin API.
// tenant_admin can only access their own tenant; super_admin can access any.
func (s *Server) handleGetTenant(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	id := r.PathValue("id")
	if claims != nil && claims.Role != "super_admin" && claims.TenantID != id {
		writeError(w, http.StatusForbidden, "forbidden", "cannot access another tenant")
		return
	}
	s.proxyToGateway(w, r, http.MethodGet, "/api/v1/tenants/"+id)
}

// handleCreateTenant proxies to the gateway admin API.
func (s *Server) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	s.proxyToGatewayWithBody(w, r, http.MethodPost, "/api/v1/tenants")
}

// handleUpdateTenant proxies to the gateway admin API.
// tenant_admin can only update their own tenant; super_admin can update any.
func (s *Server) handleUpdateTenant(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	id := r.PathValue("id")
	if claims != nil && claims.Role != "super_admin" && claims.TenantID != id {
		writeError(w, http.StatusForbidden, "forbidden", "cannot modify another tenant")
		return
	}
	s.proxyToGatewayWithBody(w, r, http.MethodPut, "/api/v1/tenants/"+id)
}

// handleDeleteTenant proxies to the gateway admin API.
func (s *Server) handleDeleteTenant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.proxyToGateway(w, r, http.MethodDelete, "/api/v1/tenants/"+id)
}

func (s *Server) proxyToGateway(w http.ResponseWriter, r *http.Request, method, path string) {
	if s.cfg.GatewayURL == "" {
		writeError(w, http.StatusServiceUnavailable, "no_gateway", "gateway URL not configured")
		return
	}

	target := strings.TrimRight(s.cfg.GatewayURL, "/") + path
	req, err := http.NewRequestWithContext(r.Context(), method, target, nil)
	if err != nil {
		slog.Error("web: create gateway request", "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to create gateway request")
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("web: gateway request failed", "path", path, "err", err)
		writeError(w, http.StatusBadGateway, "gateway_error", "failed to reach gateway")
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		slog.Error("web: copy gateway response", "err", err)
	}
}

func (s *Server) proxyToGatewayWithBody(w http.ResponseWriter, r *http.Request, method, path string) {
	if s.cfg.GatewayURL == "" {
		writeError(w, http.StatusServiceUnavailable, "no_gateway", "gateway URL not configured")
		return
	}

	target := strings.TrimRight(s.cfg.GatewayURL, "/") + path
	req, err := http.NewRequestWithContext(r.Context(), method, target, r.Body)
	if err != nil {
		slog.Error("web: create gateway request", "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to create gateway request")
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("web: gateway request failed", "path", path, "err", err)
		writeError(w, http.StatusBadGateway, "gateway_error", "failed to reach gateway")
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		slog.Error("web: copy gateway response", "err", err)
	}
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

	// Proxy creation to gateway if configured.
	if s.cfg.GatewayURL != "" {
		gwBody := fmt.Sprintf(`{"id":%q,"license_tier":"shared"}`, req.ID)
		target := strings.TrimRight(s.cfg.GatewayURL, "/") + "/api/v1/tenants"
		gwReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, strings.NewReader(gwBody))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "server_error", "failed to create gateway request")
			return
		}
		gwReq.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(gwReq)
		if err != nil {
			writeError(w, http.StatusBadGateway, "gateway_error", "failed to create tenant in gateway")
			return
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			writeError(w, resp.StatusCode, "gateway_error", "gateway rejected tenant creation")
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
