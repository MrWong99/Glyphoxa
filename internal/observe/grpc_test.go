package observe_test

import (
	"context"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/observe"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestGRPCServerOptions_NonEmpty(t *testing.T) {
	t.Parallel()
	opts := observe.GRPCServerOptions()
	if len(opts) == 0 {
		t.Fatal("GRPCServerOptions returned empty slice")
	}
}

func TestGRPCDialOptions_NonEmpty(t *testing.T) {
	t.Parallel()
	opts := observe.GRPCDialOptions()
	if len(opts) == 0 {
		t.Fatal("GRPCDialOptions returned empty slice")
	}
}

func TestTenantUnaryServerInterceptor_WithTenant(t *testing.T) {
	t.Parallel()
	interceptor := observe.TenantUnaryServerInterceptor()

	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("x-tenant-id", "test-tenant"),
	)

	var handlerCtx context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		handlerCtx = ctx
		return "ok", nil
	}

	resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}
	if resp != "ok" {
		t.Errorf("handler response = %v, want %q", resp, "ok")
	}

	tc, ok := config.TenantFromContext(handlerCtx)
	if !ok {
		t.Fatal("tenant context not set by interceptor")
	}
	if tc.TenantID != "test-tenant" {
		t.Errorf("tenant ID = %q, want %q", tc.TenantID, "test-tenant")
	}
}

func TestTenantUnaryServerInterceptor_NoMetadata(t *testing.T) {
	t.Parallel()
	interceptor := observe.TenantUnaryServerInterceptor()

	var handlerCtx context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		handlerCtx = ctx
		return "ok", nil
	}

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, handler)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}

	_, ok := config.TenantFromContext(handlerCtx)
	if ok {
		t.Error("tenant context should not be set when no metadata is present")
	}
}

func TestTenantUnaryServerInterceptor_EmptyTenantID(t *testing.T) {
	t.Parallel()
	interceptor := observe.TenantUnaryServerInterceptor()

	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("x-tenant-id", ""),
	)

	var handlerCtx context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		handlerCtx = ctx
		return "ok", nil
	}

	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}

	_, ok := config.TenantFromContext(handlerCtx)
	if ok {
		t.Error("tenant context should not be set when tenant ID is empty")
	}
}

// fakeServerStream implements grpc.ServerStream for testing the stream interceptor.
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context {
	return f.ctx
}

func TestTenantStreamServerInterceptor_WithTenant(t *testing.T) {
	t.Parallel()
	interceptor := observe.TenantStreamServerInterceptor()

	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("x-tenant-id", "stream-tenant"),
	)
	stream := &fakeServerStream{ctx: ctx}

	var handlerStream grpc.ServerStream
	handler := func(srv any, ss grpc.ServerStream) error {
		handlerStream = ss
		return nil
	}

	err := interceptor(nil, stream, &grpc.StreamServerInfo{}, handler)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}

	tc, ok := config.TenantFromContext(handlerStream.Context())
	if !ok {
		t.Fatal("tenant context not set by stream interceptor")
	}
	if tc.TenantID != "stream-tenant" {
		t.Errorf("tenant ID = %q, want %q", tc.TenantID, "stream-tenant")
	}
}

func TestTenantStreamServerInterceptor_NoMetadata(t *testing.T) {
	t.Parallel()
	interceptor := observe.TenantStreamServerInterceptor()

	stream := &fakeServerStream{ctx: context.Background()}

	var handlerStream grpc.ServerStream
	handler := func(srv any, ss grpc.ServerStream) error {
		handlerStream = ss
		return nil
	}

	err := interceptor(nil, stream, &grpc.StreamServerInfo{}, handler)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}

	_, ok := config.TenantFromContext(handlerStream.Context())
	if ok {
		t.Error("tenant context should not be set when no metadata is present")
	}
}

func TestTenantStreamServerInterceptor_WrappedStreamContext(t *testing.T) {
	t.Parallel()
	interceptor := observe.TenantStreamServerInterceptor()

	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("x-tenant-id", "wrapped-tenant"),
	)
	stream := &fakeServerStream{ctx: ctx}

	var handlerStream grpc.ServerStream
	handler := func(srv any, ss grpc.ServerStream) error {
		handlerStream = ss
		return nil
	}

	err := interceptor(nil, stream, &grpc.StreamServerInfo{}, handler)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}

	// Verify that the wrapped stream's Context() returns the enriched context,
	// not the original stream's context.
	wrappedCtx := handlerStream.Context()
	tc, ok := config.TenantFromContext(wrappedCtx)
	if !ok {
		t.Fatal("wrapped stream context should contain tenant")
	}
	if tc.TenantID != "wrapped-tenant" {
		t.Errorf("tenant ID = %q, want %q", tc.TenantID, "wrapped-tenant")
	}

	// Original stream's context should NOT have tenant (it was enriched on the wrapper).
	_, ok = config.TenantFromContext(stream.Context())
	if ok {
		t.Error("original stream context should not have been modified")
	}
}
