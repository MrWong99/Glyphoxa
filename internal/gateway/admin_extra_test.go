package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/gateway"
)

// mockBotConnector implements BotConnector for testing admin API bot interactions.
type mockBotConnector struct {
	connectErr    error
	connectCalled bool
	disconnectIDs []string
}

func (m *mockBotConnector) ConnectBot(_ context.Context, _, _ string, _ []string) error {
	m.connectCalled = true
	return m.connectErr
}

func (m *mockBotConnector) DisconnectBot(tenantID string) {
	m.disconnectIDs = append(m.disconnectIDs, tenantID)
}

// mockTenantBotConnector implements TenantBotConnector for testing.
type mockTenantBotConnector struct {
	mockBotConnector
	connectForTenantCalled bool
	connectForTenantErr    error
}

func (m *mockTenantBotConnector) ConnectBotForTenant(_ context.Context, _ gateway.Tenant) error {
	m.connectForTenantCalled = true
	return m.connectForTenantErr
}

func TestAdminAPI_CreateTenant_InvalidJSON(t *testing.T) {
	t.Parallel()

	api, _ := newTestAdminAPI(t)
	handler := api.Handler()

	req := httptest.NewRequest("POST", "/api/v1/tenants", strings.NewReader("{invalid json"))
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusBadRequest)
	}

	var errResp struct{ Error string }
	if err := json.Unmarshal(rr.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if errResp.Error == "" {
		t.Error("expected error message in response")
	}
}

func TestAdminAPI_CreateTenant_WithBotConnector(t *testing.T) {
	t.Parallel()

	store := gateway.NewMemAdminStore()
	connector := &mockBotConnector{}
	api := gateway.NewAdminAPI(store, testAPIKey, connector)
	handler := api.Handler()

	rr := doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{ID: "withbot", LicenseTier: "shared", BotToken: "secret-token"})

	if rr.Code != http.StatusCreated {
		t.Fatalf("got status %d, want 201; body: %s", rr.Code, rr.Body.String())
	}

	if !connector.connectCalled {
		t.Error("expected ConnectBot to be called when bot token is provided")
	}

	// Verify bot token is stripped from response.
	if bytes.Contains(rr.Body.Bytes(), []byte("secret-token")) {
		t.Error("response should not contain bot token")
	}
}

func TestAdminAPI_CreateTenant_BotConnectError(t *testing.T) {
	t.Parallel()

	store := gateway.NewMemAdminStore()
	connector := &mockBotConnector{connectErr: errors.New("connection failed")}
	api := gateway.NewAdminAPI(store, testAPIKey, connector)
	handler := api.Handler()

	// Create should still succeed even if bot connection fails.
	rr := doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{ID: "botfail", LicenseTier: "shared", BotToken: "tok"})

	if rr.Code != http.StatusCreated {
		t.Errorf("got status %d, want 201; bot connect failure should not prevent creation", rr.Code)
	}
}

func TestAdminAPI_CreateTenant_NoBotTokenSkipsConnect(t *testing.T) {
	t.Parallel()

	store := gateway.NewMemAdminStore()
	connector := &mockBotConnector{}
	api := gateway.NewAdminAPI(store, testAPIKey, connector)
	handler := api.Handler()

	rr := doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{ID: "notoken", LicenseTier: "shared"})

	if rr.Code != http.StatusCreated {
		t.Fatalf("got status %d, want 201", rr.Code)
	}

	if connector.connectCalled {
		t.Error("ConnectBot should not be called when no bot token is provided")
	}
}

func TestAdminAPI_UpdateTenant_InvalidJSON(t *testing.T) {
	t.Parallel()

	api, _ := newTestAdminAPI(t)
	handler := api.Handler()

	// Create tenant first.
	doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{ID: "updjson", LicenseTier: "shared"})

	req := httptest.NewRequest("PUT", "/api/v1/tenants/updjson", strings.NewReader("{bad"))
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestAdminAPI_UpdateTenant_InvalidTier(t *testing.T) {
	t.Parallel()

	api, _ := newTestAdminAPI(t)
	handler := api.Handler()

	doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{ID: "updtier", LicenseTier: "shared"})

	rr := doRequest(t, handler, "PUT", "/api/v1/tenants/updtier",
		gateway.TenantUpdateRequest{LicenseTier: "enterprise"})

	if rr.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want %d for invalid tier", rr.Code, http.StatusBadRequest)
	}
}

