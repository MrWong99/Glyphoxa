package observe

import (
	"context"
	"log/slog"

	"github.com/MrWong99/glyphoxa/internal/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// tracerName is the instrumentation scope name for the Glyphoxa tracer.
const tracerName = "github.com/MrWong99/glyphoxa"

// Tracer returns the package-level [trace.Tracer] for Glyphoxa. It uses the
// globally registered [trace.TracerProvider].
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// StartSpan starts a new span and returns the updated context and span.
// If the context carries a [config.TenantContext], tenant_id is automatically
// added as a span attribute. The caller must call span.End() when done.
func StartSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if tc, ok := config.TenantFromContext(ctx); ok {
		opts = append(opts, trace.WithAttributes(
			attribute.String("tenant_id", tc.TenantID),
		))
	}
	return Tracer().Start(ctx, name, opts...)
}

// CorrelationID extracts the trace ID from the OTel span context in ctx.
// Returns the empty string when no active span with a valid trace ID exists.
//
// This provides backward compatibility with code that used the old
// correlation ID system — the trace ID serves as the correlation identifier.
func CorrelationID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if sc.HasTraceID() {
		return sc.TraceID().String()
	}
	return ""
}

// Logger returns an [slog.Logger] enriched with trace_id and span_id from
// the OTel span context in ctx. When a [config.TenantContext] is present,
// tenant_id and campaign_id are also included. When no active span is
// present, the returned logger is the default slog logger (still enriched
// with tenant fields if available).
func Logger(ctx context.Context) *slog.Logger {
	l := slog.Default()
	sc := trace.SpanContextFromContext(ctx)
	if sc.HasTraceID() {
		l = l.With(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	if tc, ok := config.TenantFromContext(ctx); ok {
		l = l.With(
			slog.String("tenant_id", tc.TenantID),
			slog.String("campaign_id", tc.CampaignID),
		)
	}
	return l
}
