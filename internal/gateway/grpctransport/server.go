package grpctransport

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
	"github.com/MrWong99/glyphoxa/internal/gateway"
)

// WorkerServer implements the SessionWorker gRPC service on the worker side.
// It delegates to a WorkerHandler which owns the actual voice pipeline logic.
type WorkerServer struct {
	pb.UnimplementedSessionWorkerServiceServer
	handler WorkerHandler
}

// WorkerHandler defines the operations that a worker must implement.
// The gRPC server delegates all calls to this handler.
type WorkerHandler interface {
	StartSession(ctx context.Context, req gateway.StartSessionRequest) error
	StopSession(ctx context.Context, sessionID string) error
	GetStatus(ctx context.Context) ([]gateway.SessionStatus, error)
}

// NewWorkerServer creates a gRPC server that delegates to the given handler.
func NewWorkerServer(handler WorkerHandler) *WorkerServer {
	return &WorkerServer{handler: handler}
}

// Register adds the WorkerServer to a gRPC server.
func (s *WorkerServer) Register(gs *grpc.Server) {
	pb.RegisterSessionWorkerServiceServer(gs, s)
}

// StartSession implements the gRPC StartSession RPC.
func (s *WorkerServer) StartSession(ctx context.Context, req *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
	err := s.handler.StartSession(ctx, gateway.StartSessionRequest{
		SessionID:   req.GetSessionId(),
		TenantID:    req.GetTenantId(),
		CampaignID:  req.GetCampaignId(),
		GuildID:     req.GetGuildId(),
		ChannelID:   req.GetChannelId(),
		LicenseTier: req.GetLicenseTier(),
	})
	if err != nil {
		slog.Warn("grpc: start session failed", "session_id", req.GetSessionId(), "err", err)
		return &pb.StartSessionResponse{
			SessionId: req.GetSessionId(),
			Error:     err.Error(),
		}, status.Errorf(codes.Internal, "start session: %v", err)
	}

	return &pb.StartSessionResponse{
		SessionId: req.GetSessionId(),
	}, nil
}

// StopSession implements the gRPC StopSession RPC.
func (s *WorkerServer) StopSession(ctx context.Context, req *pb.StopSessionRequest) (*pb.StopSessionResponse, error) {
	if err := s.handler.StopSession(ctx, req.GetSessionId()); err != nil {
		slog.Warn("grpc: stop session failed", "session_id", req.GetSessionId(), "err", err)
		return &pb.StopSessionResponse{}, status.Errorf(codes.Internal, "stop session: %v", err)
	}
	return &pb.StopSessionResponse{}, nil
}

// GetStatus implements the gRPC GetStatus RPC.
func (s *WorkerServer) GetStatus(ctx context.Context, _ *pb.GetStatusRequest) (*pb.GetStatusResponse, error) {
	statuses, err := s.handler.GetStatus(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get status: %v", err)
	}

	pbStatuses := make([]*pb.SessionStatus, len(statuses))
	for i, st := range statuses {
		pbStatuses[i] = statusToPB(st)
	}

	return &pb.GetStatusResponse{Sessions: pbStatuses}, nil
}

// GatewayServer implements the SessionGateway gRPC service on the gateway side.
// It receives callbacks from workers (state reports and heartbeats).
type GatewayServer struct {
	pb.UnimplementedSessionGatewayServiceServer
	callback gateway.GatewayCallback
}

// NewGatewayServer creates a gRPC server that delegates callbacks to the given handler.
func NewGatewayServer(callback gateway.GatewayCallback) *GatewayServer {
	return &GatewayServer{callback: callback}
}

// Register adds the GatewayServer to a gRPC server.
func (s *GatewayServer) Register(gs *grpc.Server) {
	pb.RegisterSessionGatewayServiceServer(gs, s)
}

// ReportState implements the gRPC ReportState RPC.
func (s *GatewayServer) ReportState(ctx context.Context, req *pb.ReportStateRequest) (*pb.ReportStateResponse, error) {
	state, ok := gateway.ParseSessionState(pbStateToString(req.GetState()))
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "unknown state: %v", req.GetState())
	}

	if err := s.callback.ReportState(ctx, req.GetSessionId(), state); err != nil {
		return nil, status.Errorf(codes.Internal, "report state: %v", err)
	}

	return &pb.ReportStateResponse{}, nil
}

// Heartbeat implements the gRPC Heartbeat RPC.
func (s *GatewayServer) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	if err := s.callback.Heartbeat(ctx, req.GetSessionId()); err != nil {
		return nil, status.Errorf(codes.Internal, "heartbeat: %v", err)
	}

	return &pb.HeartbeatResponse{}, nil
}

// GatewayClient implements GatewayCallback by wrapping a gRPC connection
// to the gateway. Used by workers in distributed mode.
type GatewayClient struct {
	conn   *grpc.ClientConn
	client pb.SessionGatewayServiceClient
}

// Compile-time interface assertion.
var _ gateway.GatewayCallback = (*GatewayClient)(nil)

// NewGatewayClient creates a gRPC GatewayCallback connected to the gateway.
func NewGatewayClient(conn *grpc.ClientConn) *GatewayClient {
	return &GatewayClient{
		conn:   conn,
		client: pb.NewSessionGatewayServiceClient(conn),
	}
}

// ReportState reports a session state change to the gateway via gRPC.
func (c *GatewayClient) ReportState(ctx context.Context, sessionID string, state gateway.SessionState) error {
	_, err := c.client.ReportState(ctx, &pb.ReportStateRequest{
		SessionId: sessionID,
		State:     stringToPBState(state),
	})
	return err
}

// Heartbeat sends a heartbeat to the gateway via gRPC.
func (c *GatewayClient) Heartbeat(ctx context.Context, sessionID string) error {
	_, err := c.client.Heartbeat(ctx, &pb.HeartbeatRequest{
		SessionId: sessionID,
	})
	return err
}

// TimestampPB converts a gateway.SessionStatus.StartedAt to a protobuf Timestamp.
// Exported for use in tests.
func TimestampPB(st gateway.SessionStatus) *timestamppb.Timestamp {
	return timestamppb.New(st.StartedAt)
}