func TestAdminAPI_UpdateTenant_AllFields(t *testing.T) {
	t.Parallel()

	store := gateway.NewMemAdminStore()
	connector := &mockBotConnector{}
	api := gateway.NewAdminAPI(store, testAPIKey, connector)
	handler := api.Handler()

	doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{ID: "updall", LicenseTier: "shared"})

	dmRole := "role-123"
	campaignID := "campaign-456"
	rr := doRequest(t, handler, "PUT", "/api/v1/tenants/updall",
		gateway.TenantUpdateRequest{
			LicenseTier: "dedicated",
			BotToken:    "new-token",
			GuildIDs:    []string{"guild-1", "guild-2"},
			DMRoleID:    &dmRole,
			CampaignID:  &campaignID,
		})

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Bot should be reconnected since token and guild IDs changed.
	if !connector.connectCalled {
		t.Error("expected ConnectBot to be called after update with bot token")
	}

	// Verify response does not contain bot token.
	if bytes.Contains(rr.Body.Bytes(), []byte("new-token")) {
		t.Error("response should not contain bot token")
	}

	// Verify the tier was updated in the response.
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["license_tier"] != "dedicated" {
		t.Errorf("got license_tier %v, want %q", resp["license_tier"], "dedicated")
	}
}

func TestAdminAPI_UpdateTenant_ClearOptionalFields(t *testing.T) {
	t.Parallel()

	api, _ := newTestAdminAPI(t)
	handler := api.Handler()

	doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{
			ID:          "clearme",
			LicenseTier: "shared",
			DMRoleID:    "some-role",
			CampaignID:  "some-campaign",
		})

	// Clear DMRoleID and CampaignID by setting them to empty strings.
	empty := ""
	rr := doRequest(t, handler, "PUT", "/api/v1/tenants/clearme",
		gateway.TenantUpdateRequest{
			DMRoleID:   &empty,
			CampaignID: &empty,
		})

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

func TestAdminAPI_DeleteTenant_WithBotConnector(t *testing.T) {
	t.Parallel()

	store := gateway.NewMemAdminStore()
	connector := &mockBotConnector{}
	api := gateway.NewAdminAPI(store, testAPIKey, connector)
	handler := api.Handler()

	doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{ID: "delbot", LicenseTier: "shared"})

	rr := doRequest(t, handler, "DELETE", "/api/v1/tenants/delbot", nil)
	if rr.Code != http.StatusNoContent {
		t.Errorf("got status %d, want 204", rr.Code)
	}

	if len(connector.disconnectIDs) != 1 || connector.disconnectIDs[0] != "delbot" {
		t.Errorf("expected DisconnectBot called with 'delbot', got %v", connector.disconnectIDs)
	}
}

func TestAdminAPI_ReconnectAllBots(t *testing.T) {
	t.Parallel()

	t.Run("no bots connector", func(t *testing.T) {
		t.Parallel()

		store := gateway.NewMemAdminStore()
		api := gateway.NewAdminAPI(store, testAPIKey, nil)

		// Should not panic with nil bots.
		api.ReconnectAllBots(context.Background())
	})

	t.Run("reconnects tenants with tokens", func(t *testing.T) {
		t.Parallel()

		store := gateway.NewMemAdminStore()
		connector := &mockBotConnector{}
		api := gateway.NewAdminAPI(store, testAPIKey, connector)

		ctx := context.Background()

		// Seed tenants directly in the store.
		_ = store.CreateTenant(ctx, gateway.Tenant{ID: "t1", BotToken: "tok1"})
		_ = store.CreateTenant(ctx, gateway.Tenant{ID: "t2", BotToken: ""})   // no token
		_ = store.CreateTenant(ctx, gateway.Tenant{ID: "t3", BotToken: "tok3"})

		api.ReconnectAllBots(ctx)

		// The connector should have been called for t1 and t3 (not t2).
		// We can't check exact count with the simple mock, but connectCalled should be true.
		if !connector.connectCalled {
			t.Error("expected ConnectBot to be called for tenants with tokens")
		}
	})

	t.Run("uses TenantBotConnector when available", func(t *testing.T) {
		t.Parallel()

		store := gateway.NewMemAdminStore()
		connector := &mockTenantBotConnector{}
		api := gateway.NewAdminAPI(store, testAPIKey, connector)

		ctx := context.Background()
		_ = store.CreateTenant(ctx, gateway.Tenant{ID: "t1", BotToken: "tok1"})

		api.ReconnectAllBots(ctx)

		if !connector.connectForTenantCalled {
			t.Error("expected ConnectBotForTenant to be called for TenantBotConnector")
		}
	})

	t.Run("connect error does not stop reconnection", func(t *testing.T) {
		t.Parallel()

		store := gateway.NewMemAdminStore()
		connector := &mockBotConnector{connectErr: fmt.Errorf("connect failed")}
		api := gateway.NewAdminAPI(store, testAPIKey, connector)

		ctx := context.Background()
		_ = store.CreateTenant(ctx, gateway.Tenant{ID: "t1", BotToken: "tok1"})
		_ = store.CreateTenant(ctx, gateway.Tenant{ID: "t2", BotToken: "tok2"})

		// Should not panic even when connections fail.
		api.ReconnectAllBots(ctx)
	})
}

