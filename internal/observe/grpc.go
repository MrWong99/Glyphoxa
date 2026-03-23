package observe

import (
	"context"
	"log/slog"
	"time"

	"github.com/MrWong99/glyphoxa/internal/config"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// GRPCServerOptions returns gRPC server options that add OTel stats handlers
// for automatic trace propagation and RPC duration metrics. Append these to
// any other server options (e.g. TLS credentials) when creating a
// [grpc.Server].
//
// Keepalive parameters are configured to keep long-lived bidirectional streams
// (e.g. AudioBridgeService.StreamAudio) alive across infrastructure boundaries
// (load balancers, K8s service proxies, NAT devices) that may terminate idle
// HTTP/2 connections.
func GRPCServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(TenantUnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(TenantStreamServerInterceptor()),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			// Send pings every 30s when there is no activity on the stream.
			Time: 30 * time.Second,
			// Wait 10s for a ping ack before considering the connection dead.
			Timeout: 10 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			// Allow clients to send pings as often as every 10s.
			MinTime: 10 * time.Second,
			// Allow pings even when there are no active streams, so the
			// transport stays alive between session starts.
			PermitWithoutStream: true,
		}),
	}
}

// GRPCDialOptions returns gRPC dial options that add OTel stats handlers
// for automatic trace propagation and RPC duration metrics on the client
// side. Append these to any other dial options (e.g. TLS credentials)
// when creating a [grpc.ClientConn].
//
// Client-side keepalive pings keep long-lived streams alive across
// infrastructure that may terminate idle HTTP/2 connections.
func GRPCDialOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			// Send pings every 30s when there is no activity.
			Time: 30 * time.Second,
			// Wait 10s for a ping ack before considering the connection dead.
			Timeout: 10 * time.Second,
			// Send pings even when there are no active RPCs, so the
			// transport stays alive between sessions.
			PermitWithoutStream: true,
		}),
	}
}

// tenantMDKey is the gRPC metadata key used to propagate tenant_id.
const tenantMDKey = "x-tenant-id"

// TenantUnaryServerInterceptor returns a [grpc.UnaryServerInterceptor] that
// extracts tenant_id from incoming gRPC metadata and injects a
// [config.TenantContext] into the request context. If no tenant_id metadata
// is present the request proceeds without tenant context.
func TenantUnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		ctx = enrichWithTenant(ctx)
		return handler(ctx, req)
	}
}

// TenantStreamServerInterceptor returns a [grpc.StreamServerInterceptor]
// that extracts tenant_id from incoming gRPC metadata and injects a
// [config.TenantContext] into the stream context.
func TenantStreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		ctx := enrichWithTenant(ss.Context())
		wrapped := &tenantServerStream{ServerStream: ss, ctx: ctx}
		return handler(srv, wrapped)
	}
}

// enrichWithTenant extracts tenant_id from gRPC metadata and returns a
// context enriched with [config.TenantContext]. Returns ctx unchanged if
// no tenant metadata is present.
func enrichWithTenant(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}

	vals := md.Get(tenantMDKey)
	if len(vals) == 0 || vals[0] == "" {
		return ctx
	}

	tenantID := vals[0]
	slog.Debug("gRPC: tenant context extracted", "tenant_id", tenantID)

	tc := config.TenantContext{
		TenantID: tenantID,
	}
	return config.WithTenant(ctx, tc)
}

// tenantServerStream wraps a [grpc.ServerStream] to override Context()
// with a tenant-enriched context.
type tenantServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context returns the tenant-enriched context.
func (s *tenantServerStream) Context() context.Context {
	return s.ctx
}
