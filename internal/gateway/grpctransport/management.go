package grpctransport

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/gateway"
	"github.com/MrWong99/glyphoxa/internal/gateway/sessionorch"
	"github.com/MrWong99/glyphoxa/internal/gateway/usage"
)

// Compile-time interface assertion.
var _ pb.ManagementServiceServer = (*ManagementServer)(nil)

// ManagementOrchestrator is the subset of sessionorch.Orchestrator needed by
// ManagementServer for listing and querying sessions.
type ManagementOrchestrator interface {
	ActiveSessions(ctx context.Context, tenantID string) ([]sessionorch.Session, error)
	GetSession(ctx context.Context, sessionID string) (sessionorch.Session, error)
}

// ManagementBotChecker reports whether a tenant's Discord bot is connected.
type ManagementBotChecker interface {
	IsBotConnected(tenantID string) (connected bool, guildCount int)
}

// ManagementServer implements the ManagementService gRPC service on the
// gateway side. It handles tenant CRUD, session start/stop from the web UI,
// usage queries, and bot status checks.
type ManagementServer struct {
	pb.UnimplementedManagementServiceServer

	store      gateway.AdminStore
	bots       gateway.BotConnector
	orch       ManagementOrchestrator
	usageStore usage.Store
	botChecker ManagementBotChecker
	ctrlLookup func(tenantID string) (gateway.SessionController, bool)
}

// ManagementServerConfig holds the dependencies for creating a ManagementServer.
type ManagementServerConfig struct {
	Store      gateway.AdminStore
	Bots       gateway.BotConnector
	Orch       ManagementOrchestrator
	UsageStore usage.Store
	BotChecker ManagementBotChecker
	// CtrlLookup resolves a tenant ID to its SessionController.
	// Returns false if the tenant has no connected bot / controller.
	CtrlLookup func(tenantID string) (gateway.SessionController, bool)
}

// NewManagementServer creates a gRPC ManagementServer.
func NewManagementServer(cfg ManagementServerConfig) *ManagementServer {
	return &ManagementServer{
		store:      cfg.Store,
		bots:       cfg.Bots,
		orch:       cfg.Orch,
		usageStore: cfg.UsageStore,
		botChecker: cfg.BotChecker,
		ctrlLookup: cfg.CtrlLookup,
	}
}

// Register adds the ManagementServer to a gRPC server.
func (s *ManagementServer) Register(gs *grpc.Server) {
	pb.RegisterManagementServiceServer(gs, s)
}

// ── Tenant CRUD ─────────────────────────────────────────────────────────────

// CreateTenant implements the gRPC CreateTenant RPC.
func (s *ManagementServer) CreateTenant(ctx context.Context, req *pb.CreateTenantRequest) (*pb.TenantResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	tc := config.TenantContext{TenantID: req.GetId()}
	if err := tc.Validate(); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid tenant id: %v", err)
	}
	tier, err := config.ParseLicenseTier(req.GetLicenseTier())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid license tier: %v", err)
	}

	now := time.Now().UTC()
	tenant := gateway.Tenant{
		ID:          req.GetId(),
		LicenseTier: tier,
		BotToken:    req.GetBotToken(),
		GuildIDs:    req.GetGuildIds(),
		DMRoleID:    req.GetDmRoleId(),
		CampaignID:  req.GetCampaignId(),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.store.CreateTenant(ctx, tenant); err != nil {
		slog.Warn("mgmt: create tenant failed", "tenant_id", req.GetId(), "err", err)
		return nil, status.Errorf(codes.AlreadyExists, "tenant %q already exists", req.GetId())
	}

	slog.Info("mgmt: tenant created", "tenant_id", req.GetId(), "license_tier", tier.String())

	if s.bots != nil && req.GetBotToken() != "" {
		if err := s.connectBotForTenant(ctx, tenant); err != nil {
			slog.Error("mgmt: failed to connect bot for new tenant", "tenant_id", tenant.ID, "err", err)
		}
	}

	return &pb.TenantResponse{Tenant: tenantToPB(tenant)}, nil
}

// GetTenant implements the gRPC GetTenant RPC.
func (s *ManagementServer) GetTenant(ctx context.Context, req *pb.GetTenantRequest) (*pb.TenantResponse, error) {
	tenant, err := s.store.GetTenant(ctx, req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "tenant %q not found", req.GetId())
	}
	return &pb.TenantResponse{Tenant: tenantToPB(tenant)}, nil
}

// ListTenants implements the gRPC ListTenants RPC.
func (s *ManagementServer) ListTenants(ctx context.Context, _ *pb.ListTenantsRequest) (*pb.ListTenantsResponse, error) {
	tenants, err := s.store.ListTenants(ctx)
	if err != nil {
		slog.Warn("mgmt: list tenants failed", "err", err)
		return nil, status.Error(codes.Internal, "failed to list tenants")
	}
	pbTenants := make([]*pb.TenantInfo, len(tenants))
	for i, t := range tenants {
		pbTenants[i] = tenantToPB(t)
	}
	return &pb.ListTenantsResponse{Tenants: pbTenants}, nil
}

