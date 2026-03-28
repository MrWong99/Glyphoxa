package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

func (m *mockNPCStore) Get(_ context.Context, tenantID, id, campaignID string) (*npcstore.NPCDefinition, error) {
	def, ok := m.npcs[id]
	if !ok {
		return nil, nil
	}
	if tenantID != "" && def.TenantID != tenantID {
		return nil, nil
	}
	if campaignID != "" && def.CampaignID != campaignID {
		return nil, nil
	}
	return def, nil
}

func (m *mockNPCStore) Update(_ context.Context, def *npcstore.NPCDefinition) error {
	existing, ok := m.npcs[def.ID]
	if !ok {
		return nil
	}
	if def.TenantID != "" && existing.TenantID != def.TenantID {
		return nil
	}
	def.UpdatedAt = time.Now().UTC()
	m.npcs[def.ID] = def
	return nil
}

func (m *mockNPCStore) Delete(_ context.Context, tenantID, id, campaignID string) error {
	if def, ok := m.npcs[id]; ok {
		if tenantID != "" && def.TenantID != tenantID {
			return nil
		}
		if campaignID != "" && def.CampaignID != campaignID {
			return nil
		}
	}
	delete(m.npcs, id)
	return nil
}

func (m *mockNPCStore) List(_ context.Context, tenantID, campaignID string) ([]npcstore.NPCDefinition, error) {
	var result []npcstore.NPCDefinition
	for _, def := range m.npcs {
		if tenantID != "" && def.TenantID != tenantID {
			continue
		}
		if campaignID == "" || def.CampaignID == campaignID {
			result = append(result, *def)
		}
	}
	return result, nil
}

func (m *mockNPCStore) Upsert(_ context.Context, def *npcstore.NPCDefinition) error {
	return m.Create(context.Background(), def)
}

// mockVoicePreviewProvider is a mock VoicePreviewProvider for tests.
type mockVoicePreviewProvider struct{}

// Compile-time assertion.
var _ VoicePreviewProvider = (*mockVoicePreviewProvider)(nil)

func (m *mockVoicePreviewProvider) SynthesizePreview(_ context.Context, _ string, _ npcstore.VoiceConfig) ([]byte, string, error) {
	return []byte("fake-audio-bytes"), "audio/mpeg", nil
}

// mockWebStore is a simple in-memory implementation of WebStore for tests.
type mockWebStore struct {
	users             map[string]*User
	campaigns         map[string]*Campaign
	sessions          []SessionSummary
	transcripts       map[string][]TranscriptEntry
	usage             []UsageRecord
	invites           map[string]*Invite
	loreDocs          map[string]*LoreDocument
	campaignNPCLinks  map[string][]CampaignNPCLink // keyed by campaign_id
	knowledgeEntities map[string][]KnowledgeEntity // keyed by campaign_id
	auditLogs         []AuditLogEntry
}

func newMockWebStore() *mockWebStore {
	return &mockWebStore{
		users:             make(map[string]*User),
		campaigns:         make(map[string]*Campaign),
		transcripts:       make(map[string][]TranscriptEntry),
		invites:           make(map[string]*Invite),
		loreDocs:          make(map[string]*LoreDocument),
		campaignNPCLinks:  make(map[string][]CampaignNPCLink),
		knowledgeEntities: make(map[string][]KnowledgeEntity),
	}
}

func (m *mockWebStore) Ping(_ context.Context) error { return nil }

// strPtr returns a pointer to the given string.
func strPtr(s string) *string { return &s }

func (m *mockWebStore) UpsertDiscordUser(_ context.Context, discordID, email, displayName, avatarURL, tenantID string) (*User, error) {
	id := "user-" + discordID
	u := &User{ID: id, TenantID: tenantID, DiscordID: strPtr(discordID), Email: strPtr(email), DisplayName: displayName, AvatarURL: strPtr(avatarURL), Role: "dm"}
	m.users[id] = u
	return u, nil
}

func (m *mockWebStore) UpsertGoogleUser(_ context.Context, googleID, email, displayName, avatarURL, tenantID string) (*User, error) {
	id := "user-google-" + googleID
	u := &User{ID: id, TenantID: tenantID, GoogleID: strPtr(googleID), Email: strPtr(email), DisplayName: displayName, AvatarURL: strPtr(avatarURL), Role: "dm"}
	m.users[id] = u
	return u, nil
}

