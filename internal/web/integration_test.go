//go:build integration

package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
	"github.com/MrWong99/glyphoxa/internal/web"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testDSN reads the PostgreSQL DSN for integration tests.
// Tests are skipped when the env var is not set.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GLYPHOXA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GLYPHOXA_TEST_POSTGRES_DSN not set; skipping integration test")
	}
	return dsn
}

// sharedStore is initialized once per test binary to avoid concurrent
// migration races on CREATE SCHEMA.
var (
	sharedStore     *web.Store
	sharedPool      *pgxpool.Pool
	sharedStoreOnce sync.Once
	sharedStoreErr  error
)

// setupTestDB returns the shared pgxpool and web.Store. Migrations run
// exactly once. The pool outlives individual tests (closed at process exit).
func setupTestDB(t *testing.T) (*web.Store, *pgxpool.Pool) {
	t.Helper()
	testDSN(t) // skip if DSN not set

	sharedStoreOnce.Do(func() {
		ctx := context.Background()
		dsn := os.Getenv("GLYPHOXA_TEST_POSTGRES_DSN")
		sharedPool, sharedStoreErr = pgxpool.New(ctx, dsn)
		if sharedStoreErr != nil {
			return
		}
		sharedStore, sharedStoreErr = web.NewStore(ctx, sharedPool)
	})
	if sharedStoreErr != nil {
		t.Fatalf("shared store init: %v", sharedStoreErr)
	}
	return sharedStore, sharedPool
}

// jwtSecret is the test secret for JWT signing (≥32 chars).
const jwtSecret = "integration-test-jwt-secret-32ch"

// testAdminKey is the admin API key for test auth.
const testAdminKey = "test-admin-key-for-integration"

// mockNPCStore is a minimal in-memory npcstore.Store for the web server.
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

