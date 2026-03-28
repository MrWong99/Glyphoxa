package grpctransport

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	// mgmtSecretKey is the gRPC metadata key used to transmit the
	// management shared secret.
	mgmtSecretKey = "x-mgmt-secret"

	// managementServicePrefix matches all ManagementService RPCs.
	managementServicePrefix = "/glyphoxa.v1.ManagementService/"
)

// ManagementAuthUnaryInterceptor returns a [grpc.UnaryServerInterceptor] that
// validates a shared secret in gRPC metadata for ManagementService RPCs.
// Other services (GatewayService, AudioBridgeService) pass through unchecked.
//
// If secret is empty, all ManagementService RPCs are rejected (fail-closed).
// Set GLYPHOXA_GRPC_MGMT_SECRET in production.
func ManagementAuthUnaryInterceptor(secret string) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if !strings.HasPrefix(info.FullMethod, managementServicePrefix) {
			return handler(ctx, req)
		}
		if secret == "" {
			slog.Error("mgmt-auth: GLYPHOXA_GRPC_MGMT_SECRET not configured — rejecting management RPC", "method", info.FullMethod)
			return nil, status.Error(codes.Unavailable, "management secret not configured — set GLYPHOXA_GRPC_MGMT_SECRET")
		}
		if err := verifyMgmtSecret(ctx, secret); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// ManagementAuthStreamInterceptor returns a [grpc.StreamServerInterceptor]
// that validates a shared secret for ManagementService streaming RPCs.
//
// If secret is empty, all ManagementService streaming RPCs are rejected (fail-closed).
func ManagementAuthStreamInterceptor(secret string) grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if !strings.HasPrefix(info.FullMethod, managementServicePrefix) {
			return handler(srv, ss)
		}
		if secret == "" {
			slog.Error("mgmt-auth: GLYPHOXA_GRPC_MGMT_SECRET not configured — rejecting management RPC", "method", info.FullMethod)
			return status.Error(codes.Unavailable, "management secret not configured — set GLYPHOXA_GRPC_MGMT_SECRET")
		}
		if err := verifyMgmtSecret(ss.Context(), secret); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

// verifyMgmtSecret extracts the shared secret from gRPC metadata and compares
// it against the expected value using constant-time comparison.
func verifyMgmtSecret(ctx context.Context, expected string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		slog.Warn("mgmt-auth: request missing metadata")
		return status.Error(codes.Unauthenticated, "missing authentication metadata")
	}

	vals := md.Get(mgmtSecretKey)
	if len(vals) == 0 || vals[0] == "" {
		slog.Warn("mgmt-auth: request missing shared secret")
		return status.Error(codes.Unauthenticated, "missing management secret")
	}

	if subtle.ConstantTimeCompare([]byte(vals[0]), []byte(expected)) != 1 {
		slog.Warn("mgmt-auth: invalid shared secret")
		return status.Error(codes.Unauthenticated, "invalid management secret")
	}

	return nil
}

// MgmtSecretUnaryClientInterceptor returns a [grpc.UnaryClientInterceptor]
// that injects the shared secret into outgoing gRPC metadata.
// If secret is empty the interceptor is a no-op.
func MgmtSecretUnaryClientInterceptor(secret string) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		if secret != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, mgmtSecretKey, secret)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
