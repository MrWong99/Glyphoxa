package grpctransport

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/gateway"
	"github.com/MrWong99/glyphoxa/internal/gateway/sessionorch"
	"github.com/MrWong99/glyphoxa/internal/gateway/usage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ── Mock AdminStore ─────────────────────────────────────────────────────────

type mockAdminStore struct {
	tenants   map[string]gateway.Tenant
	createErr error
	updateErr error
	deleteErr error
	listErr   error
}

func newMockAdminStore() *mockAdminStore {
	return &mockAdminStore{tenants: make(map[string]gateway.Tenant)}
}

func (m *mockAdminStore) CreateTenant(_ context.Context, t gateway.Tenant) error {
	if m.createErr != nil {
		return m.createErr
	}
	if _, ok := m.tenants[t.ID]; ok {
		return errors.New("already exists")
	}
	m.tenants[t.ID] = t
	return nil
}

func (m *mockAdminStore) GetTenant(_ context.Context, id string) (gateway.Tenant, error) {
	t, ok := m.tenants[id]
	if !ok {
		return gateway.Tenant{}, errors.New("not found")
	}
	return t, nil
}

func (m *mockAdminStore) UpdateTenant(_ context.Context, t gateway.Tenant) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.tenants[t.ID] = t
	return nil
}

func (m *mockAdminStore) DeleteTenant(_ context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	if _, ok := m.tenants[id]; !ok {
		return errors.New("not found")
	}
	delete(m.tenants, id)
	return nil
}

func (m *mockAdminStore) ListTenants(_ context.Context) ([]gateway.Tenant, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	tenants := make([]gateway.Tenant, 0, len(m.tenants))
	for _, t := range m.tenants {
		tenants = append(tenants, t)
	}
	return tenants, nil
}

// ── Mock Orchestrator ───────────────────────────────────────────────────────

type mockOrchestrator struct {
	sessions      []sessionorch.Session
	sessionsErr   error
	getSession    sessionorch.Session
	getSessionErr error
}

func (m *mockOrchestrator) ActiveSessions(_ context.Context, _ string) ([]sessionorch.Session, error) {
	return m.sessions, m.sessionsErr
}

func (m *mockOrchestrator) GetSession(_ context.Context, _ string) (sessionorch.Session, error) {
	return m.getSession, m.getSessionErr
}

// ── Mock BotChecker ─────────────────────────────────────────────────────────

type mockBotChecker struct {
	connected  bool
	guildCount int
}

func (m *mockBotChecker) IsBotConnected(_ string) (bool, int) {
	return m.connected, m.guildCount
}

// ── Mock BotConnector ───────────────────────────────────────────────────────

type mockBotConnector struct {
	connectCalls    []string
	connectErr      error
	disconnectCalls []string
}

func (m *mockBotConnector) ConnectBot(_ context.Context, tenantID, _ string, _ []string) error {
	m.connectCalls = append(m.connectCalls, tenantID)
	return m.connectErr
}

func (m *mockBotConnector) DisconnectBot(tenantID string) {
	m.disconnectCalls = append(m.disconnectCalls, tenantID)
}

// ── Mock UsageStore ─────────────────────────────────────────────────────────

type mockUsageStore struct {
	record    usage.Record
	recordErr error
}

func (m *mockUsageStore) RecordUsage(_ context.Context, _ string, _ usage.Record) error {
	return nil
}

func (m *mockUsageStore) GetUsage(_ context.Context, _ string, _ time.Time) (usage.Record, error) {
	return m.record, m.recordErr
}

func (m *mockUsageStore) CheckQuota(_ context.Context, _ string, _ usage.QuotaConfig) error {
	return nil
}

// ── Mock SessionController ──────────────────────────────────────────────────

type mockSessionController struct {
	startErr  error
	stopErr   error
	active    bool
	info      gateway.SessionInfo
	infoFound bool
}

func (m *mockSessionController) Start(_ context.Context, _ gateway.SessionStartRequest) error {
	return m.startErr
}

func (m *mockSessionController) Stop(_ context.Context, _ string) error {
	return m.stopErr
}