// UpdateTenant implements the gRPC UpdateTenant RPC.
func (s *ManagementServer) UpdateTenant(ctx context.Context, req *pb.UpdateTenantRequest) (*pb.TenantResponse, error) {
	existing, err := s.store.GetTenant(ctx, req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "tenant %q not found", req.GetId())
	}

	if req.GetLicenseTier() != "" {
		tier, err := config.ParseLicenseTier(req.GetLicenseTier())
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid license tier: %v", err)
		}
		existing.LicenseTier = tier
	}
	if req.GetBotToken() != "" {
		existing.BotToken = req.GetBotToken()
	}
	if len(req.GetGuildIds()) > 0 {
		existing.GuildIDs = req.GetGuildIds()
	}
	if req.GetDmRoleId() != "" || req.GetClearDmRoleId() {
		if req.GetClearDmRoleId() {
			existing.DMRoleID = ""
		} else {
			existing.DMRoleID = req.GetDmRoleId()
		}
	}
	if req.GetCampaignId() != "" || req.GetClearCampaignId() {
		if req.GetClearCampaignId() {
			existing.CampaignID = ""
		} else {
			existing.CampaignID = req.GetCampaignId()
		}
	}

	existing.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateTenant(ctx, existing); err != nil {
		slog.Warn("mgmt: update tenant failed", "tenant_id", req.GetId(), "err", err)
		return nil, status.Error(codes.Internal, "failed to update tenant")
	}

	slog.Info("mgmt: tenant updated", "tenant_id", req.GetId())

	needsReconnect := req.GetBotToken() != "" || len(req.GetGuildIds()) > 0 ||
		req.GetDmRoleId() != "" || req.GetClearDmRoleId() ||
		req.GetCampaignId() != "" || req.GetClearCampaignId()
	if s.bots != nil && existing.BotToken != "" && needsReconnect {
		if err := s.connectBotForTenant(ctx, existing); err != nil {
			slog.Error("mgmt: failed to reconnect bot for tenant", "tenant_id", req.GetId(), "err", err)
		}
	}

	return &pb.TenantResponse{Tenant: tenantToPB(existing)}, nil
}

// DeleteTenant implements the gRPC DeleteTenant RPC.
func (s *ManagementServer) DeleteTenant(ctx context.Context, req *pb.DeleteTenantRequest) (*pb.DeleteTenantResponse, error) {
	if err := s.store.DeleteTenant(ctx, req.GetId()); err != nil {
		return nil, status.Errorf(codes.NotFound, "tenant %q not found", req.GetId())
	}
	if s.bots != nil {
		s.bots.DisconnectBot(req.GetId())
	}
	slog.Info("mgmt: tenant deleted", "tenant_id", req.GetId())
	return &pb.DeleteTenantResponse{}, nil
}

// ── Session control ─────────────────────────────────────────────────────────

// StartWebSession implements the gRPC StartWebSession RPC.
func (s *ManagementServer) StartWebSession(ctx context.Context, req *pb.StartWebSessionRequest) (*pb.StartWebSessionResponse, error) {
	if req.GetTenantId() == "" || req.GetGuildId() == "" || req.GetChannelId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, guild_id, and channel_id are required")
	}
	if s.ctrlLookup == nil {
		return nil, status.Error(codes.Unavailable, "session control not available")
	}

	ctrl, ok := s.ctrlLookup(req.GetTenantId())
	if !ok {
		return nil, status.Errorf(codes.FailedPrecondition, "no active bot for tenant %q — bot must be connected first", req.GetTenantId())
	}

	startReq := gateway.SessionStartRequest{
		TenantID:   req.GetTenantId(),
		CampaignID: req.GetCampaignId(),
		GuildID:    req.GetGuildId(),
		ChannelID:  req.GetChannelId(),
	}
	if err := ctrl.Start(ctx, startReq); err != nil {
		slog.Warn("mgmt: start web session failed",
			"tenant_id", req.GetTenantId(),
			"guild_id", req.GetGuildId(),
			"err", err,
		)
		return nil, status.Errorf(codes.Internal, "start session: %v", err)
	}

	// Retrieve the session ID from the orchestrator via the guild's active session.
	info, found := ctrl.Info(req.GetGuildId())
	sessionID := ""
	if found {
		sessionID = info.SessionID
	}

	slog.Info("mgmt: web session started",
		"tenant_id", req.GetTenantId(),
		"session_id", sessionID,
		"guild_id", req.GetGuildId(),
	)

	return &pb.StartWebSessionResponse{SessionId: sessionID}, nil
}

