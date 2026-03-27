package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/MrWong99/glyphoxa/internal/config"
)

// Tenant represents a tenant record managed by the admin API.
type Tenant struct {
	ID                  string             `json:"id"`
	LicenseTier         config.LicenseTier `json:"license_tier"`
	BotToken            string             `json:"bot_token,omitempty"`
	GuildIDs            []string           `json:"guild_ids,omitempty"`
	DMRoleID            string             `json:"dm_role_id,omitempty"`
	CampaignID          string             `json:"campaign_id,omitempty"`
	MonthlySessionHours float64            `json:"monthly_session_hours,omitempty"`
	CreatedAt           time.Time          `json:"created_at"`
	UpdatedAt           time.Time          `json:"updated_at"`
}

// MarshalJSON implements custom JSON marshalling to serialise LicenseTier as a string.
func (t Tenant) MarshalJSON() ([]byte, error) {
	type Alias Tenant
	return json.Marshal(&struct {
		LicenseTier string `json:"license_tier"`
		Alias
	}{
		LicenseTier: t.LicenseTier.String(),
		Alias:       Alias(t),
	})
}

// TenantCreateRequest is the JSON body for creating a tenant.
type TenantCreateRequest struct {
	ID          string   `json:"id"`
	LicenseTier string   `json:"license_tier"`
	BotToken    string   `json:"bot_token,omitempty"`
	GuildIDs    []string `json:"guild_ids,omitempty"`
	DMRoleID    string   `json:"dm_role_id,omitempty"`
	CampaignID  string   `json:"campaign_id,omitempty"`
}

// TenantUpdateRequest is the JSON body for updating a tenant.
type TenantUpdateRequest struct {
	LicenseTier string   `json:"license_tier,omitempty"`
	BotToken    string   `json:"bot_token,omitempty"`
	GuildIDs    []string `json:"guild_ids,omitempty"`
	DMRoleID    *string  `json:"dm_role_id,omitempty"`
	CampaignID  *string  `json:"campaign_id,omitempty"`
}

// AdminStore abstracts persistent storage for tenant records.
type AdminStore interface {
	CreateTenant(ctx context.Context, t Tenant) error
	GetTenant(ctx context.Context, id string) (Tenant, error)
	UpdateTenant(ctx context.Context, t Tenant) error
	DeleteTenant(ctx context.Context, id string) error
	ListTenants(ctx context.Context) ([]Tenant, error)
}

// BotConnector manages Discord bot connections for tenants.
type BotConnector interface {
	ConnectBot(ctx context.Context, tenantID, botToken string, guildIDs []string) error
	DisconnectBot(tenantID string)
}

// TenantBotConnector extends BotConnector with tenant-aware bot creation.
type TenantBotConnector interface {
	BotConnector
	ConnectBotForTenant(ctx context.Context, tenant Tenant) error
}

// AdminAPI serves the internal admin HTTP API for tenant and session management.
type AdminAPI struct {
	mux    *http.ServeMux
	store  AdminStore
	apiKey string
	bots   BotConnector
}

// NewAdminAPI creates an AdminAPI.
func NewAdminAPI(store AdminStore, apiKey string, bots BotConnector) *AdminAPI {
	a := &AdminAPI{
		mux:    http.NewServeMux(),
		store:  store,
		apiKey: apiKey,
		bots:   bots,
	}
	a.registerRoutes()
	return a
}

// Handler returns the http.Handler for this admin API.
func (a *AdminAPI) Handler() http.Handler {
	return a.authMiddleware(a.mux)
}

// ReconnectAllBots loads all tenants and reconnects bots on startup.
func (a *AdminAPI) ReconnectAllBots(ctx context.Context) {
	if a.bots == nil {
		return
	}
	tenants, err := a.store.ListTenants(ctx)
	if err != nil {
		slog.Error("admin: failed to list tenants for bot reconnection", "err", err)
		return
	}
	var connected int
	for _, tenant := range tenants {
		if tenant.BotToken == "" {
			continue
		}
		if err := a.connectBotForTenant(ctx, tenant); err != nil {
			slog.Error("admin: failed to connect bot for tenant", "tenant_id", tenant.ID, "err", err)
			continue
		}
		connected++
	}
	slog.Info("admin: bot reconnection complete",
		"total_tenants", len(tenants),
		"connected", connected,
	)
}

func (a *AdminAPI) connectBotForTenant(ctx context.Context, tenant Tenant) error {
	if tbc, ok := a.bots.(TenantBotConnector); ok {
		return tbc.ConnectBotForTenant(ctx, tenant)
	}
	return a.bots.ConnectBot(ctx, tenant.ID, tenant.BotToken, tenant.GuildIDs)
}

func (a *AdminAPI) registerRoutes() {
	a.mux.HandleFunc("POST /api/v1/tenants", a.createTenant)
	a.mux.HandleFunc("GET /api/v1/tenants", a.listTenants)
	a.mux.HandleFunc("GET /api/v1/tenants/{id}", a.getTenant)
	a.mux.HandleFunc("PUT /api/v1/tenants/{id}", a.updateTenant)
	a.mux.HandleFunc("DELETE /api/v1/tenants/{id}", a.deleteTenant)
}

