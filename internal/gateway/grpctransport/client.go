// Package grpctransport provides gRPC-backed implementations of the gateway
// contracts for distributed mode (--mode=gateway and --mode=worker).
package grpctransport

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
	"github.com/MrWong99/glyphoxa/internal/gateway"
	"github.com/MrWong99/glyphoxa/internal/resilience"
)

// Compile-time interface assertion.
var _ gateway.WorkerClient = (*Client)(nil)

// Client implements WorkerClient by wrapping a gRPC connection to a worker.
// Each connection is protected by a circuit breaker that fast-fails on
// unhealthy workers instead of waiting for gRPC timeouts.
type Client struct {
	conn    *grpc.ClientConn
	client  pb.SessionWorkerServiceClient
	breaker *resilience.CircuitBreaker
}

// NewClient creates a gRPC WorkerClient connected to the given address.
func NewClient(addr string, opts ...grpc.DialOption) (*Client, error) {
	if len(opts) == 0 {
		opts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}

	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("grpctransport: dial %q: %w", addr, err)
	}

	return &Client{
		conn:   conn,
		client: pb.NewSessionWorkerServiceClient(conn),
		breaker: resilience.NewCircuitBreaker(resilience.CircuitBreakerConfig{
			Name:         "worker-" + addr,
			MaxFailures:  5,
			ResetTimeout: 30 * time.Second,
			HalfOpenMax:  3,
		}),
	}, nil
}

// StartSession sends a start request to the worker via gRPC, wrapped
// in a circuit breaker.
func (c *Client) StartSession(ctx context.Context, req gateway.StartSessionRequest) error {
	return c.breaker.Execute(func() error {
		_, err := c.client.StartSession(ctx, &pb.StartSessionRequest{
			SessionId:   req.SessionID,
			TenantId:    req.TenantID,
			CampaignId:  req.CampaignID,
			GuildId:     req.GuildID,
			ChannelId:   req.ChannelID,
			LicenseTier: req.LicenseTier,
		})
		if err != nil {
			return fmt.Errorf("grpctransport: start session: %w", err)
		}
		return nil
	})
}

// StopSession sends a stop request to the worker via gRPC, wrapped
// in a circuit breaker.
func (c *Client) StopSession(ctx context.Context, sessionID string) error {
	return c.breaker.Execute(func() error {
		_, err := c.client.StopSession(ctx, &pb.StopSessionRequest{
			SessionId: sessionID,
		})
		if err != nil {
			return fmt.Errorf("grpctransport: stop session: %w", err)
		}
		return nil
	})
}

// GetStatus queries the worker for its session status, wrapped in a
// circuit breaker.
func (c *Client) GetStatus(ctx context.Context) ([]gateway.SessionStatus, error) {
	var result []gateway.SessionStatus
	err := c.breaker.Execute(func() error {
		resp, err := c.client.GetStatus(ctx, &pb.GetStatusRequest{})
		if err != nil {
			return fmt.Errorf("grpctransport: get status: %w", err)
		}
		result = make([]gateway.SessionStatus, len(resp.GetSessions()))
		for i, s := range resp.GetSessions() {
			state, _ := gateway.ParseSessionState(pbStateToString(s.GetState()))
			result[i] = gateway.SessionStatus{
				SessionID: s.GetSessionId(),
				State:     state,
				StartedAt: s.GetStartedAt().AsTime(),
				Error:     s.GetError(),
			}
		}
		return nil
	})
	return result, err
}

// Close closes the underlying gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// pbStateToString converts a protobuf SessionState to its string form.
func pbStateToString(s pb.SessionState) string {
	switch s {
	case pb.SessionState_SESSION_STATE_PENDING:
		return "pending"
	case pb.SessionState_SESSION_STATE_ACTIVE:
		return "active"
	case pb.SessionState_SESSION_STATE_ENDED:
		return "ended"
	default:
		return "unknown"
	}
}

// stringToPBState converts a string session state to the protobuf enum.
func stringToPBState(s gateway.SessionState) pb.SessionState {
	switch s {
	case gateway.SessionPending:
		return pb.SessionState_SESSION_STATE_PENDING
	case gateway.SessionActive:
		return pb.SessionState_SESSION_STATE_ACTIVE
	case gateway.SessionEnded:
		return pb.SessionState_SESSION_STATE_ENDED
	default:
		return pb.SessionState_SESSION_STATE_UNSPECIFIED
	}
}

// statusToPB converts a gateway.SessionStatus to a protobuf SessionStatus.
func statusToPB(s gateway.SessionStatus) *pb.SessionStatus {
	return &pb.SessionStatus{
		SessionId: s.SessionID,
		State:     stringToPBState(s.State),
		StartedAt: timestamppb.New(s.StartedAt),
		Error:     s.Error,
	}
}