// StopWebSession implements the gRPC StopWebSession RPC.
func (s *ManagementServer) StopWebSession(ctx context.Context, req *pb.StopWebSessionRequest) (*pb.StopWebSessionResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if s.orch == nil || s.ctrlLookup == nil {
		return nil, status.Error(codes.Unavailable, "session control not available")
	}

	// Look up the session to find its tenant.
	sess, err := s.orch.GetSession(ctx, req.GetSessionId())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "session %q not found", req.GetSessionId())
	}

	ctrl, ok := s.ctrlLookup(sess.TenantID)
	if !ok {
		return nil, status.Errorf(codes.FailedPrecondition, "no active controller for tenant %q", sess.TenantID)
	}

	if err := ctrl.Stop(ctx, req.GetSessionId()); err != nil {
		slog.Warn("mgmt: stop web session failed", "session_id", req.GetSessionId(), "err", err)
		return nil, status.Errorf(codes.Internal, "stop session: %v", err)
	}

	slog.Info("mgmt: web session stopped", "session_id", req.GetSessionId())
	return &pb.StopWebSessionResponse{}, nil
}

// ListActiveSessions implements the gRPC ListActiveSessions RPC.
func (s *ManagementServer) ListActiveSessions(ctx context.Context, req *pb.ListActiveSessionsRequest) (*pb.ListActiveSessionsResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if s.orch == nil {
		return nil, status.Error(codes.Unavailable, "session orchestrator not available")
	}

	sessions, err := s.orch.ActiveSessions(ctx, req.GetTenantId())
	if err != nil {
		slog.Warn("mgmt: list active sessions failed", "tenant_id", req.GetTenantId(), "err", err)
		return nil, status.Error(codes.Internal, "failed to list active sessions")
	}

	pbSessions := make([]*pb.ActiveSessionInfo, len(sessions))
	for i, sess := range sessions {
		pbSessions[i] = &pb.ActiveSessionInfo{
			SessionId:   sess.ID,
			TenantId:    sess.TenantID,
			CampaignId:  sess.CampaignID,
			GuildId:     sess.GuildID,
			ChannelId:   sess.ChannelID,
			LicenseTier: sess.LicenseTier.String(),
			State:       stringToPBState(sess.State),
			Error:       sess.Error,
			StartedAt:   timestamppb.New(sess.StartedAt),
		}
	}

	return &pb.ListActiveSessionsResponse{Sessions: pbSessions}, nil
}

// ── Usage ───────────────────────────────────────────────────────────────────

// GetUsage implements the gRPC GetUsage RPC.
func (s *ManagementServer) GetUsage(ctx context.Context, req *pb.GetUsageRequest) (*pb.GetUsageResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if s.usageStore == nil {
		return nil, status.Error(codes.Unavailable, "usage store not available")
	}

	period := usage.CurrentPeriod()
	if req.GetPeriod() != nil {
		period = req.GetPeriod().AsTime()
	}

	record, err := s.usageStore.GetUsage(ctx, req.GetTenantId(), period)
	if err != nil {
		slog.Warn("mgmt: get usage failed", "tenant_id", req.GetTenantId(), "err", err)
		return nil, status.Error(codes.Internal, "failed to get usage")
	}

	return &pb.GetUsageResponse{
		TenantId:     record.TenantID,
		Period:       timestamppb.New(period),
		SessionHours: record.SessionHours,
		LlmTokens:    record.LLMTokens,
		SttSeconds:   record.STTSeconds,
		TtsChars:     record.TTSChars,
	}, nil
}

// ── Bot status ──────────────────────────────────────────────────────────────

// GetBotStatus implements the gRPC GetBotStatus RPC.
func (s *ManagementServer) GetBotStatus(_ context.Context, req *pb.GetBotStatusRequest) (*pb.GetBotStatusResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if s.botChecker == nil {
		return &pb.GetBotStatusResponse{
			TenantId: req.GetTenantId(),
			State:    pb.BotConnectionState_BOT_CONNECTION_STATE_DISCONNECTED,
		}, nil
	}

	connected, guildCount := s.botChecker.IsBotConnected(req.GetTenantId())
	state := pb.BotConnectionState_BOT_CONNECTION_STATE_DISCONNECTED
	if connected {
		state = pb.BotConnectionState_BOT_CONNECTION_STATE_CONNECTED
	}

	return &pb.GetBotStatusResponse{
		TenantId:   req.GetTenantId(),
		State:      state,
		GuildCount: int32(guildCount),
	}, nil
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func (s *ManagementServer) connectBotForTenant(ctx context.Context, tenant gateway.Tenant) error {
	if tbc, ok := s.bots.(gateway.TenantBotConnector); ok {
		return tbc.ConnectBotForTenant(ctx, tenant)
	}
	return s.bots.ConnectBot(ctx, tenant.ID, tenant.BotToken, tenant.GuildIDs)
}

// tenantToPB converts a gateway.Tenant to a protobuf TenantInfo.
// Bot tokens are never exposed in the proto representation.
func tenantToPB(t gateway.Tenant) *pb.TenantInfo {
	return &pb.TenantInfo{
		Id:                  t.ID,
		LicenseTier:         t.LicenseTier.String(),
		GuildIds:            t.GuildIDs,
		DmRoleId:            t.DMRoleID,
		CampaignId:          t.CampaignID,
		MonthlySessionHours: t.MonthlySessionHours,
		CreatedAt:           timestamppb.New(t.CreatedAt),
		UpdatedAt:           timestamppb.New(t.UpdatedAt),
	}
}
