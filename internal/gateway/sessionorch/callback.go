package sessionorch

import (
	"context"
	"fmt"

	"github.com/MrWong99/glyphoxa/internal/gateway"
)

// Compile-time interface assertion.
var _ gateway.GatewayCallback = (*CallbackBridge)(nil)

// CallbackBridge implements gateway.GatewayCallback by delegating to an
// Orchestrator. Used by the gateway to handle worker state reports and
// heartbeats.
type CallbackBridge struct {
	orch Orchestrator
}

// NewCallbackBridge creates a GatewayCallback that delegates to the given
// Orchestrator.
func NewCallbackBridge(orch Orchestrator) *CallbackBridge {
	return &CallbackBridge{orch: orch}
}

// ReportState transitions the session to the given state in the orchestrator.
func (cb *CallbackBridge) ReportState(ctx context.Context, sessionID string, state gateway.SessionState, errMsg string) error {
	if err := cb.orch.Transition(ctx, sessionID, state, errMsg); err != nil {
		return fmt.Errorf("sessionorch: report state: %w", err)
	}
	return nil
}

// Heartbeat records a heartbeat for the session in the orchestrator.
func (cb *CallbackBridge) Heartbeat(ctx context.Context, sessionID string) error {
	if err := cb.orch.RecordHeartbeat(ctx, sessionID); err != nil {
		return fmt.Errorf("sessionorch: heartbeat: %w", err)
	}
	return nil
}