func (m *mockSessionController) IsActive(_ string) bool {
	return m.active
}

func (m *mockSessionController) Info(_ string) (gateway.SessionInfo, bool) {
	return m.info, m.infoFound
}

// ── Helper ──────────────────────────────────────────────────────────────────

func assertGRPCCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected gRPC error with code %v, got nil", want)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != want {
		t.Errorf("code = %v, want %v (msg: %s)", st.Code(), want, st.Message())
	}
}

func newTestManagementServer(store *mockAdminStore, opts ...func(*ManagementServerConfig)) *ManagementServer {
	cfg := ManagementServerConfig{
		Store: store,
	}
	for _, o := range opts {
		o(&cfg)
	}
	return NewManagementServer(cfg)
}

// ── Tenant CRUD Tests ───────────────────────────────────────────────────────

func TestManagementServer_CreateTenant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		req      *pb.CreateTenantRequest
		store    *mockAdminStore
		bots     *mockBotConnector
		wantCode codes.Code
		wantErr  bool
	}{
		{
			name: "success",
			req: &pb.CreateTenantRequest{
				Id:          "testtenant",
				LicenseTier: "shared",
				BotToken:    "tok-abc",
				GuildIds:    []string{"g1", "g2"},
				DmRoleId:    "role-1",
				CampaignId:  "camp-1",
			},
			store: newMockAdminStore(),
			bots:  &mockBotConnector{},
		},
		{
			name:     "missing id",
			req:      &pb.CreateTenantRequest{LicenseTier: "shared"},
			store:    newMockAdminStore(),
			wantCode: codes.InvalidArgument,
			wantErr:  true,
		},
		{
			name: "invalid tenant id format",
			req: &pb.CreateTenantRequest{
				Id:          "INVALID-ID!",
				LicenseTier: "shared",
			},
			store:    newMockAdminStore(),
			wantCode: codes.InvalidArgument,
			wantErr:  true,
		},
		{
			name: "invalid license tier",
			req: &pb.CreateTenantRequest{
				Id:          "testtenant",
				LicenseTier: "platinum",
			},
			store:    newMockAdminStore(),
			wantCode: codes.InvalidArgument,
			wantErr:  true,
		},
		{
			name: "duplicate tenant",
			req: &pb.CreateTenantRequest{
				Id:          "existing",
				LicenseTier: "shared",
			},
			store: func() *mockAdminStore {
				s := newMockAdminStore()
				s.tenants["existing"] = gateway.Tenant{ID: "existing"}
				return s
			}(),
			wantCode: codes.AlreadyExists,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var bots gateway.BotConnector
			if tt.bots != nil {
				bots = tt.bots
			}
			srv := newTestManagementServer(tt.store, func(cfg *ManagementServerConfig) {
				cfg.Bots = bots
			})
			resp, err := srv.CreateTenant(context.Background(), tt.req)

			if tt.wantErr {
				assertGRPCCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.GetTenant().GetId() != tt.req.GetId() {
				t.Errorf("tenant.Id = %q, want %q", resp.GetTenant().GetId(), tt.req.GetId())
			}
			if resp.GetTenant().GetLicenseTier() != tt.req.GetLicenseTier() {
				t.Errorf("tenant.LicenseTier = %q, want %q", resp.GetTenant().GetLicenseTier(), tt.req.GetLicenseTier())
			}
			// Verify stored tenant.
			stored, ok := tt.store.tenants[tt.req.GetId()]
			if !ok {
				t.Fatal("tenant not found in store after creation")
			}
			if stored.DMRoleID != tt.req.GetDmRoleId() {
				t.Errorf("stored.DMRoleID = %q, want %q", stored.DMRoleID, tt.req.GetDmRoleId())
			}
		})
	}
}