func (m *mockWebStore) UpsertGitHubUser(_ context.Context, githubID, email, displayName, avatarURL, tenantID string) (*User, error) {
	id := "user-github-" + githubID
	u := &User{ID: id, TenantID: tenantID, GitHubID: strPtr(githubID), Email: strPtr(email), DisplayName: displayName, AvatarURL: strPtr(avatarURL), Role: "dm"}
	m.users[id] = u
	return u, nil
}

func (m *mockWebStore) EnsureAdminUser(_ context.Context, tenantID string) (*User, error) {
	u := &User{ID: adminUserID, TenantID: tenantID, DisplayName: "Admin", Role: "super_admin"}
	m.users[adminUserID] = u
	return u, nil
}

func (m *mockWebStore) GetUser(_ context.Context, tenantID, id string) (*User, error) {
	u, ok := m.users[id]
	if !ok || u.TenantID != tenantID {
		return nil, nil
	}
	return u, nil
}

func (m *mockWebStore) ListUsers(_ context.Context, tenantID, role string, limit, offset int) ([]User, int, error) {
	var filtered []User
	for _, u := range m.users {
		if u.TenantID != tenantID {
			continue
		}
		if role != "" && u.Role != role {
			continue
		}
		filtered = append(filtered, *u)
	}
	total := len(filtered)
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	if offset >= len(filtered) {
		return nil, total, nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end], total, nil
}

func (m *mockWebStore) UpdateUser(_ context.Context, u *User) error {
	existing, ok := m.users[u.ID]
	if !ok || (u.TenantID != "" && existing.TenantID != u.TenantID) {
		return fmt.Errorf("web: user %q not found", u.ID)
	}
	if u.DisplayName != "" {
		existing.DisplayName = u.DisplayName
	}
	if u.Role != "" {
		existing.Role = u.Role
	}
	existing.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *mockWebStore) UpdateUserTenant(_ context.Context, userID, tenantID, role string) error {
	u, ok := m.users[userID]
	if !ok {
		return fmt.Errorf("web: user %q not found", userID)
	}
	u.TenantID = tenantID
	u.Role = role
	u.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *mockWebStore) DeleteUser(_ context.Context, tenantID, id string) error {
	u, ok := m.users[id]
	if !ok || u.TenantID != tenantID {
		return fmt.Errorf("web: user %q not found", id)
	}
	delete(m.users, id)
	return nil
}

func (m *mockWebStore) UpdateUserPreferences(_ context.Context, id string, prefs json.RawMessage) (*User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, nil
	}
	// Simple merge: just overwrite for test purposes.
	existing := map[string]any{}
	if u.Preferences != nil {
		_ = json.Unmarshal(u.Preferences, &existing)
	}
	incoming := map[string]any{}
	_ = json.Unmarshal(prefs, &incoming)
	for k, v := range incoming {
		existing[k] = v
	}
	merged, _ := json.Marshal(existing)
	u.Preferences = merged
	u.UpdatedAt = time.Now().UTC()
	return u, nil
}