// setupTestServer creates a full web.Server backed by a real PostgreSQL store.
// Returns the httptest server and a cleanup function.
func setupTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	store, _ := setupTestDB(t)

	cfg := &web.Config{
		DatabaseDSN: testDSN(t),
		JWTSecret:   jwtSecret,
		AdminAPIKey: testAdminKey,
		ListenAddr:  ":0",
	}

	srv := web.NewServer(cfg, store, newMockNPCStore(), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// authenticateAdmin logs in with the admin API key and returns a valid JWT.
func authenticateAdmin(t *testing.T, ts *httptest.Server) string {
	t.Helper()

	body := `{"api_key":"` + testAdminKey + `"}`
	resp, err := http.Post(ts.URL+"/api/v1/auth/apikey", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /api/v1/auth/apikey: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apikey login: status %d", resp.StatusCode)
	}

	var result struct {
		Data struct {
			AccessToken string `json:"access_token"`
			User        struct {
				ID       string `json:"id"`
				Role     string `json:"role"`
				TenantID string `json:"tenant_id"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode apikey response: %v", err)
	}
	if result.Data.AccessToken == "" {
		t.Fatal("empty access token from apikey login")
	}
	return result.Data.AccessToken
}

// doRequest performs an authenticated HTTP request and returns the response.
func doRequest(t *testing.T, method, url, token string, body any) *http.Response {
	t.Helper()

	var bodyReader *bytes.Buffer
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		bodyReader = bytes.NewBuffer(data)
	} else {
		bodyReader = &bytes.Buffer{}
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

// ---------- Auth Flow Integration Tests ----------

// TestIntegration_APIKeyAuth verifies the API key login flow against a real
// PostgreSQL database, including user creation and JWT issuance.
func TestIntegration_APIKeyAuth(t *testing.T) {
	t.Parallel()
	ts := setupTestServer(t)

	t.Run("valid API key returns JWT and user", func(t *testing.T) {
		t.Parallel()

		body := `{"api_key":"` + testAdminKey + `"}`
		resp, err := http.Post(ts.URL+"/api/v1/auth/apikey", "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode: %v", err)
		}

		data, ok := result["data"].(map[string]any)
		if !ok {
			t.Fatal("missing data in response")
		}
		if data["access_token"] == nil || data["access_token"] == "" {
			t.Error("missing access_token")
		}
		if data["token_type"] != "Bearer" {
			t.Errorf("token_type = %v, want Bearer", data["token_type"])
		}

		user, ok := data["user"].(map[string]any)
		if !ok {
			t.Fatal("missing user in response")
		}
		if user["role"] != "super_admin" {
			t.Errorf("role = %v, want super_admin", user["role"])
		}
	})

	t.Run("invalid API key returns 401", func(t *testing.T) {
		t.Parallel()

		body := `{"api_key":"wrong-key"}`
		resp, err := http.Post(ts.URL+"/api/v1/auth/apikey", "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("missing API key in body returns 400", func(t *testing.T) {
		t.Parallel()

		body := `{}`
		resp, err := http.Post(ts.URL+"/api/v1/auth/apikey", "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
	})
}

// TestIntegration_JWTAuthMiddleware verifies JWT authentication middleware
// against a real database, testing token verification and protected routes.
func TestIntegration_JWTAuthMiddleware(t *testing.T) {
	t.Parallel()
	ts := setupTestServer(t)

	t.Run("authenticated request to /auth/me succeeds", func(t *testing.T) {
		t.Parallel()
		token := authenticateAdmin(t, ts)

		resp := doRequest(t, "GET", ts.URL+"/api/v1/auth/me", token, nil)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		data, _ := result["data"].(map[string]any)
		if data == nil {
			t.Fatal("missing data in /me response")
		}
		if data["role"] != "super_admin" {
			t.Errorf("role = %v, want super_admin", data["role"])
		}
	})

	t.Run("unauthenticated request returns 401", func(t *testing.T) {
		t.Parallel()

		resp := doRequest(t, "GET", ts.URL+"/api/v1/auth/me", "", nil)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("invalid token returns 401", func(t *testing.T) {
		t.Parallel()

		resp := doRequest(t, "GET", ts.URL+"/api/v1/auth/me", "invalid.token.here", nil)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("expired token returns 401", func(t *testing.T) {
		t.Parallel()

		expiredToken, err := web.SignJWT(jwtSecret, web.Claims{
			Sub:      "test-user",
			TenantID: "test-tenant",
			Role:     "dm",
			Expires:  time.Now().Add(-1 * time.Hour).Unix(),
		})
		if err != nil {
			t.Fatalf("sign expired JWT: %v", err)
		}

		resp := doRequest(t, "GET", ts.URL+"/api/v1/auth/me", expiredToken, nil)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})
}

// TestIntegration_TokenRefresh verifies that token refresh works against a
// real database, returning updated user info.
func TestIntegration_TokenRefresh(t *testing.T) {
	t.Parallel()
	ts := setupTestServer(t)

	token := authenticateAdmin(t, ts)
	resp := doRequest(t, "POST", ts.URL+"/api/v1/auth/refresh", token, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	data, _ := result["data"].(map[string]any)
	if data == nil || data["access_token"] == nil {
		t.Fatal("missing access_token in refresh response")
	}
}

// ---------- Campaign/NPC/Session Lifecycle Integration Tests ----------

// TestIntegration_CampaignLifecycle tests creating, listing, updating, and
// deleting campaigns through the API with a real PostgreSQL backend.
func TestIntegration_CampaignLifecycle(t *testing.T) {
	t.Parallel()
	ts := setupTestServer(t)
	token := authenticateAdmin(t, ts)

	var campaignID string

	t.Run("create campaign", func(t *testing.T) {
		body := map[string]any{
			"name":        "Curse of Strahd",
			"game_system": "dnd5e",
			"language":    "en",
			"description": "A gothic horror campaign in Barovia",
		}
		resp := doRequest(t, "POST", ts.URL+"/api/v1/campaigns", token, body)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			var errBody json.RawMessage
			json.NewDecoder(resp.Body).Decode(&errBody)
			t.Fatalf("create campaign: status %d, body: %s", resp.StatusCode, errBody)
		}

		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		data, _ := result["data"].(map[string]any)
		if data == nil {
			t.Fatal("missing data in create response")
		}
		id, ok := data["id"].(string)
		if !ok || id == "" {
			t.Fatal("missing campaign ID")
		}
		campaignID = id
	})

	t.Run("get campaign", func(t *testing.T) {
		if campaignID == "" {
			t.Skip("no campaign created")
		}

		resp := doRequest(t, "GET", ts.URL+"/api/v1/campaigns/"+campaignID, token, nil)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get campaign: status %d", resp.StatusCode)
		}

		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		data, _ := result["data"].(map[string]any)
		if data == nil {
			t.Fatal("missing data in get response")
		}
		if data["name"] != "Curse of Strahd" {
			t.Errorf("name = %v, want 'Curse of Strahd'", data["name"])
		}
	})

	t.Run("list campaigns", func(t *testing.T) {
		resp := doRequest(t, "GET", ts.URL+"/api/v1/campaigns", token, nil)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list campaigns: status %d", resp.StatusCode)
		}

		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		data, ok := result["data"].([]any)
		if !ok {
			t.Fatal("data is not an array")
		}
		if len(data) == 0 {
			t.Error("expected at least 1 campaign")
		}
	})

	t.Run("update campaign", func(t *testing.T) {
		if campaignID == "" {
			t.Skip("no campaign created")
		}

		body := map[string]any{
			"name":        "Curse of Strahd (Updated)",
			"game_system": "dnd5e",
			"description": "Updated description",
		}
		resp := doRequest(t, "PUT", ts.URL+"/api/v1/campaigns/"+campaignID, token, body)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("update campaign: status %d", resp.StatusCode)
		}

		// Verify update.
		resp2 := doRequest(t, "GET", ts.URL+"/api/v1/campaigns/"+campaignID, token, nil)
		defer resp2.Body.Close()

		var result map[string]any
		json.NewDecoder(resp2.Body).Decode(&result)
		data, _ := result["data"].(map[string]any)
		if data["name"] != "Curse of Strahd (Updated)" {
			t.Errorf("name after update = %v", data["name"])
		}
	})

	t.Run("delete campaign", func(t *testing.T) {
		if campaignID == "" {
			t.Skip("no campaign created")
		}

		resp := doRequest(t, "DELETE", ts.URL+"/api/v1/campaigns/"+campaignID, token, nil)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
			t.Fatalf("delete campaign: status %d", resp.StatusCode)
		}

		// Verify it's gone (or soft-deleted).
		resp2 := doRequest(t, "GET", ts.URL+"/api/v1/campaigns/"+campaignID, token, nil)
		defer resp2.Body.Close()

		if resp2.StatusCode != http.StatusNotFound {
			t.Errorf("get after delete: status %d, want 404", resp2.StatusCode)
		}
	})
}

// TestIntegration_LoreDocumentLifecycle tests CRUD operations for lore
// documents through the API with a real PostgreSQL backend.
func TestIntegration_LoreDocumentLifecycle(t *testing.T) {
	t.Parallel()
	ts := setupTestServer(t)
	token := authenticateAdmin(t, ts)

	// Create a campaign first.
	campaignBody := map[string]any{"name": "Lore Test Campaign", "game_system": "pf2e"}
	resp := doRequest(t, "POST", ts.URL+"/api/v1/campaigns", token, campaignBody)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create campaign: status %d", resp.StatusCode)
	}

	var campaignResult map[string]any
	json.NewDecoder(resp.Body).Decode(&campaignResult)
	campaignData, _ := campaignResult["data"].(map[string]any)
	campaignID, _ := campaignData["id"].(string)
	if campaignID == "" {
		t.Fatal("missing campaign ID")
	}

	var loreID string

	t.Run("create lore document", func(t *testing.T) {
		body := map[string]any{
			"title":            "The History of Barovia",
			"content_markdown": "# Barovia\n\nBarovia is a land of darkness...",
			"sort_order":       1,
		}
		resp := doRequest(t, "POST", fmt.Sprintf("%s/api/v1/campaigns/%s/lore", ts.URL, campaignID), token, body)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			var errBody json.RawMessage
			json.NewDecoder(resp.Body).Decode(&errBody)
			t.Fatalf("create lore: status %d, body: %s", resp.StatusCode, errBody)
		}

		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		data, _ := result["data"].(map[string]any)
		if id, ok := data["id"].(string); ok {
			loreID = id
		}
	})

	t.Run("list lore documents", func(t *testing.T) {
		resp := doRequest(t, "GET", fmt.Sprintf("%s/api/v1/campaigns/%s/lore", ts.URL, campaignID), token, nil)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list lore: status %d", resp.StatusCode)
		}

		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		data, ok := result["data"].([]any)
		if !ok || len(data) == 0 {
			t.Error("expected at least 1 lore document")
		}
	})

	t.Run("update and get lore document", func(t *testing.T) {
		if loreID == "" {
			t.Skip("no lore document created")
		}

		body := map[string]any{
			"title":            "The Complete History of Barovia",
			"content_markdown": "# Barovia (Updated)\n\nMore details...",
			"sort_order":       2,
		}
		resp := doRequest(t, "PUT", fmt.Sprintf("%s/api/v1/campaigns/%s/lore/%s", ts.URL, campaignID, loreID), token, body)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("update lore: status %d", resp.StatusCode)
		}
	})

	t.Run("delete lore document", func(t *testing.T) {
		if loreID == "" {
			t.Skip("no lore document created")
		}

		resp := doRequest(t, "DELETE", fmt.Sprintf("%s/api/v1/campaigns/%s/lore/%s", ts.URL, campaignID, loreID), token, nil)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
			t.Fatalf("delete lore: status %d", resp.StatusCode)
		}
	})
}

// TestIntegration_RoleBasedAccessControl verifies that role-based access
// control works correctly with real database users.
func TestIntegration_RoleBasedAccessControl(t *testing.T) {
	t.Parallel()
	ts := setupTestServer(t)

	t.Run("viewer cannot create campaigns", func(t *testing.T) {
		t.Parallel()

		// Create a viewer-level JWT.
		viewerToken, err := web.SignJWT(jwtSecret, web.Claims{
			Sub:      "viewer-user-id",
			TenantID: "test-tenant",
			Role:     "viewer",
		})
		if err != nil {
			t.Fatalf("sign viewer JWT: %v", err)
		}

		body := map[string]any{"name": "Forbidden Campaign"}
		resp := doRequest(t, "POST", ts.URL+"/api/v1/campaigns", viewerToken, body)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("dm can create campaigns", func(t *testing.T) {
		t.Parallel()

		// Use admin token (super_admin ≥ dm).
		token := authenticateAdmin(t, ts)

		body := map[string]any{"name": "DM Campaign", "game_system": "dnd5e"}
		resp := doRequest(t, "POST", ts.URL+"/api/v1/campaigns", token, body)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			t.Errorf("status = %d, want 201", resp.StatusCode)
		}
	})

	t.Run("viewer cannot access users list", func(t *testing.T) {
		t.Parallel()

		viewerToken, _ := web.SignJWT(jwtSecret, web.Claims{
			Sub:      "viewer-user-id",
			TenantID: "test-tenant",
			Role:     "viewer",
		})

		resp := doRequest(t, "GET", ts.URL+"/api/v1/users", viewerToken, nil)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
	})
}

// TestIntegration_WebStoreDirectOperations tests the WebStore implementation
// directly against PostgreSQL to verify SQL correctness.
func TestIntegration_WebStoreDirectOperations(t *testing.T) {
	t.Parallel()
	store, _ := setupTestDB(t)

	ctx := context.Background()

	t.Run("ping", func(t *testing.T) {
		t.Parallel()
		if err := store.Ping(ctx); err != nil {
			t.Fatalf("Ping: %v", err)
		}
	})

	t.Run("ensure admin user idempotent", func(t *testing.T) {
		t.Parallel()

		user1, err := store.EnsureAdminUser(ctx, "test-direct")
		if err != nil {
			t.Fatalf("EnsureAdminUser (first): %v", err)
		}
		if user1.Role != "super_admin" {
			t.Errorf("role = %q, want super_admin", user1.Role)
		}

		// Second call should succeed without duplicate key error.
		user2, err := store.EnsureAdminUser(ctx, "test-direct")
		if err != nil {
			t.Fatalf("EnsureAdminUser (second): %v", err)
		}
		if user1.ID != user2.ID {
			t.Errorf("IDs differ: %q vs %q", user1.ID, user2.ID)
		}
	})

	t.Run("upsert discord user", func(t *testing.T) {
		t.Parallel()

		discordID := fmt.Sprintf("discord-%d", time.Now().UnixNano())
		user, err := store.UpsertDiscordUser(ctx, discordID, "test@example.com", "TestUser", "", "tenant-upsert")
		if err != nil {
			t.Fatalf("UpsertDiscordUser: %v", err)
		}
		if user.DisplayName != "TestUser" {
			t.Errorf("DisplayName = %q, want TestUser", user.DisplayName)
		}

		// Update the display name.
		user2, err := store.UpsertDiscordUser(ctx, discordID, "test@example.com", "UpdatedUser", "", "tenant-upsert")
		if err != nil {
			t.Fatalf("UpsertDiscordUser (update): %v", err)
		}
		if user2.DisplayName != "UpdatedUser" {
			t.Errorf("DisplayName after update = %q, want UpdatedUser", user2.DisplayName)
		}
		if user.ID != user2.ID {
			t.Errorf("user IDs differ on upsert: %q vs %q", user.ID, user2.ID)
		}
	})

	t.Run("campaign CRUD", func(t *testing.T) {
		t.Parallel()

		campaign := &web.Campaign{
			TenantID:    "tenant-campaign-crud",
			Name:        "Test Campaign",
			System:      "dnd5e",
			Description: "A test campaign",
		}
		if err := store.CreateCampaign(ctx, campaign); err != nil {
			t.Fatalf("CreateCampaign: %v", err)
		}
		if campaign.ID == "" {
			t.Fatal("campaign ID not set after create")
		}

		got, err := store.GetCampaign(ctx, campaign.TenantID, campaign.ID)
		if err != nil {
			t.Fatalf("GetCampaign: %v", err)
		}
		if got.Name != "Test Campaign" {
			t.Errorf("Name = %q, want 'Test Campaign'", got.Name)
		}

		campaign.Name = "Updated Campaign"
		if err := store.UpdateCampaign(ctx, campaign); err != nil {
			t.Fatalf("UpdateCampaign: %v", err)
		}

		got2, _ := store.GetCampaign(ctx, campaign.TenantID, campaign.ID)
		if got2.Name != "Updated Campaign" {
			t.Errorf("Name after update = %q", got2.Name)
		}

		if err := store.DeleteCampaign(ctx, campaign.TenantID, campaign.ID); err != nil {
			t.Fatalf("DeleteCampaign: %v", err)
		}

		got3, err := store.GetCampaign(ctx, campaign.TenantID, campaign.ID)
		if err != nil {
			t.Fatalf("GetCampaign after delete: %v", err)
		}
		if got3 != nil {
			t.Error("campaign should be nil after deletion")
		}
	})

	t.Run("lore document CRUD", func(t *testing.T) {
		t.Parallel()

		campaign := &web.Campaign{
			TenantID: "tenant-lore-crud",
			Name:     "Lore Campaign",
		}
		if err := store.CreateCampaign(ctx, campaign); err != nil {
			t.Fatalf("CreateCampaign: %v", err)
		}

		doc := &web.LoreDocument{
			CampaignID:      campaign.ID,
			Title:           "Ancient History",
			ContentMarkdown: "# History\nSome content",
			SortOrder:       1,
		}
		if err := store.CreateLoreDocument(ctx, doc); err != nil {
			t.Fatalf("CreateLoreDocument: %v", err)
		}
		if doc.ID == "" {
			t.Fatal("lore document ID not set")
		}

		got, err := store.GetLoreDocument(ctx, campaign.ID, doc.ID)
		if err != nil {
			t.Fatalf("GetLoreDocument: %v", err)
		}
		if got.Title != "Ancient History" {
			t.Errorf("Title = %q", got.Title)
		}

		docs, err := store.ListLoreDocuments(ctx, campaign.ID)
		if err != nil {
			t.Fatalf("ListLoreDocuments: %v", err)
		}
		if len(docs) == 0 {
			t.Error("expected at least 1 lore document")
		}

		if err := store.DeleteLoreDocument(ctx, campaign.ID, doc.ID); err != nil {
			t.Fatalf("DeleteLoreDocument: %v", err)
		}
	})
}

// TestIntegration_CORSHeaders verifies CORS middleware behaves correctly.
func TestIntegration_CORSHeaders(t *testing.T) {
	t.Parallel()
	ts := setupTestServer(t)

	t.Run("preflight request returns CORS headers", func(t *testing.T) {
		t.Parallel()

		req, _ := http.NewRequest("OPTIONS", ts.URL+"/api/v1/campaigns", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("OPTIONS: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("status = %d, want 204", resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Methods"); got == "" {
			t.Error("missing Access-Control-Allow-Methods")
		}
	})
}