func TestAdminAPI_ListTenants_BotTokenStripped(t *testing.T) {
	t.Parallel()

	store := gateway.NewMemAdminStore()
	api := gateway.NewAdminAPI(store, testAPIKey, nil)
	handler := api.Handler()

	// Create a tenant with a bot token.
	doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{ID: "secretbot", LicenseTier: "shared", BotToken: "super-secret"})

	rr := doRequest(t, handler, "GET", "/api/v1/tenants", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rr.Code)
	}

	if bytes.Contains(rr.Body.Bytes(), []byte("super-secret")) {
		t.Error("list response should not contain bot tokens")
	}
}

func TestAdminAPI_GetTenant_BotTokenStripped(t *testing.T) {
	t.Parallel()

	store := gateway.NewMemAdminStore()
	api := gateway.NewAdminAPI(store, testAPIKey, nil)
	handler := api.Handler()

	doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{ID: "getbot", LicenseTier: "shared", BotToken: "my-secret"})

	rr := doRequest(t, handler, "GET", "/api/v1/tenants/getbot", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rr.Code)
	}

	if bytes.Contains(rr.Body.Bytes(), []byte("my-secret")) {
		t.Error("get response should not contain bot token")
	}
}

func TestAdminAPI_UpdateTenant_ReconnectOnGuildIDsChange(t *testing.T) {
	t.Parallel()

	store := gateway.NewMemAdminStore()
	connector := &mockBotConnector{}
	api := gateway.NewAdminAPI(store, testAPIKey, connector)
	handler := api.Handler()

	// Create tenant with a bot token.
	doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{ID: "guildupd", LicenseTier: "shared", BotToken: "tok"})

	// Reset state after create.
	connector.connectCalled = false

	// Update only guild IDs - should trigger reconnect since existing has a token.
	rr := doRequest(t, handler, "PUT", "/api/v1/tenants/guildupd",
		gateway.TenantUpdateRequest{GuildIDs: []string{"new-guild"}})

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	if !connector.connectCalled {
		t.Error("expected reconnect when guild IDs change")
	}
}

func TestAdminAPI_UpdateTenant_NoReconnectForTierOnly(t *testing.T) {
	t.Parallel()

	store := gateway.NewMemAdminStore()
	connector := &mockBotConnector{}
	api := gateway.NewAdminAPI(store, testAPIKey, connector)
	handler := api.Handler()

	doRequest(t, handler, "POST", "/api/v1/tenants",
		gateway.TenantCreateRequest{ID: "tieronly", LicenseTier: "shared", BotToken: "tok"})

	connector.connectCalled = false

	// Updating only the tier should not reconnect the bot.
	rr := doRequest(t, handler, "PUT", "/api/v1/tenants/tieronly",
		gateway.TenantUpdateRequest{LicenseTier: "dedicated"})

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rr.Code)
	}

	if connector.connectCalled {
		t.Error("should not reconnect bot when only tier changes")
	}
}

func TestTenant_MarshalJSON(t *testing.T) {
	t.Parallel()

	tenant := gateway.Tenant{
		ID:          "test-tenant",
		LicenseTier: 0, // TierShared
	}

	data, err := json.Marshal(tenant)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// LicenseTier should be serialised as a string, not a number.
	tier, ok := result["license_tier"].(string)
	if !ok {
		t.Fatalf("license_tier is not a string: %T", result["license_tier"])
	}
	if tier != "shared" {
		t.Errorf("got license_tier %q, want %q", tier, "shared")
	}
}
