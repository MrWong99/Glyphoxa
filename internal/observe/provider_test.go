package observe_test

import (
	"context"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/observe"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestInitProvider_DefaultServiceName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	shutdown, err := observe.InitProvider(ctx, observe.ProviderConfig{})
	if err != nil {
		t.Fatalf("InitProvider: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if shutdown == nil {
		t.Fatal("shutdown function is nil")
	}
}

func TestInitProvider_CustomServiceName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	shutdown, err := observe.InitProvider(ctx, observe.ProviderConfig{
		ServiceName:    "test-service",
		ServiceVersion: "1.2.3",
	})
	if err != nil {
		t.Fatalf("InitProvider: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if shutdown == nil {
		t.Fatal("shutdown function is nil")
	}
}

func TestInitProvider_WithTraceExporter(t *testing.T) {
	// Not parallel: we need to control the global tracer provider.
	ctx := context.Background()
	exp := tracetest.NewInMemoryExporter()

	shutdown, err := observe.InitProvider(ctx, observe.ProviderConfig{
		ServiceName:   "trace-test",
		TraceExporter: exp,
	})
	if err != nil {
		t.Fatalf("InitProvider: %v", err)
	}

	// Grab the tracer provider that InitProvider just registered globally.
	tp, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	if !ok {
		t.Fatal("global tracer provider is not *sdktrace.TracerProvider")
	}

	// Create a span and end it.
	_, span := tp.Tracer("test").Start(ctx, "test-span")
	span.End()

	// Force flush so the batcher exports buffered spans.
	if err := tp.ForceFlush(ctx); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}

	spans := exp.GetSpans()
	if len(spans) == 0 {
		t.Fatal("expected at least one span to be exported")
	}
	if spans[0].Name != "test-span" {
		t.Errorf("span name = %q, want %q", spans[0].Name, "test-span")
	}

	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestInitProvider_ShutdownNoError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	shutdown, err := observe.InitProvider(ctx, observe.ProviderConfig{
		ServiceName: "shutdown-test",
	})
	if err != nil {
		t.Fatalf("InitProvider: %v", err)
	}

	if err := shutdown(ctx); err != nil {
		t.Errorf("shutdown returned error: %v", err)
	}
}

func TestInitProvider_SetsGlobalProviders(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	shutdown, err := observe.InitProvider(ctx, observe.ProviderConfig{
		ServiceName: "global-test",
	})
	if err != nil {
		t.Fatalf("InitProvider: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	// Verify the global tracer provider is an SDK TracerProvider (not noop).
	tp := otel.GetTracerProvider()
	if _, ok := tp.(*sdktrace.TracerProvider); !ok {
		t.Errorf("global tracer provider type = %T, want *sdktrace.TracerProvider", tp)
	}

	// Verify the global meter provider is set (not nil).
	mp := otel.GetMeterProvider()
	if mp == nil {
		t.Fatal("global meter provider is nil")
	}
}

func TestInitProvider_MultipleShutdownCalls(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	shutdown, err := observe.InitProvider(ctx, observe.ProviderConfig{
		ServiceName: "multi-shutdown",
	})
	if err != nil {
		t.Fatalf("InitProvider: %v", err)
	}

	// First shutdown should succeed.
	if err := shutdown(ctx); err != nil {
		t.Errorf("first shutdown returned error: %v", err)
	}

	// Second shutdown should not panic (providers tolerate double-close).
	if err := shutdown(ctx); err != nil {
		t.Logf("second shutdown returned error (acceptable): %v", err)
	}
}