func TestManagementServer_CreateTenant_ConnectsBot(t *testing.T) {
	t.Parallel()

	store := newMockAdminStore()
	bots := &mockBotConnector{}
	srv := newTestManagementServer(store, func(cfg *ManagementServerConfig) {
		cfg.Bots = bots
	})

	_, err := srv.CreateTenant(context.Background(), &pb.CreateTenantRequest{
		Id:          "bottest",
		LicenseTier: "shared",
		BotToken:    "tok-xyz",
		GuildIds:    []string{"g1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(bots.connectCalls) != 1 || bots.connectCalls[0] != "bottest" {
		t.Errorf("connectCalls = %v, want [bottest]", bots.connectCalls)
	}
}

func TestManagementServer_CreateTenant_NoBotTokenSkipsConnect(t *testing.T) {
	t.Parallel()

	store := newMockAdminStore()
	bots := &mockBotConnector{}
	srv := newTestManagementServer(store, func(cfg *ManagementServerConfig) {
		cfg.Bots = bots
	})

	_, err := srv.CreateTenant(context.Background(), &pb.CreateTenantRequest{
		Id:          "nobot",
		LicenseTier: "shared",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(bots.connectCalls) != 0 {
		t.Errorf("expected no bot connect calls, got %v", bots.connectCalls)
	}
}

func TestManagementServer_GetTenant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		store    *mockAdminStore
		req      *pb.GetTenantRequest
		wantCode codes.Code
		wantErr  bool
	}{
		{
			name: "found",
			store: func() *mockAdminStore {
				s := newMockAdminStore()
				s.tenants["abc"] = gateway.Tenant{
					ID:          "abc",
					LicenseTier: config.TierShared,
					GuildIDs:    []string{"g1"},
				}
				return s
			}(),
			req: &pb.GetTenantRequest{Id: "abc"},
		},
		{
			name:     "not found",
			store:    newMockAdminStore(),
			req:      &pb.GetTenantRequest{Id: "missing"},
			wantCode: codes.NotFound,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := newTestManagementServer(tt.store)
			resp, err := srv.GetTenant(context.Background(), tt.req)

			if tt.wantErr {
				assertGRPCCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.GetTenant().GetId() != tt.req.GetId() {
				t.Errorf("tenant.Id = %q, want %q", resp.GetTenant().GetId(), tt.req.GetId())
			}
		})
	}
}

func TestManagementServer_ListTenants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		store    *mockAdminStore
		wantLen  int
		wantCode codes.Code
		wantErr  bool
	}{
		{
			name:    "empty",
			store:   newMockAdminStore(),
			wantLen: 0,
		},
		{
			name: "two tenants",
			store: func() *mockAdminStore {
				s := newMockAdminStore()
				s.tenants["a"] = gateway.Tenant{ID: "a", LicenseTier: config.TierShared}
				s.tenants["b"] = gateway.Tenant{ID: "b", LicenseTier: config.TierDedicated}
				return s
			}(),
			wantLen: 2,
		},
		{
			name: "store error",
			store: &mockAdminStore{
				tenants: make(map[string]gateway.Tenant),
				listErr: errors.New("db down"),
			},
			wantCode: codes.Internal,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := newTestManagementServer(tt.store)
			resp, err := srv.ListTenants(context.Background(), &pb.ListTenantsRequest{})

			if tt.wantErr {
				assertGRPCCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(resp.GetTenants()) != tt.wantLen {
				t.Errorf("tenants len = %d, want %d", len(resp.GetTenants()), tt.wantLen)
			}
		})
	}
}

