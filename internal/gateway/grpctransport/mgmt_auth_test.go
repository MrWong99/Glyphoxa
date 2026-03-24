package grpctransport

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestManagementAuthUnaryInterceptor(t *testing.T) {
	t.Parallel()

	dummyHandler := func(_ context.Context, _ any) (any, error) {
		return "ok", nil
	}

	tests := []struct {
		name     string
		secret   string
		method   string
		mdSecret string
		wantCode codes.Code
		wantPass bool
	}{
		{
			name:     "no secret configured — passthrough",
			secret:   "",
			method:   "/glyphoxa.v1.ManagementService/CreateTenant",
			mdSecret: "",
			wantPass: true,
		},
		{
			name:     "non-management RPC — passthrough",
			secret:   "my-secret",
			method:   "/glyphoxa.v1.GatewayService/SessionReady",
			mdSecret: "",
			wantPass: true,
		},
		{
			name:     "valid secret",
			secret:   "my-secret",
			method:   "/glyphoxa.v1.ManagementService/CreateTenant",
			mdSecret: "my-secret",
			wantPass: true,
		},
		{
			name:     "missing secret",
			secret:   "my-secret",
			method:   "/glyphoxa.v1.ManagementService/CreateTenant",
			mdSecret: "",
			wantCode: codes.Unauthenticated,
		},
		{
			name:     "wrong secret",
			secret:   "my-secret",
			method:   "/glyphoxa.v1.ManagementService/CreateTenant",
			mdSecret: "wrong-secret",
			wantCode: codes.Unauthenticated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			interceptor := ManagementAuthUnaryInterceptor(tt.secret)

			ctx := context.Background()
			if tt.mdSecret != "" {
				md := metadata.Pairs(mgmtSecretKey, tt.mdSecret)
				ctx = metadata.NewIncomingContext(ctx, md)
			}

			info := &grpc.UnaryServerInfo{FullMethod: tt.method}
			resp, err := interceptor(ctx, nil, info, dummyHandler)

			if tt.wantPass {
				if err != nil {
					t.Fatalf("expected pass, got error: %v", err)
				}
				if resp != "ok" {
					t.Errorf("expected response 'ok', got %v", resp)
				}
			} else {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Fatalf("expected gRPC status error, got: %v", err)
				}
				if st.Code() != tt.wantCode {
					t.Errorf("code = %v, want %v", st.Code(), tt.wantCode)
				}
			}
		})
	}
}

func TestMgmtSecretUnaryClientInterceptor(t *testing.T) {
	t.Parallel()

	t.Run("injects secret into metadata", func(t *testing.T) {
		t.Parallel()

		interceptor := MgmtSecretUnaryClientInterceptor("client-secret")

		var capturedCtx context.Context
		fakeInvoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			capturedCtx = ctx
			return nil
		}

		ctx := context.Background()
		err := interceptor(ctx, "/test/method", nil, nil, nil, fakeInvoker)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		md, ok := metadata.FromOutgoingContext(capturedCtx)
		if !ok {
			t.Fatal("expected outgoing metadata")
		}
		vals := md.Get(mgmtSecretKey)
		if len(vals) != 1 || vals[0] != "client-secret" {
			t.Errorf("secret in metadata = %v, want [client-secret]", vals)
		}
	})

	t.Run("no-op when empty secret", func(t *testing.T) {
		t.Parallel()

		interceptor := MgmtSecretUnaryClientInterceptor("")

		var capturedCtx context.Context
		fakeInvoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			capturedCtx = ctx
			return nil
		}

		ctx := context.Background()
		err := interceptor(ctx, "/test/method", nil, nil, nil, fakeInvoker)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		md, ok := metadata.FromOutgoingContext(capturedCtx)
		if ok && len(md.Get(mgmtSecretKey)) > 0 {
			t.Error("expected no secret in metadata when secret is empty")
		}
	})
}
