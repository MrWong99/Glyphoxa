package observe

import (
	"context"
	"log/slog"

	"github.com/MrWong99/glyphoxa/internal/config"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// GRPCServerOptions returns gRPC server options that add OTel stats handlers
// for automatic trace propagation and RPC duration metrics. Append these to
// any other server options (e.g. TLS credentials) when creating a
// [grpc.Server].
func GRPCServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(TenantUnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(TenantStreamServerInterceptor()),
	}
}

// GRPCDialOptions returns gRPC dial options that add OTel stats handlers
// for automatic trace propagation and RPC duration metrics on the client
// side. Append these to any other dial options (e.g. TLS credentials)
// when creating a [grpc.ClientConn].
func GRPCDialOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
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