func TestManagementServer_UpdateTenant(t *testing.T) {
	t.Parallel()

	baseTenant := gateway.Tenant{
		ID:          "updatable",
		LicenseTier: config.TierShared,
		BotToken:    "old-tok",
		GuildIDs:    []string{"g1"},
		DMRoleID:    "old-role",
		CampaignID:  "old-camp",
		CreatedAt:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	tests := []struct {
		name     string
		store    *mockAdminStore
		bots     *mockBotConnector
		req      *pb.UpdateTenantRequest
		wantCode codes.Code
		wantErr  bool
		check    func(t *testing.T, store *mockAdminStore)
	}{
		{
			name: "update license tier",
			store: func() *mockAdminStore {
				s := newMockAdminStore()
				s.tenants["updatable"] = baseTenant
				return s
			}(),
			req: &pb.UpdateTenantRequest{
				Id:          "updatable",
				LicenseTier: "dedicated",
			},
			check: func(t *testing.T, store *mockAdminStore) {
				t.Helper()
				stored := store.tenants["updatable"]
				if stored.LicenseTier != config.TierDedicated {
					t.Errorf("LicenseTier = %v, want %v", stored.LicenseTier, config.TierDedicated)
				}
			},
		},
		{
			name: "update guild IDs triggers bot reconnect",
			store: func() *mockAdminStore {
				s := newMockAdminStore()
				s.tenants["updatable"] = baseTenant
				return s
			}(),
			bots: &mockBotConnector{},
			req: &pb.UpdateTenantRequest{
				Id:       "updatable",
				GuildIds: []string{"g2", "g3"},
			},
			check: func(t *testing.T, store *mockAdminStore) {
				t.Helper()
				stored := store.tenants["updatable"]
				if len(stored.GuildIDs) != 2 || stored.GuildIDs[0] != "g2" {
					t.Errorf("GuildIDs = %v, want [g2 g3]", stored.GuildIDs)
				}
			},
		},
		{
			name: "clear dm role id",
			store: func() *mockAdminStore {
				s := newMockAdminStore()
				s.tenants["updatable"] = baseTenant
				return s
			}(),
			req: &pb.UpdateTenantRequest{
				Id:            "updatable",
				ClearDmRoleId: true,
			},
			check: func(t *testing.T, store *mockAdminStore) {
				t.Helper()
				stored := store.tenants["updatable"]
				if stored.DMRoleID != "" {
					t.Errorf("DMRoleID = %q, want empty", stored.DMRoleID)
				}
			},
		},
		{
			name: "clear campaign id",
			store: func() *mockAdminStore {
				s := newMockAdminStore()
				s.tenants["updatable"] = baseTenant
				return s
			}(),
			req: &pb.UpdateTenantRequest{
				Id:              "updatable",
				ClearCampaignId: true,
			},
			check: func(t *testing.T, store *mockAdminStore) {
				t.Helper()
				stored := store.tenants["updatable"]
				if stored.CampaignID != "" {
					t.Errorf("CampaignID = %q, want empty", stored.CampaignID)
				}
			},
		},
		{
			name:  "not found",
			store: newMockAdminStore(),
			req: &pb.UpdateTenantRequest{
				Id:          "missing",
				LicenseTier: "shared",
			},
			wantCode: codes.NotFound,
			wantErr:  true,
		},
		{
			name: "invalid license tier",
			store: func() *mockAdminStore {
				s := newMockAdminStore()
				s.tenants["updatable"] = baseTenant
				return s
			}(),
			req: &pb.UpdateTenantRequest{
				Id:          "updatable",
				LicenseTier: "invalid",
			},
			wantCode: codes.InvalidArgument,
			wantErr:  true,
		},
		{
			name: "store update error",
			store: func() *mockAdminStore {
				s := newMockAdminStore()
				s.tenants["updatable"] = baseTenant
				s.updateErr = errors.New("db write fail")
				return s
			}(),
			req: &pb.UpdateTenantRequest{
				Id:       "updatable",
				BotToken: "new-tok",
			},
			wantCode: codes.Internal,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var bots gateway.BotConnector
			if tt.bots != nil {
				bots = tt.bots
			}
			srv := newTestManagementServer(tt.store, func(cfg *ManagementServerConfig) {
				cfg.Bots = bots
			})
			resp, err := srv.UpdateTenant(context.Background(), tt.req)

			if tt.wantErr {
				assertGRPCCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.GetTenant().GetId() != tt.req.GetId() {
				t.Errorf("tenant.Id = %q, want %q", resp.GetTenant().GetId(), tt.req.GetId())
			}
			if tt.check != nil {
				tt.check(t, tt.store)
			}
		})
	}
}

func TestManagementServer_DeleteTenant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		store    *mockAdminStore
		bots     *mockBotConnector
		req      *pb.DeleteTenantRequest
		wantCode codes.Code
		wantErr  bool
	}{
		{
			name: "success",
			store: func() *mockAdminStore {
				s := newMockAdminStore()
				s.tenants["del"] = gateway.Tenant{ID: "del"}
				return s
			}(),
			bots: &mockBotConnector{},
			req:  &pb.DeleteTenantRequest{Id: "del"},
		},
		{
			name:     "not found",
			store:    newMockAdminStore(),
			req:      &pb.DeleteTenantRequest{Id: "missing"},
			wantCode: codes.NotFound,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var bots gateway.BotConnector
			if tt.bots != nil {
				bots = tt.bots
			}
			srv := newTestManagementServer(tt.store, func(cfg *ManagementServerConfig) {
				cfg.Bots = bots
			})
			_, err := srv.DeleteTenant(context.Background(), tt.req)

			if tt.wantErr {
				assertGRPCCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Verify tenant removed from store.
			if _, ok := tt.store.tenants[tt.req.GetId()]; ok {
				t.Error("tenant still in store after deletion")
			}
			// Verify bot disconnected.
			if tt.bots != nil {
				if len(tt.bots.disconnectCalls) != 1 || tt.bots.disconnectCalls[0] != tt.req.GetId() {
					t.Errorf("disconnectCalls = %v, want [%s]", tt.bots.disconnectCalls, tt.req.GetId())
				}
			}
		})
	}
}

