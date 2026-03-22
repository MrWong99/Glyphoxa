package usage

import (
	"context"
	"log/slog"
	"time"

	"github.com/MrWong99/glyphoxa/internal/gateway"
	"github.com/MrWong99/glyphoxa/internal/gateway/sessionorch"
)

// Compile-time interface assertion.
var _ gateway.GatewayCallback = (*RecordingBridge)(nil)

// RecordingBridge wraps a [gateway.GatewayCallback] and records session
// hours in the usage [Store] when a session transitions to ended.
// It looks up session start times from the orchestrator to compute duration.
type RecordingBridge struct {
	inner gateway.GatewayCallback
	orch  sessionorch.Orchestrator
	usage Store
}

// NewRecordingBridge creates a GatewayCallback that records session hours
// on session end, then delegates to the inner callback.
func NewRecordingBridge(inner gateway.GatewayCallback, orch sessionorch.Orchestrator, usage Store) *RecordingBridge {
	return &RecordingBridge{inner: inner, orch: orch, usage: usage}
}

// ReportState delegates to the inner callback. When the state is
// [gateway.SessionEnded], it records the session duration in the usage store.
func (b *RecordingBridge) ReportState(ctx context.Context, sessionID string, state gateway.SessionState, errMsg string) error {
	// Record usage before transitioning (session is still accessible).
	if state == gateway.SessionEnded {
		b.recordSessionHours(ctx, sessionID)
	}

	return b.inner.ReportState(ctx, sessionID, state, errMsg)
}

// Heartbeat delegates directly to the inner callback.
func (b *RecordingBridge) Heartbeat(ctx context.Context, sessionID string) error {
	return b.inner.Heartbeat(ctx, sessionID)
}

// recordSessionHours looks up the session, computes its duration, and
// records it in the usage store. Errors are logged but not propagated
// — session lifecycle must not fail due to metering errors.
func (b *RecordingBridge) recordSessionHours(ctx context.Context, sessionID string) {
	sess, err := b.orch.GetSession(ctx, sessionID)
	if err != nil {
		slog.Warn("usage: failed to look up session for recording", "session_id", sessionID, "err", err)
		return
	}

	if sess.StartedAt.IsZero() {
		slog.Warn("usage: session has no start time, skipping usage recording", "session_id", sessionID)
		return
	}

	duration := time.Since(sess.StartedAt)
	hours := duration.Hours()
	if hours <= 0 {
		return
	}

	delta := Record{
		TenantID:     sess.TenantID,
		Period:       CurrentPeriod(),
		SessionHours: hours,
	}

	if err := b.usage.RecordUsage(ctx, sess.TenantID, delta); err != nil {
		slog.Warn("usage: failed to record session hours",
			"session_id", sessionID,
			"tenant_id", sess.TenantID,
			"hours", hours,
			"err", err,
		)
		return
	}

	slog.Info("usage: recorded session hours",
		"session_id", sessionID,
		"tenant_id", sess.TenantID,
		"hours", hours,
	)
}