func (m *mockWebStore) CreateInvite(_ context.Context, inv *Invite) error {
	if inv.ID == "" {
		inv.ID = "inv-" + inv.TenantID
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	inv.Token = hex.EncodeToString(b)
	inv.CreatedAt = time.Now().UTC()
	if inv.ExpiresAt.IsZero() {
		inv.ExpiresAt = inv.CreatedAt.Add(7 * 24 * time.Hour)
	}
	m.invites[inv.ID] = inv
	return nil
}

func (m *mockWebStore) GetInviteByToken(_ context.Context, token string) (*Invite, error) {
	for _, inv := range m.invites {
		if inv.Token == token && inv.UsedAt == nil {
			return inv, nil
		}
	}
	return nil, nil
}

func (m *mockWebStore) UseInvite(_ context.Context, inviteID, userID string) error {
	inv, ok := m.invites[inviteID]
	if !ok || inv.UsedAt != nil {
		return fmt.Errorf("web: invite %q not found or already used", inviteID)
	}
	now := time.Now().UTC()
	inv.UsedBy = &userID
	inv.UsedAt = &now
	return nil
}

func (m *mockWebStore) CreateCampaign(_ context.Context, c *Campaign) error {
	if c.ID == "" {
		c.ID = "camp-" + c.Name
	}
	now := time.Now().UTC()
	c.CreatedAt = now
	c.UpdatedAt = now
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

func (m *mockWebStore) ListCampaigns(_ context.Context, tenantID string, _ CursorPage) ([]Campaign, error) {
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

func (m *mockWebStore) DeleteCampaign(_ context.Context, _, id string) error {
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

func (m *mockWebStore) SessionExists(_ context.Context, tenantID, sessionID string) (bool, error) {
	for _, s := range m.sessions {
		if s.ID == sessionID && s.TenantID == tenantID {
			return true, nil
		}
	}
	return false, nil
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

// Lore document mocks.

func (m *mockWebStore) CreateLoreDocument(_ context.Context, _ string, doc *LoreDocument) error {
	if doc.ID == "" {
		doc.ID = "lore-" + doc.Title
	}
	now := time.Now().UTC()
	doc.CreatedAt = now
	doc.UpdatedAt = now
	m.loreDocs[doc.ID] = doc
	return nil
}

func (m *mockWebStore) GetLoreDocument(_ context.Context, _, campaignID, id string) (*LoreDocument, error) {
	doc, ok := m.loreDocs[id]
	if !ok || doc.CampaignID != campaignID {
		return nil, nil
	}
	return doc, nil
}

func (m *mockWebStore) ListLoreDocuments(_ context.Context, _, campaignID string) ([]LoreDocument, error) {
	var result []LoreDocument
	for _, doc := range m.loreDocs {
		if doc.CampaignID == campaignID {
			result = append(result, *doc)
		}
	}
	return result, nil
}

func (m *mockWebStore) UpdateLoreDocument(_ context.Context, _ string, doc *LoreDocument) error {
	existing, ok := m.loreDocs[doc.ID]
	if !ok || existing.CampaignID != doc.CampaignID {
		return fmt.Errorf("web: lore document %q not found", doc.ID)
	}
	doc.UpdatedAt = time.Now().UTC()
	m.loreDocs[doc.ID] = doc
	return nil
}

func (m *mockWebStore) DeleteLoreDocument(_ context.Context, _, campaignID, id string) error {
	doc, ok := m.loreDocs[id]
	if !ok || doc.CampaignID != campaignID {
		return fmt.Errorf("web: lore document %q not found", id)
	}
	delete(m.loreDocs, id)
	return nil
}

// Campaign-NPC link mocks.

func (m *mockWebStore) LinkNPCToCampaign(_ context.Context, campaignID, npcID string) error {
	for _, link := range m.campaignNPCLinks[campaignID] {
		if link.NPCID == npcID {
			return nil // ON CONFLICT DO NOTHING
		}
	}
	m.campaignNPCLinks[campaignID] = append(m.campaignNPCLinks[campaignID], CampaignNPCLink{
		CampaignID: campaignID,
		NPCID:      npcID,
		CreatedAt:  time.Now().UTC(),
	})
	return nil
}

func (m *mockWebStore) UnlinkNPCFromCampaign(_ context.Context, campaignID, npcID string) error {
	links := m.campaignNPCLinks[campaignID]
	for i, link := range links {
		if link.NPCID == npcID {
			m.campaignNPCLinks[campaignID] = append(links[:i], links[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("web: link not found for NPC %q in campaign %q", npcID, campaignID)
}

func (m *mockWebStore) ListCampaignNPCLinks(_ context.Context, campaignID string) ([]CampaignNPCLink, error) {
	return m.campaignNPCLinks[campaignID], nil
}

// Knowledge entity mocks.

func (m *mockWebStore) ListKnowledgeEntities(_ context.Context, _, campaignID string, _ CursorPage) ([]KnowledgeEntity, error) {
	return m.knowledgeEntities[campaignID], nil
}

func (m *mockWebStore) DeleteKnowledgeEntity(_ context.Context, _, campaignID, entityID string) error {
	entities := m.knowledgeEntities[campaignID]
	for i, e := range entities {
		if e.ID == entityID {
			m.knowledgeEntities[campaignID] = append(entities[:i], entities[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("web: knowledge entity %q not found", entityID)
}

func (m *mockWebStore) GetDashboardStats(_ context.Context, tenantID string) (*DashboardStats, error) {
	stats := &DashboardStats{}
	for _, c := range m.campaigns {
		if c.TenantID == tenantID {
			stats.CampaignCount++
		}
	}
	for _, s := range m.sessions {
		if s.TenantID == tenantID && s.State == "running" {
			stats.ActiveSessionCount++
		}
	}
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for _, u := range m.usage {
		if u.TenantID == tenantID && !u.Period.Before(monthStart) {
			stats.HoursUsed += u.SessionHours
		}
	}
	return stats, nil
}

func (m *mockWebStore) GetRecentActivity(_ context.Context, tenantID string, limit int) ([]ActivityItem, error) {
	var items []ActivityItem
	for _, c := range m.campaigns {
		if c.TenantID == tenantID {
			items = append(items, ActivityItem{ID: c.ID, Type: "campaign_created", Description: "Campaign created: " + c.Name, Timestamp: c.CreatedAt, CampaignID: c.ID})
		}
	}
	for _, s := range m.sessions {
		if s.TenantID == tenantID {
			if s.State == "running" {
				items = append(items, ActivityItem{ID: s.ID, Type: "session_started", Description: "Session started", Timestamp: s.StartedAt})
			} else if s.EndedAt != nil {
				items = append(items, ActivityItem{ID: s.ID, Type: "session_ended", Description: "Session ended", Timestamp: *s.EndedAt})
			}
		}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// Audit log mocks.

func (m *mockWebStore) CreateAuditLog(_ context.Context, entry *AuditLogEntry) error {
	m.auditLogs = append(m.auditLogs, *entry)
	return nil
}

func (m *mockWebStore) ListAuditLogs(_ context.Context, tenantID string, limit, offset int, resourceType, action string) ([]AuditLogEntry, int, error) {
	var filtered []AuditLogEntry
	for _, e := range m.auditLogs {
		if tenantID != "" && (e.TenantID == nil || *e.TenantID != tenantID) {
			continue
		}
		if resourceType != "" && e.ResourceType != resourceType {
			continue
		}
		if action != "" && e.Action != action {
			continue
		}
		filtered = append(filtered, e)
	}
	total := len(filtered)
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	if offset >= len(filtered) {
		return nil, total, nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end], total, nil
}

func (m *mockWebStore) GetAdminDashboardStats(_ context.Context) (*AdminDashboardStats, error) {
	return &AdminDashboardStats{
		TotalTenants:      1,
		TotalUsers:        len(m.users),
		TotalCampaigns:    len(m.campaigns),
		ActiveSessions:    0,
		TotalSessionHours: 0,
		AuditLogCount:     len(m.auditLogs),
	}, nil
}

func (m *mockWebStore) ListAllTenantUsers(_ context.Context, limit, offset int) ([]User, int, error) {
	var all []User
	for _, u := range m.users {
		all = append(all, *u)
	}
	total := len(all)
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	if offset >= len(all) {
		return nil, total, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], total, nil
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
		mux:            http.NewServeMux(),
		cfg:            cfg,
		store:          ws,
		npcs:           ns,
		voicePreview:   &mockVoicePreviewProvider{},
		voicePreviewRL: newVoicePreviewRateLimiter(5, time.Minute),
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
			name: "valid with discord",
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
			name: "valid with apikey only",
			cfg: Config{
				DatabaseDSN: "postgres://localhost/test",
				JWTSecret:   "a-very-long-jwt-secret-that-is-at-least-32-chars",
				AdminAPIKey: "my-admin-key",
			},
			wantErr: false,
		},
		{
			name: "valid with both",
			cfg: Config{
				DatabaseDSN:         "postgres://localhost/test",
				JWTSecret:           "a-very-long-jwt-secret-that-is-at-least-32-chars",
				DiscordClientID:     "id",
				DiscordClientSecret: "secret",
				DiscordRedirectURI:  "http://localhost/callback",
				AdminAPIKey:         "my-admin-key",
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
			name:    "empty - no auth method",
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
		{
			name: "no auth method configured",
			cfg: Config{
				DatabaseDSN: "postgres://localhost/test",
				JWTSecret:   "a-very-long-jwt-secret-that-is-at-least-32-chars",
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
