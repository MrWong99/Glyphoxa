package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
)

// Compile-time assertion.
var _ WebStore = (*mockWebStore)(nil)

// mockNPCStore is a simple in-memory implementation of npcstore.Store for tests.
type mockNPCStore struct {
	npcs map[string]*npcstore.NPCDefinition
}

var _ npcstore.Store = (*mockNPCStore)(nil)

func newMockNPCStore() *mockNPCStore {
	return &mockNPCStore{npcs: make(map[string]*npcstore.NPCDefinition)}
}

func (m *mockNPCStore) Create(_ context.Context, def *npcstore.NPCDefinition) error {
	if def.ID == "" {
		def.ID = "npc-" + def.Name
	}
	now := time.Now().UTC()
	def.CreatedAt = now
	def.UpdatedAt = now
	m.npcs[def.ID] = def
	return nil
}

func (m *mockNPCStore) Get(_ context.Context, id string) (*npcstore.NPCDefinition, error) {
	def, ok := m.npcs[id]
	if !ok {
		return nil, nil
	}
	return def, nil
}

func (m *mockNPCStore) Update(_ context.Context, def *npcstore.NPCDefinition) error {
	if _, ok := m.npcs[def.ID]; !ok {
		return nil
	}
	def.UpdatedAt = time.Now().UTC()
	m.npcs[def.ID] = def
	return nil
}

func (m *mockNPCStore) Delete(_ context.Context, id string) error {
	delete(m.npcs, id)
	return nil
}

func (m *mockNPCStore) List(_ context.Context, campaignID string) ([]npcstore.NPCDefinition, error) {
	var result []npcstore.NPCDefinition
	for _, def := range m.npcs {
		if campaignID == "" || def.CampaignID == campaignID {
			result = append(result, *def)
		}
	}
	return result, nil
}

func (m *mockNPCStore) Upsert(_ context.Context, def *npcstore.NPCDefinition) error {
	return m.Create(context.Background(), def)
}

// mockWebStore is a simple in-memory implementation of WebStore for tests.
type mockWebStore struct {
	users       map[string]*User
	campaigns   map[string]*Campaign
	sessions    []SessionSummary
	transcripts map[string][]TranscriptEntry
	usage       []UsageRecord
}

func newMockWebStore() *mockWebStore {
	return &mockWebStore{
		users:       make(map[string]*User),
		campaigns:   make(map[string]*Campaign),
		transcripts: make(map[string][]TranscriptEntry),
	}
}

func (m *mockWebStore) Ping(_ context.Context) error { return nil }

func (m *mockWebStore) UpsertDiscordUser(_ context.Context, discordID, email, displayName, avatarURL, tenantID string) (*User, error) {
	id := "user-" + discordID
	u := &User{ID: id, TenantID: tenantID, DiscordID: discordID, Email: email, DisplayName: displayName, AvatarURL: avatarURL, Role: "dm"}
	m.users[id] = u
	return u, nil
}

func (m *mockWebStore) GetUser(_ context.Context, id string) (*User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, nil
	}
	return u, nil
}

func (m *mockWebStore) CreateCampaign(_ context.Context, c *Campaign) error {
	if c.ID == "" {
		c.ID = "camp-" + c.Name
	}
	m.campaigns[c.ID] = c
	return nil
}

func (m *mockWebStore) GetCampaign(_ context.Context, tenantID, id string) (*Campaign, error) {
	c, ok := m.campaigns[id]
	if !ok || c.TenantID != tenantID {
		return nil, nil
	}
	return c, nil
}

func (m *mockWebStore) ListCampaigns(_ context.Context, tenantID string) ([]Campaign, error) {
	var result []Campaign
	for _, c := range m.campaigns {
		if c.TenantID == tenantID {
			result = append(result, *c)
		}
	}
	return result, nil
}

func (m *mockWebStore) UpdateCampaign(_ context.Context, c *Campaign) error {
	m.campaigns[c.ID] = c
	return nil
}

func (m *mockWebStore) DeleteCampaign(_ context.Context, tenantID, id string) error {
	delete(m.campaigns, id)
	return nil
}

func (m *mockWebStore) ListSessions(_ context.Context, tenantID string, limit, offset int) ([]SessionSummary, error) {
	var filtered []SessionSummary
	for _, s := range m.sessions {
		if s.TenantID == tenantID {
			filtered = append(filtered, s)
		}
	}
	if offset >= len(filtered) {
		return nil, nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end], nil
}

func (m *mockWebStore) GetTranscript(_ context.Context, _, sessionID string) ([]TranscriptEntry, error) {
	entries, ok := m.transcripts[sessionID]
	if !ok {
		return nil, nil
	}
	return entries, nil
}

