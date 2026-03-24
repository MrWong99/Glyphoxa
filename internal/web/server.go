package web

import (
	"encoding/json"
	"net/http"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
	"github.com/MrWong99/glyphoxa/internal/health"
)

type Server struct {
	mux      *http.ServeMux
	cfg      *Config
	store    WebStore
	npcs     npcstore.Store
	gwClient pb.ManagementServiceClient
}

func NewServer(cfg *Config, store WebStore, npcs npcstore.Store, gwClient pb.ManagementServiceClient) *Server {
	s := &Server{mux: http.NewServeMux(), cfg: cfg, store: store, npcs: npcs, gwClient: gwClient}
	s.registerRoutes()
	return s
}

func (s *Server) Handler() http.Handler {
	var h http.Handler = s.mux
	h = MaxBytesMiddleware(h)
	h = CORSMiddleware(s.cfg.AllowedOrigins)(h)
	h = LoggingMiddleware(h)
	return h
}

func (s *Server) registerRoutes() {
	hc := health.New(health.Checker{Name: "database", Check: s.store.Ping})
	hc.Register(s.mux)
	s.mux.HandleFunc("GET /api/v1/auth/discord", s.handleDiscordLogin)
	s.mux.HandleFunc("GET /api/v1/auth/discord/callback", s.handleDiscordCallback)
	s.mux.HandleFunc("POST /api/v1/auth/apikey", s.handleAPIKeyLogin)
	auth := AuthMiddleware(s.cfg.JWTSecret)
	s.mux.Handle("POST /api/v1/auth/refresh", auth(http.HandlerFunc(s.handleRefresh)))
	s.mux.Handle("GET /api/v1/auth/me", auth(http.HandlerFunc(s.handleMe)))
	s.mux.Handle("POST /api/v1/campaigns", auth(RequireRole("dm")(http.HandlerFunc(s.handleCreateCampaign))))
	s.mux.Handle("GET /api/v1/campaigns", auth(http.HandlerFunc(s.handleListCampaigns)))
	s.mux.Handle("GET /api/v1/campaigns/{id}", auth(http.HandlerFunc(s.handleGetCampaign)))
	s.mux.Handle("PUT /api/v1/campaigns/{id}", auth(RequireRole("dm")(http.HandlerFunc(s.handleUpdateCampaign))))
	s.mux.Handle("DELETE /api/v1/campaigns/{id}", auth(RequireRole("dm")(http.HandlerFunc(s.handleDeleteCampaign))))
	s.mux.Handle("POST /api/v1/campaigns/{id}/npcs", auth(RequireRole("dm")(http.HandlerFunc(s.handleCreateNPC))))
	s.mux.Handle("GET /api/v1/campaigns/{id}/npcs", auth(http.HandlerFunc(s.handleListNPCs)))
	s.mux.Handle("GET /api/v1/campaigns/{id}/npcs/{npc_id}", auth(http.HandlerFunc(s.handleGetNPC)))
	s.mux.Handle("PUT /api/v1/campaigns/{id}/npcs/{npc_id}", auth(RequireRole("dm")(http.HandlerFunc(s.handleUpdateNPC))))
	s.mux.Handle("DELETE /api/v1/campaigns/{id}/npcs/{npc_id}", auth(RequireRole("dm")(http.HandlerFunc(s.handleDeleteNPC))))
	s.mux.Handle("GET /api/v1/sessions", auth(http.HandlerFunc(s.handleListSessions)))
	s.mux.Handle("GET /api/v1/sessions/{id}/transcript", auth(http.HandlerFunc(s.handleGetTranscript)))
	s.mux.Handle("POST /api/v1/sessions/start", auth(RequireRole("dm")(http.HandlerFunc(s.handleStartSession))))
	s.mux.Handle("POST /api/v1/sessions/{id}/stop", auth(RequireRole("dm")(http.HandlerFunc(s.handleStopSession))))
	s.mux.Handle("GET /api/v1/sessions/active", auth(http.HandlerFunc(s.handleListActiveSessions)))
	s.mux.Handle("GET /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(s.handleListTenants))))
	s.mux.Handle("GET /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(s.handleGetTenant))))
	s.mux.Handle("POST /api/v1/tenants", auth(RequireRole("super_admin")(http.HandlerFunc(s.handleCreateTenant))))
	s.mux.Handle("PUT /api/v1/tenants/{id}", auth(RequireRole("tenant_admin")(http.HandlerFunc(s.handleUpdateTenant))))
	s.mux.Handle("DELETE /api/v1/tenants/{id}", auth(RequireRole("super_admin")(http.HandlerFunc(s.handleDeleteTenant))))
	s.mux.Handle("GET /api/v1/usage", auth(http.HandlerFunc(s.handleGetUsage)))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, `{"error":{"code":"encoding_error","message":"failed to encode response"}}`, http.StatusInternalServerError)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}