// ── Session Control Tests ───────────────────────────────────────────────────

func TestManagementServer_StartWebSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ctrl     *mockSessionController
		lookup   func(string) (gateway.SessionController, bool)
		req      *pb.StartWebSessionRequest
		wantCode codes.Code
		wantErr  bool
	}{
		{
			name: "success",
			ctrl: &mockSessionController{
				info:      gateway.SessionInfo{SessionID: "new-sess"},
				infoFound: true,
			},
			req: &pb.StartWebSessionRequest{
				TenantId:  "tenant_a",
				GuildId:   "guild-1",
				ChannelId: "chan-1",
			},
		},
		{
			name: "missing required fields",
			req: &pb.StartWebSessionRequest{
				TenantId: "tenant_a",
				// Missing GuildId and ChannelId.
			},
			wantCode: codes.InvalidArgument,
			wantErr:  true,
		},
		{
			name: "no controller found",
			lookup: func(_ string) (gateway.SessionController, bool) {
				return nil, false
			},
			req: &pb.StartWebSessionRequest{
				TenantId:  "tenant_a",
				GuildId:   "guild-1",
				ChannelId: "chan-1",
			},
			wantCode: codes.FailedPrecondition,
			wantErr:  true,
		},
		{
			name: "ctrl.Start fails",
			ctrl: &mockSessionController{startErr: errors.New("already running")},
			req: &pb.StartWebSessionRequest{
				TenantId:  "tenant_a",
				GuildId:   "guild-1",
				ChannelId: "chan-1",
			},
			wantCode: codes.Internal,
			wantErr:  true,
		},
		{
			name: "nil ctrlLookup",
			req: &pb.StartWebSessionRequest{
				TenantId:  "tenant_a",
				GuildId:   "guild-1",
				ChannelId: "chan-1",
			},
			lookup:   nil, // explicitly nil
			wantCode: codes.Unavailable,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := newMockAdminStore()

			// Build ctrlLookup. If no explicit lookup and no error expected from
			// missing lookup, use the default ctrl.
			var lookup func(string) (gateway.SessionController, bool)
			if tt.lookup != nil {
				lookup = tt.lookup
			} else if tt.ctrl != nil {
				ctrl := tt.ctrl
				lookup = func(_ string) (gateway.SessionController, bool) {
					return ctrl, true
				}
			}

			srv := newTestManagementServer(store, func(cfg *ManagementServerConfig) {
				cfg.CtrlLookup = lookup
			})
			resp, err := srv.StartWebSession(context.Background(), tt.req)

			if tt.wantErr {
				assertGRPCCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.GetSessionId() != "new-sess" {
				t.Errorf("session_id = %q, want %q", resp.GetSessionId(), "new-sess")
			}
		})
	}
}

