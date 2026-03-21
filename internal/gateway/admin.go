// Package gateway provides the gateway-mode components for multi-tenant
// Glyphoxa deployments: the internal admin API, bot management, and session
// orchestration.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/MrWong99/glyphoxa/internal/config"
)

// Tenant represents a tenant record managed by the admin API.
type Tenant struct {
	ID                  string             `json:"id"`
	LicenseTier         config.LicenseTier `json:"license_tier"`
	BotToken            string             `json:"bot_token,omitempty"`
	GuildIDs            []string           `json:"guild_ids,omitempty"`
	MonthlySessionHours float64            `json:"monthly_session_hours,omitempty"` // 0 = unlimited
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
	ID          string `json:"id"`
	LicenseTier string `json:"license_tier"`
	BotToken    string `json:"bot_token,omitempty"`
}

// TenantUpdateRequest is the JSON body for updating a tenant.
type TenantUpdateRequest struct {
	LicenseTier string   `json:"license_tier,omitempty"`
	BotToken    string   `json:"bot_token,omitempty"`
	GuildIDs    []string `json:"guild_ids,omitempty"`
}

// AdminStore abstracts persistent storage for tenant records.
type AdminStore interface {
	CreateTenant(ctx context.Context, t Tenant) error
	GetTenant(ctx context.Context, id string) (Tenant, error)
	UpdateTenant(ctx context.Context, t Tenant) error
	DeleteTenant(ctx context.Context, id string) error
	ListTenants(ctx context.Context) ([]Tenant, error)
}

// BotConnector manages Discord bot connections for tenants. Implementations
// handle creating, replacing, and removing bot gateway sessions.
type BotConnector interface {
	// ConnectBot creates a Discord bot client for the tenant and opens
	// the gateway connection. If a bot is already connected for this tenant,
	// it is replaced (the old connection is closed gracefully).
	ConnectBot(ctx context.Context, tenantID, botToken string, guildIDs []string) error

	// DisconnectBot removes and closes the bot for the given tenant.
	// It is a no-op if no bot is connected.
	DisconnectBot(tenantID string)
}

// AdminAPI serves the internal admin HTTP API for tenant and session management.
// It listens on a separate port behind a NetworkPolicy in production.
//
// All exported methods are safe for concurrent use.
type AdminAPI struct {
	mux    *http.ServeMux
	store  AdminStore
	apiKey string
	bots   BotConnector // nil = bot management disabled
}

// NewAdminAPI creates an AdminAPI with the given store, API key, and optional
// BotConnector. If bots is nil, tenant bot tokens are stored but not connected.
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

// Handler returns the http.Handler for this admin API, wrapped with auth middleware.
func (a *AdminAPI) Handler() http.Handler {
	return a.authMiddleware(a.mux)
}

// registerRoutes wires all admin API endpoints.
func (a *AdminAPI) registerRoutes() {
	a.mux.HandleFunc("POST /api/v1/tenants", a.createTenant)
	a.mux.HandleFunc("GET /api/v1/tenants", a.listTenants)
	a.mux.HandleFunc("GET /api/v1/tenants/{id}", a.getTenant)
	a.mux.HandleFunc("PUT /api/v1/tenants/{id}", a.updateTenant)
	a.mux.HandleFunc("DELETE /api/v1/tenants/{id}", a.deleteTenant)
}

// authMiddleware validates the API key from the Authorization header.
func (a *AdminAPI) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization")
		if key == "" {
			writeAdminError(w, http.StatusUnauthorized, "missing Authorization header")
			return
		}
		// Support "Bearer <key>" format.
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

// createTenant handles POST /api/v1/tenants.
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

	// Validate tenant ID format (must be valid for PostgreSQL schema names).
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
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := a.store.CreateTenant(r.Context(), tenant); err != nil {
		slog.Warn("admin: create tenant failed", "tenant_id", req.ID, "err", err)
		writeAdminError(w, http.StatusConflict, fmt.Sprintf("tenant %q already exists", req.ID))
		return
	}

	slog.Info("admin: tenant created",
		"admin_id", "api_key",
		"action", "create_tenant",
		"tenant_id", req.ID,
		"license_tier", tier.String(),
	)

	// Connect the Discord bot if a token was provided.
	if a.bots != nil && req.BotToken != "" {
		if err := a.bots.ConnectBot(r.Context(), tenant.ID, tenant.BotToken, tenant.GuildIDs); err != nil {
			slog.Error("admin: failed to connect bot for tenant", "tenant_id", tenant.ID, "err", err)
		}
	}

	// Omit bot token from response.
	tenant.BotToken = ""
	writeAdminJSON(w, http.StatusCreated, tenant)
}

// getTenant handles GET /api/v1/tenants/{id}.
func (a *AdminAPI) getTenant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	tenant, err := a.store.GetTenant(r.Context(), id)
	if err != nil {
		writeAdminError(w, http.StatusNotFound, fmt.Sprintf("tenant %q not found", id))
		return
	}

	// Omit bot token from response.
	tenant.BotToken = ""
	writeAdminJSON(w, http.StatusOK, tenant)
}

// listTenants handles GET /api/v1/tenants.
func (a *AdminAPI) listTenants(w http.ResponseWriter, r *http.Request) {
	tenants, err := a.store.ListTenants(r.Context())
	if err != nil {
		slog.Warn("admin: list tenants failed", "err", err)
		writeAdminError(w, http.StatusInternalServerError, "failed to list tenants")
		return
	}

	// Omit bot tokens from response.
	for i := range tenants {
		tenants[i].BotToken = ""
	}

	writeAdminJSON(w, http.StatusOK, tenants)
}

// updateTenant handles PUT /api/v1/tenants/{id}.
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
	existing.UpdatedAt = time.Now().UTC()

	if err := a.store.UpdateTenant(r.Context(), existing); err != nil {
		slog.Warn("admin: update tenant failed", "tenant_id", id, "err", err)
		writeAdminError(w, http.StatusInternalServerError, "failed to update tenant")
		return
	}

	slog.Info("admin: tenant updated",
		"admin_id", "api_key",
		"action", "update_tenant",
		"tenant_id", id,
	)

	// Reconnect the bot if the token or guild IDs changed.
	if a.bots != nil && existing.BotToken != "" && (req.BotToken != "" || req.GuildIDs != nil) {
		if err := a.bots.ConnectBot(r.Context(), existing.ID, existing.BotToken, existing.GuildIDs); err != nil {
			slog.Error("admin: failed to reconnect bot for tenant", "tenant_id", id, "err", err)
		}
	}

	existing.BotToken = ""
	writeAdminJSON(w, http.StatusOK, existing)
}

// deleteTenant handles DELETE /api/v1/tenants/{id}.
func (a *AdminAPI) deleteTenant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := a.store.DeleteTenant(r.Context(), id); err != nil {
		writeAdminError(w, http.StatusNotFound, fmt.Sprintf("tenant %q not found", id))
		return
	}

	// Disconnect the bot gracefully.
	if a.bots != nil {
		a.bots.DisconnectBot(id)
	}

	slog.Info("admin: tenant deleted",
		"admin_id", "api_key",
		"action", "delete_tenant",
		"tenant_id", id,
	)

	writeAdminJSON(w, http.StatusNoContent, nil)
}

// writeAdminJSON writes a JSON response with the given status code.
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

// adminError is the JSON error response body.
type adminError struct {
	Error string `json:"error"`
}

// writeAdminError writes a JSON error response.
func writeAdminError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(adminError{Error: msg}); err != nil {
		slog.Warn("admin: failed to encode error response", "err", err)
	}
}