func (m *mockWebStore) GetUsage(_ context.Context, tenantID string, from, to time.Time) ([]UsageRecord, error) {
	var result []UsageRecord
	for _, r := range m.usage {
		if r.TenantID == tenantID && !r.Period.Before(from) && !r.Period.After(to) {
			result = append(result, r)
		}
	}
	return result, nil
}

// testServer creates a Server with mock stores for testing.
func testServer(t *testing.T) (*Server, string) {
	t.Helper()
	srv, _, _, _ := testServerWithStores(t)
	return srv, "test-jwt-secret"
}

// testServerWithStores creates a Server and returns access to mock stores for seeding data.
func testServerWithStores(t *testing.T) (*Server, *mockWebStore, *mockNPCStore, string) {
	t.Helper()

	secret := "test-jwt-secret"
	cfg := &Config{
		JWTSecret:           secret,
		DiscordClientID:     "test-client-id",
		DiscordClientSecret: "test-client-secret",
		DiscordRedirectURI:  "http://localhost/callback",
	}

	ws := newMockWebStore()
	ns := newMockNPCStore()
	srv := &Server{
		mux:       http.NewServeMux(),
		cfg:       cfg,
		store:     ws,
		npcs:      ns,
		gatewayHC: &http.Client{Timeout: 5 * time.Second},
	}
	return srv, ws, ns, secret
}

func signTestToken(t *testing.T, secret, userID, tenantID, role string) string {
	t.Helper()
	token, err := SignJWT(secret, Claims{
		Sub:      userID,
		TenantID: tenantID,
		Role:     role,
		Expires:  time.Now().Add(1 * time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	return token
}

// authReq creates an HTTP request with a valid JWT Authorization header.
func authReq(t *testing.T, method, path string, body *bytes.Buffer, secret, userID, tenantID, role string) *http.Request {
	t.Helper()
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, body)
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	token := signTestToken(t, secret, userID, tenantID, role)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestHandleDiscordLogin_Redirect(t *testing.T) {
	t.Parallel()

	srv, _ := testServer(t)
	srv.mux.HandleFunc("GET /api/v1/auth/discord", srv.handleDiscordLogin)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/discord", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}

	loc := rr.Header().Get("Location")
	if loc == "" {
		t.Fatal("missing Location header")
	}
	if !bytes.Contains([]byte(loc), []byte("discord.com/oauth2/authorize")) {
		t.Errorf("Location = %q, want discord OAuth URL", loc)
	}
	if !bytes.Contains([]byte(loc), []byte("client_id=test-client-id")) {
		t.Errorf("Location missing client_id, got %q", loc)
	}

	// Should set state cookie.
	cookies := rr.Result().Cookies()
	var stateCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "glyphoxa_oauth_state" {
			stateCookie = c
			break
		}
	}
	if stateCookie == nil {
		t.Fatal("missing glyphoxa_oauth_state cookie")
	}
	if stateCookie.Value == "" {
		t.Fatal("empty state cookie value")
	}
	if !stateCookie.Secure {
		t.Error("state cookie should have Secure flag set")
	}
}

func TestHandleMe_Unauthenticated(t *testing.T) {
	t.Parallel()

	srv, secret := testServer(t)
	auth := AuthMiddleware(secret)
	srv.mux.Handle("GET /api/v1/auth/me", auth(http.HandlerFunc(srv.handleMe)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	writeJSON(rr, http.StatusOK, map[string]string{"hello": "world"})

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	ct := rr.Header().Get("Content-Type")
	if ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", ct)
	}

	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["hello"] != "world" {
		t.Errorf("body = %v, want {hello: world}", body)
	}
}

func TestWriteError(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	writeError(rr, http.StatusNotFound, "not_found", "campaign not found")

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "not_found" {
		t.Errorf("error code = %q, want %q", body.Error.Code, "not_found")
	}
	if body.Error.Message != "campaign not found" {
		t.Errorf("error message = %q, want %q", body.Error.Message, "campaign not found")
	}
}

func TestConfigValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid",
			cfg: Config{
				DatabaseDSN:         "postgres://localhost/test",
				JWTSecret:           "a-very-long-jwt-secret-that-is-at-least-32-chars",
				DiscordClientID:     "id",
				DiscordClientSecret: "secret",
				DiscordRedirectURI:  "http://localhost/callback",
			},
			wantErr: false,
		},
		{
			name: "jwt secret too short",
			cfg: Config{
				DatabaseDSN:         "postgres://localhost/test",
				JWTSecret:           "short",
				DiscordClientID:     "id",
				DiscordClientSecret: "secret",
				DiscordRedirectURI:  "http://localhost/callback",
			},
			wantErr: true,
		},
		{
			name:    "empty",
			cfg:     Config{},
			wantErr: true,
		},
		{
			name: "missing jwt secret",
			cfg: Config{
				DatabaseDSN:         "postgres://localhost/test",
				DiscordClientID:     "id",
				DiscordClientSecret: "secret",
				DiscordRedirectURI:  "http://localhost/callback",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}