func TestManagementServer_StopWebSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		orch     *mockOrchestrator
		ctrl     *mockSessionController
		lookup   func(string) (gateway.SessionController, bool)
		req      *pb.StopWebSessionRequest
		wantCode codes.Code
		wantErr  bool
	}{
		{
			name: "success",
			orch: &mockOrchestrator{
				getSession: sessionorch.Session{
					ID:       "sess-1",
					TenantID: "tenant_a",
				},
			},
			ctrl: &mockSessionController{},
			req:  &pb.StopWebSessionRequest{SessionId: "sess-1"},
		},
		{
			name:     "missing session_id",
			req:      &pb.StopWebSessionRequest{},
			wantCode: codes.InvalidArgument,
			wantErr:  true,
		},
		{
			name: "session not found in orchestrator",
			orch: &mockOrchestrator{
				getSessionErr: errors.New("not found"),
			},
			ctrl:     &mockSessionController{},
			req:      &pb.StopWebSessionRequest{SessionId: "sess-missing"},
			wantCode: codes.NotFound,
			wantErr:  true,
		},
		{
			name: "no controller for tenant",
			orch: &mockOrchestrator{
				getSession: sessionorch.Session{
					ID:       "sess-1",
					TenantID: "tenant_a",
				},
			},
			lookup: func(_ string) (gateway.SessionController, bool) {
				return nil, false
			},
			req:      &pb.StopWebSessionRequest{SessionId: "sess-1"},
			wantCode: codes.FailedPrecondition,
			wantErr:  true,
		},
		{
			name: "ctrl.Stop fails",
			orch: &mockOrchestrator{
				getSession: sessionorch.Session{
					ID:       "sess-1",
					TenantID: "tenant_a",
				},
			},
			ctrl:     &mockSessionController{stopErr: errors.New("cannot stop")},
			req:      &pb.StopWebSessionRequest{SessionId: "sess-1"},
			wantCode: codes.Internal,
			wantErr:  true,
		},
		{
			name:     "nil orch",
			req:      &pb.StopWebSessionRequest{SessionId: "sess-1"},
			wantCode: codes.Unavailable,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := newMockAdminStore()

			var lookup func(string) (gateway.SessionController, bool)
			if tt.lookup != nil {
				lookup = tt.lookup
			} else if tt.ctrl != nil {
				ctrl := tt.ctrl
				lookup = func(_ string) (gateway.SessionController, bool) {
					return ctrl, true
				}
			}

			srv := newTestManagementServer(store, func(cfg *ManagementServerConfig) {
				if tt.orch != nil {
					cfg.Orch = tt.orch
				}
				cfg.CtrlLookup = lookup
			})
			_, err := srv.StopWebSession(context.Background(), tt.req)

			if tt.wantErr {
				assertGRPCCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestManagementServer_ListActiveSessions(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		orch     *mockOrchestrator
		req      *pb.ListActiveSessionsRequest
		wantLen  int
		wantCode codes.Code
		wantErr  bool
	}{
		{
			name: "returns sessions",
			orch: &mockOrchestrator{
				sessions: []sessionorch.Session{
					{
						ID:          "s1",
						TenantID:    "tenant_a",
						CampaignID:  "camp-1",
						GuildID:     "g1",
						ChannelID:   "c1",
						LicenseTier: config.TierShared,
						State:       gateway.SessionActive,
						StartedAt:   now,
					},
				},
			},
			req:     &pb.ListActiveSessionsRequest{TenantId: "tenant_a"},
			wantLen: 1,
		},
		{
			name:     "missing tenant_id",
			orch:     &mockOrchestrator{},
			req:      &pb.ListActiveSessionsRequest{},
			wantCode: codes.InvalidArgument,
			wantErr:  true,
		},
		{
			name: "orchestrator error",
			orch: &mockOrchestrator{
				sessionsErr: errors.New("db error"),
			},
			req:      &pb.ListActiveSessionsRequest{TenantId: "tenant_a"},
			wantCode: codes.Internal,
			wantErr:  true,
		},
		{
			name:     "nil orchestrator",
			req:      &pb.ListActiveSessionsRequest{TenantId: "tenant_a"},
			wantCode: codes.Unavailable,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := newMockAdminStore()
			srv := newTestManagementServer(store, func(cfg *ManagementServerConfig) {
				if tt.orch != nil {
					cfg.Orch = tt.orch
				}
			})
			resp, err := srv.ListActiveSessions(context.Background(), tt.req)

			if tt.wantErr {
				assertGRPCCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(resp.GetSessions()) != tt.wantLen {
				t.Errorf("sessions len = %d, want %d", len(resp.GetSessions()), tt.wantLen)
			}
			// Verify field mapping for the first session.
			if tt.wantLen > 0 {
				s := resp.GetSessions()[0]
				if s.GetSessionId() != "s1" {
					t.Errorf("session[0].Id = %q, want %q", s.GetSessionId(), "s1")
				}
				if s.GetGuildId() != "g1" {
					t.Errorf("session[0].GuildId = %q, want %q", s.GetGuildId(), "g1")
				}
				if s.GetState() != pb.SessionState_SESSION_STATE_ACTIVE {
					t.Errorf("session[0].State = %v, want ACTIVE", s.GetState())
				}
			}
		})
	}
}

// ── Usage Tests ─────────────────────────────────────────────────────────────

func TestManagementServer_GetUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		usageStore *mockUsageStore
		req        *pb.GetUsageRequest
		wantCode   codes.Code
		wantErr    bool
	}{
		{
			name: "returns usage",
			usageStore: &mockUsageStore{
				record: usage.Record{
					TenantID:     "tenant_a",
					SessionHours: 42.5,
					LLMTokens:    1000,
					STTSeconds:   300.0,
					TTSChars:     5000,
				},
			},
			req: &pb.GetUsageRequest{TenantId: "tenant_a"},
		},
		{
			name:     "missing tenant_id",
			req:      &pb.GetUsageRequest{},
			wantCode: codes.InvalidArgument,
			wantErr:  true,
		},
		{
			name:     "nil usage store",
			req:      &pb.GetUsageRequest{TenantId: "tenant_a"},
			wantCode: codes.Unavailable,
			wantErr:  true,
		},
		{
			name: "store error",
			usageStore: &mockUsageStore{
				recordErr: errors.New("db down"),
			},
			req:      &pb.GetUsageRequest{TenantId: "tenant_a"},
			wantCode: codes.Internal,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := newMockAdminStore()
			srv := newTestManagementServer(store, func(cfg *ManagementServerConfig) {
				if tt.usageStore != nil {
					cfg.UsageStore = tt.usageStore
				}
			})
			resp, err := srv.GetUsage(context.Background(), tt.req)

			if tt.wantErr {
				assertGRPCCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.GetTenantId() != "tenant_a" {
				t.Errorf("TenantId = %q, want %q", resp.GetTenantId(), "tenant_a")
			}
			if resp.GetSessionHours() != 42.5 {
				t.Errorf("SessionHours = %v, want 42.5", resp.GetSessionHours())
			}
			if resp.GetLlmTokens() != 1000 {
				t.Errorf("LlmTokens = %v, want 1000", resp.GetLlmTokens())
			}
		})
	}
}

