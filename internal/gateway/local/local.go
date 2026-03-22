// Package local provides in-process implementations of the gateway contracts
// for --mode=full. Instead of gRPC, function calls happen directly within
// the same process.
package local

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/MrWong99/glyphoxa/internal/gateway"
)

// Compile-time interface assertions.
var (
	_ gateway.WorkerClient    = (*Client)(nil)
	_ gateway.GatewayCallback = (*Callback)(nil)
	_ gateway.NPCController   = (*NPCClient)(nil)
)

// SessionStartFunc is called by Client.StartSession to run the voice pipeline.
// It receives the start request and should block until the session is running.
type SessionStartFunc func(ctx context.Context, req gateway.StartSessionRequest) error

// SessionStopFunc is called by Client.StopSession to stop the voice pipeline.
type SessionStopFunc func(ctx context.Context, sessionID string) error

// Client implements WorkerClient as direct in-process function calls.
// Used by --mode=full where no gRPC boundary exists.
type Client struct {
	mu       sync.Mutex
	startFn  SessionStartFunc
	stopFn   SessionStopFunc
	sessions map[string]gateway.SessionStatus
}

// NewClient creates a local WorkerClient. The start and stop functions are called
// directly when StartSession and StopSession are invoked.
func NewClient(startFn SessionStartFunc, stopFn SessionStopFunc) *Client {
	return &Client{
		startFn:  startFn,
		stopFn:   stopFn,
		sessions: make(map[string]gateway.SessionStatus),
	}
}

// StartSession calls the start function directly and tracks the session.
func (c *Client) StartSession(ctx context.Context, req gateway.StartSessionRequest) error {
	c.mu.Lock()
	c.sessions[req.SessionID] = gateway.SessionStatus{
		SessionID: req.SessionID,
		State:     gateway.SessionPending,
		StartedAt: time.Now().UTC(),
	}
	c.mu.Unlock()

	if err := c.startFn(ctx, req); err != nil {
		c.mu.Lock()
		c.sessions[req.SessionID] = gateway.SessionStatus{
			SessionID: req.SessionID,
			State:     gateway.SessionEnded,
			StartedAt: time.Now().UTC(),
			Error:     err.Error(),
		}
		c.mu.Unlock()
		return fmt.Errorf("local: start session: %w", err)
	}

	c.mu.Lock()
	c.sessions[req.SessionID] = gateway.SessionStatus{
		SessionID: req.SessionID,
		State:     gateway.SessionActive,
		StartedAt: time.Now().UTC(),
	}
	c.mu.Unlock()

	slog.Info("local: session started", "session_id", req.SessionID)
	return nil
}

// StopSession calls the stop function directly and marks the session as ended.
func (c *Client) StopSession(ctx context.Context, sessionID string) error {
	if err := c.stopFn(ctx, sessionID); err != nil {
		return fmt.Errorf("local: stop session: %w", err)
	}

	c.mu.Lock()
	if s, ok := c.sessions[sessionID]; ok {
		s.State = gateway.SessionEnded
		c.sessions[sessionID] = s
	}
	c.mu.Unlock()

	slog.Info("local: session stopped", "session_id", sessionID)
	return nil
}

// GetStatus returns the status of all tracked sessions.
func (c *Client) GetStatus(_ context.Context) ([]gateway.SessionStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	result := make([]gateway.SessionStatus, 0, len(c.sessions))
	for _, s := range c.sessions {
		result = append(result, s)
	}
	return result, nil
}

// NPCClient implements NPCController via direct orchestrator calls for full mode.
type NPCClient struct {
	handler gateway.NPCController
}

// NewNPCClient creates a local NPCController that delegates to the given handler.
func NewNPCClient(handler gateway.NPCController) *NPCClient {
	return &NPCClient{handler: handler}
}

// ListNPCs delegates to the handler.
func (n *NPCClient) ListNPCs(ctx context.Context, sessionID string) ([]gateway.NPCStatus, error) {
	return n.handler.ListNPCs(ctx, sessionID)
}

// MuteNPC delegates to the handler.
func (n *NPCClient) MuteNPC(ctx context.Context, sessionID, npcName string) error {
	return n.handler.MuteNPC(ctx, sessionID, npcName)
}

// UnmuteNPC delegates to the handler.
func (n *NPCClient) UnmuteNPC(ctx context.Context, sessionID, npcName string) error {
	return n.handler.UnmuteNPC(ctx, sessionID, npcName)
}

// MuteAllNPCs delegates to the handler.
func (n *NPCClient) MuteAllNPCs(ctx context.Context, sessionID string) (int, error) {
	return n.handler.MuteAllNPCs(ctx, sessionID)
}

// UnmuteAllNPCs delegates to the handler.
func (n *NPCClient) UnmuteAllNPCs(ctx context.Context, sessionID string) (int, error) {
	return n.handler.UnmuteAllNPCs(ctx, sessionID)
}

// SpeakNPC delegates to the handler.
func (n *NPCClient) SpeakNPC(ctx context.Context, sessionID, npcName, text string) error {
	return n.handler.SpeakNPC(ctx, sessionID, npcName, text)
}

// Callback implements GatewayCallback as no-ops for full mode.
// In full mode the gateway and worker are the same process, so
// state reporting and heartbeats are unnecessary.
type Callback struct{}

// ReportState is a no-op in full mode.
func (c *Callback) ReportState(_ context.Context, sessionID string, state gateway.SessionState, _ string) error {
	slog.Debug("local: state reported (no-op)", "session_id", sessionID, "state", state)
	return nil
}

// Heartbeat is a no-op in full mode.
func (c *Callback) Heartbeat(_ context.Context, sessionID string) error {
	slog.Debug("local: heartbeat (no-op)", "session_id", sessionID)
	return nil
}
