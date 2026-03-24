package web

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
	"github.com/MrWong99/glyphoxa/internal/health"
)

// Server is the HTTP server for the web management service.
type Server struct {
	mux       *http.ServeMux
	cfg       *Config
	store     WebStore
	npcs      npcstore.Store
	gatewayHC *http.Client // HTTP client for gateway proxy requests.
}

// NewServer creates a Server and registers all routes.
func NewServer(cfg *Config, store WebStore, npcs npcstore.Store) *Server {
	s := &Server{
		mux:   http.NewServeMux(),
		cfg:   cfg,
		store: store,
		npcs:  npcs,
		gatewayHC: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
	s.registerRoutes()
	return s
}

// Handler returns the root http.Handler with all middleware applied.
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.mux
	h = MaxBytesMiddleware(h)
	h = CORSMiddleware(s.cfg.AllowedOrigins)(h)
	h = LoggingMiddleware(h)
	return h
}

func (s *Server) registerRoutes() {
	// Health probes (unauthenticated).
	hc := health.New(health.Checker{
		Name:  "database",
		Check: s.store.Ping,
	})
	hc.Register(s.mux)

	// Auth endpoints (unauthenticated).
	s.mux.HandleFunc("GET /api/v1/auth/discord", s.handleDiscordLogin)
	s.mux.HandleFunc("GET /api/v1/auth/discord/callback", s.handleDiscordCallback)

	// Authenticated endpoints — wrap with auth middleware.
	auth := AuthMiddleware(s.cfg.JWTSecret)

	// Auth management.
	s.mux.Handle("POST /api/v1/auth/refresh", auth(http.HandlerFunc(s.handleRefresh)))
	s.mux.Handle("GET /api/v1/auth/me", auth(http.HandlerFunc(s.handleMe)))

	// Campaigns.
	s.mux.Handle("POST /api/v1/campaigns", auth(RequireRole("dm")(http.HandlerFunc(s.handleCreateCampaign))))
	s.mux.Handle("GET /api/v1/campaigns", auth(http.HandlerFunc(s.handleListCampaigns)))
	s.mux.Handle("GET /api/v1/campaigns/{id}", auth(http.HandlerFunc(s.handleGetCampaign)))
	s.mux.Handle("PUT /api/v1/campaigns/{id}", auth(RequireRole("dm")(http.HandlerFunc(s.handleUpdateCampaign))))
	s.mux.Handle("DELETE /api/v1/campaigns/{id}", auth(RequireRole("dm")(http.HandlerFunc(s.handleDeleteCampaign))))

	// NPCs (nested under campaigns).
	s.mux.Handle("POST /api/v1/campaigns/{id}/npcs", auth(RequireRole("dm")(http.HandlerFunc(s.handleCreateNPC))))
	s.mux.Handle("GET /api/v1/campaigns/{id}/npcs", auth(http.HandlerFunc(s.handleListNPCs)))
	s.mux.Handle("GET /api/v1/campaigns/{id}/npcs/{npc_id}", auth(http.HandlerFunc(s.handleGetNPC)))
	s.mux.Handle("PUT /api/v1/campaigns/{id}/npcs/{npc_id}", auth(RequireRole("dm")(http.HandlerFunc(s.handleUpdateNPC))))
	s.mux.Handle("DELETE /api/v1/campaigns/{id}/npcs/{npc_id}", auth(RequireRole("dm")(http.HandlerFunc(s.handleDeleteNPC))))

	// Sessions.
	s.mux.Handle("GET /api/v1/sessions", auth(http.HandlerFunc(s.handleListSessions)))
	s.mux.Handle("GET /api/v1/sessions/{id}/transcript", auth(http.HandlerFunc(s.handleGetTranscript)))

	// Tenants (admin-only, proxied to gateway).
	s.mux.Handle("GET /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(s.handleListTenants))))
	s.mux.Handle("GET /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(s.handleGetTenant))))
	s.mux.Handle("POST /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(s.handleCreateTenant))))
	s.mux.Handle("PUT /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(s.handleUpdateTenant))))
	s.mux.Handle("DELETE /api/v1/tenants/{id}", auth(RequireRole("super_admin")(http.HandlerFunc(s.handleDeleteTenant))))

	// Usage.
	s.mux.Handle("GET /api/v1/usage", auth(http.HandlerFunc(s.handleGetUsage)))
}

// writeJSON encodes v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, `{"error":{"code":"encoding_error","message":"failed to encode response"}}`, http.StatusInternalServerError)
	}
}

// writeError writes a structured error response matching the API convention.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}