func (a *AdminAPI) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// When no API key is configured, reject all requests.
		if a.apiKey == "" {
			writeAdminError(w, http.StatusForbidden, "admin API key not configured — set GLYPHOXA_ADMIN_API_KEY")
			return
		}

		// Check Authorization header first, then X-API-Key.
		key := r.Header.Get("Authorization")
		if key == "" {
			key = r.Header.Get("X-API-Key")
		}
		if key == "" {
			writeAdminError(w, http.StatusUnauthorized, "missing API key — set Authorization or X-API-Key header")
			return
		}
		const bearerPrefix = "Bearer "
		if len(key) > len(bearerPrefix) && key[:len(bearerPrefix)] == bearerPrefix {
			key = key[len(bearerPrefix):]
		}
		if key != a.apiKey {
			writeAdminError(w, http.StatusForbidden, "invalid API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *AdminAPI) createTenant(w http.ResponseWriter, r *http.Request) {
	var req TenantCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.ID == "" {
		writeAdminError(w, http.StatusBadRequest, "id is required")
		return
	}
	tc := config.TenantContext{TenantID: req.ID}
	if err := tc.Validate(); err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	tier, err := config.ParseLicenseTier(req.LicenseTier)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	tenant := Tenant{
		ID:          req.ID,
		LicenseTier: tier,
		BotToken:    req.BotToken,
		GuildIDs:    req.GuildIDs,
		DMRoleID:    req.DMRoleID,
		CampaignID:  req.CampaignID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := a.store.CreateTenant(r.Context(), tenant); err != nil {
		slog.Warn("admin: create tenant failed", "tenant_id", req.ID, "err", err)
		writeAdminError(w, http.StatusConflict, fmt.Sprintf("tenant %q already exists", req.ID))
		return
	}
	slog.Info("admin: tenant created",
		"action", "create_tenant",
		"tenant_id", req.ID, "license_tier", tier.String(),
		"source_ip", clientIP(r),
	)
	if a.bots != nil && req.BotToken != "" {
		if err := a.connectBotForTenant(r.Context(), tenant); err != nil {
			slog.Error("admin: failed to connect bot for new tenant", "tenant_id", tenant.ID, "err", err)
		}
	}
	tenant.BotToken = ""
	writeAdminJSON(w, http.StatusCreated, tenant)
}

func (a *AdminAPI) getTenant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tenant, err := a.store.GetTenant(r.Context(), id)
	if err != nil {
		writeAdminError(w, http.StatusNotFound, fmt.Sprintf("tenant %q not found", id))
		return
	}
	tenant.BotToken = ""
	writeAdminJSON(w, http.StatusOK, tenant)
}

func (a *AdminAPI) listTenants(w http.ResponseWriter, r *http.Request) {
	tenants, err := a.store.ListTenants(r.Context())
	if err != nil {
		slog.Warn("admin: list tenants failed", "err", err)
		writeAdminError(w, http.StatusInternalServerError, "failed to list tenants")
		return
	}
	for i := range tenants {
		tenants[i].BotToken = ""
	}
	writeAdminJSON(w, http.StatusOK, tenants)
}

func (a *AdminAPI) updateTenant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.store.GetTenant(r.Context(), id)
	if err != nil {
		writeAdminError(w, http.StatusNotFound, fmt.Sprintf("tenant %q not found", id))
		return
	}
	var req TenantUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.LicenseTier != "" {
		tier, err := config.ParseLicenseTier(req.LicenseTier)
		if err != nil {
			writeAdminError(w, http.StatusBadRequest, err.Error())
			return
		}
		existing.LicenseTier = tier
	}
	if req.BotToken != "" {
		existing.BotToken = req.BotToken
	}
	if req.GuildIDs != nil {
		existing.GuildIDs = req.GuildIDs
	}
	if req.DMRoleID != nil {
		existing.DMRoleID = *req.DMRoleID
	}
	if req.CampaignID != nil {
		existing.CampaignID = *req.CampaignID
	}
	existing.UpdatedAt = time.Now().UTC()
	if err := a.store.UpdateTenant(r.Context(), existing); err != nil {
		slog.Warn("admin: update tenant failed", "tenant_id", id, "err", err)
		writeAdminError(w, http.StatusInternalServerError, "failed to update tenant")
		return
	}
	slog.Info("admin: tenant updated",
		"action", "update_tenant", "tenant_id", id,
		"source_ip", clientIP(r),
	)
	needsReconnect := req.BotToken != "" || req.GuildIDs != nil || req.DMRoleID != nil || req.CampaignID != nil
	if a.bots != nil && existing.BotToken != "" && needsReconnect {
		if err := a.connectBotForTenant(r.Context(), existing); err != nil {
			slog.Error("admin: failed to reconnect bot for tenant", "tenant_id", id, "err", err)
		}
	}
	existing.BotToken = ""
	writeAdminJSON(w, http.StatusOK, existing)
}

func (a *AdminAPI) deleteTenant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.store.DeleteTenant(r.Context(), id); err != nil {
		writeAdminError(w, http.StatusNotFound, fmt.Sprintf("tenant %q not found", id))
		return
	}
	if a.bots != nil {
		a.bots.DisconnectBot(id)
	}
	slog.Info("admin: tenant deleted",
		"action", "delete_tenant", "tenant_id", id,
		"source_ip", clientIP(r),
	)
	writeAdminJSON(w, http.StatusNoContent, nil)
}

// clientIP extracts the client IP address from the request, checking
// X-Forwarded-For for reverse-proxy setups before falling back to RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For can contain a chain: "client, proxy1, proxy2".
		if ip, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(ip)
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeAdminJSON(w http.ResponseWriter, status int, v any) {
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("admin: failed to encode response", "err", err)
	}
}

type adminError struct {
	Error string `json:"error"`
}

func writeAdminError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(adminError{Error: msg}); err != nil {
		slog.Warn("admin: failed to encode error response", "err", err)
	}
}