// ── Bot Status Tests ────────────────────────────────────────────────────────

func TestManagementServer_GetBotStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		botChecker *mockBotChecker
		req        *pb.GetBotStatusRequest
		wantState  pb.BotConnectionState
		wantGuilds int32
		wantCode   codes.Code
		wantErr    bool
	}{
		{
			name:       "connected",
			botChecker: &mockBotChecker{connected: true, guildCount: 5},
			req:        &pb.GetBotStatusRequest{TenantId: "tenant_a"},
			wantState:  pb.BotConnectionState_BOT_CONNECTION_STATE_CONNECTED,
			wantGuilds: 5,
		},
		{
			name:       "disconnected",
			botChecker: &mockBotChecker{connected: false, guildCount: 0},
			req:        &pb.GetBotStatusRequest{TenantId: "tenant_a"},
			wantState:  pb.BotConnectionState_BOT_CONNECTION_STATE_DISCONNECTED,
			wantGuilds: 0,
		},
		{
			name:       "nil botChecker returns disconnected",
			botChecker: nil,
			req:        &pb.GetBotStatusRequest{TenantId: "tenant_a"},
			wantState:  pb.BotConnectionState_BOT_CONNECTION_STATE_DISCONNECTED,
			wantGuilds: 0,
		},
		{
			name:     "missing tenant_id",
			req:      &pb.GetBotStatusRequest{},
			wantCode: codes.InvalidArgument,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := newMockAdminStore()
			srv := newTestManagementServer(store, func(cfg *ManagementServerConfig) {
				if tt.botChecker != nil {
					cfg.BotChecker = tt.botChecker
				}
			})
			resp, err := srv.GetBotStatus(context.Background(), tt.req)

			if tt.wantErr {
				assertGRPCCode(t, err, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.GetState() != tt.wantState {
				t.Errorf("State = %v, want %v", resp.GetState(), tt.wantState)
			}
			if resp.GetGuildCount() != tt.wantGuilds {
				t.Errorf("GuildCount = %d, want %d", resp.GetGuildCount(), tt.wantGuilds)
			}
			if resp.GetTenantId() != tt.req.GetTenantId() {
				t.Errorf("TenantId = %q, want %q", resp.GetTenantId(), tt.req.GetTenantId())
			}
		})
	}
}

// ── tenantToPB Tests ────────────────────────────────────────────────────────

func TestTenantToPB(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 3, 15, 10, 30, 0, 0, time.UTC)
	tenant := gateway.Tenant{
		ID:                  "test_tenant",
		LicenseTier:         config.TierDedicated,
		BotToken:            "secret-token-should-not-appear",
		GuildIDs:            []string{"g1", "g2"},
		DMRoleID:            "role-123",
		CampaignID:          "camp-abc",
		MonthlySessionHours: 100.5,
		CreatedAt:           now,
		UpdatedAt:           now.Add(time.Hour),
	}

	pbTenant := tenantToPB(tenant)

	if pbTenant.GetId() != tenant.ID {
		t.Errorf("Id = %q, want %q", pbTenant.GetId(), tenant.ID)
	}
	if pbTenant.GetLicenseTier() != "dedicated" {
		t.Errorf("LicenseTier = %q, want %q", pbTenant.GetLicenseTier(), "dedicated")
	}
	if len(pbTenant.GetGuildIds()) != 2 {
		t.Errorf("GuildIds len = %d, want 2", len(pbTenant.GetGuildIds()))
	}
	if pbTenant.GetDmRoleId() != tenant.DMRoleID {
		t.Errorf("DmRoleId = %q, want %q", pbTenant.GetDmRoleId(), tenant.DMRoleID)
	}
	if pbTenant.GetCampaignId() != tenant.CampaignID {
		t.Errorf("CampaignId = %q, want %q", pbTenant.GetCampaignId(), tenant.CampaignID)
	}
	if pbTenant.GetMonthlySessionHours() != tenant.MonthlySessionHours {
		t.Errorf("MonthlySessionHours = %v, want %v", pbTenant.GetMonthlySessionHours(), tenant.MonthlySessionHours)
	}
	if !pbTenant.GetCreatedAt().AsTime().Equal(tenant.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", pbTenant.GetCreatedAt().AsTime(), tenant.CreatedAt)
	}
	if !pbTenant.GetUpdatedAt().AsTime().Equal(tenant.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", pbTenant.GetUpdatedAt().AsTime(), tenant.UpdatedAt)
	}
}

func TestManagementServer_Register(t *testing.T) {
	t.Parallel()

	srv := NewManagementServer(ManagementServerConfig{
		Store: newMockAdminStore(),
	})
	gs := grpc.NewServer()
	// Register should not panic.
	srv.Register(gs)
	gs.Stop()
}
