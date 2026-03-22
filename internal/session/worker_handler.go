package session

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/MrWong99/glyphoxa/internal/agent/orchestrator"
	"github.com/MrWong99/glyphoxa/internal/gateway"
	"github.com/MrWong99/glyphoxa/internal/gateway/grpctransport"
)

// Compile-time interface assertion.
var _ grpctransport.WorkerHandler = (*WorkerHandler)(nil)

// RuntimeFactory creates a Runtime for a session request.
// In practice this builds providers, agents, and engines from config.
type RuntimeFactory func(ctx context.Context, req gateway.StartSessionRequest) (*Runtime, error)

// WorkerHandler manages SessionRuntime instances on a worker pod.
// It implements grpctransport.WorkerHandler to receive gRPC calls from
// the gateway.
//
// All methods are safe for concurrent use.
type WorkerHandler struct {
	mu       sync.Mutex
	sessions map[string]*managedSession
	factory  RuntimeFactory
	callback gateway.GatewayCallback
}

// managedSession tracks a running Runtime and its metadata.
type managedSession struct {
	runtime   *Runtime
	state     gateway.SessionState
	startedAt time.Time
	cancel    context.CancelFunc
}

// NewWorkerHandler creates a WorkerHandler.
//
// factory creates Runtime instances for new sessions.
// callback is used to report state changes and heartbeats back to the gateway.
func NewWorkerHandler(factory RuntimeFactory, callback gateway.GatewayCallback) *WorkerHandler {
	return &WorkerHandler{
		sessions: make(map[string]*managedSession),
		factory:  factory,
		callback: callback,
	}
}

// StartSession creates and starts a Runtime for the given request.
func (h *WorkerHandler) StartSession(ctx context.Context, req gateway.StartSessionRequest) error {
	h.mu.Lock()
	if _, exists := h.sessions[req.SessionID]; exists {
		h.mu.Unlock()
		return fmt.Errorf("session: %q already running", req.SessionID)
	}
	h.mu.Unlock()

	rt, err := h.factory(ctx, req)
	if err != nil {
		return fmt.Errorf("session: create runtime for %q: %w", req.SessionID, err)
	}

	sessionCtx, cancel := context.WithCancel(context.Background())

	if err := rt.Start(sessionCtx, nil); err != nil {
		cancel()
		return fmt.Errorf("session: start runtime for %q: %w", req.SessionID, err)
	}

	h.mu.Lock()
	h.sessions[req.SessionID] = &managedSession{
		runtime:   rt,
		state:     gateway.SessionActive,
		startedAt: time.Now().UTC(),
		cancel:    cancel,
	}
	h.mu.Unlock()

	// Report active state to gateway.
	if h.callback != nil {
		if err := h.callback.ReportState(ctx, req.SessionID, gateway.SessionActive, ""); err != nil {
			slog.Warn("session: failed to report active state", "session_id", req.SessionID, "err", err)
		}
	}

	slog.Info("session: started", "session_id", req.SessionID)
	return nil
}

// StopSession stops a running Runtime.
func (h *WorkerHandler) StopSession(ctx context.Context, sessionID string) error {
	h.mu.Lock()
	ms, ok := h.sessions[sessionID]
	if !ok {
		h.mu.Unlock()
		return fmt.Errorf("session: %q not found", sessionID)
	}
	// Remove from map before stopping to prevent double-stop.
	delete(h.sessions, sessionID)
	h.mu.Unlock()

	ms.cancel()
	var errMsg string
	if err := ms.runtime.Stop(ctx); err != nil {
		errMsg = err.Error()
		slog.Warn("session: stop error", "session_id", sessionID, "err", err)
	}

	// Report ended state to gateway, forwarding any runtime error.
	if h.callback != nil {
		if err := h.callback.ReportState(ctx, sessionID, gateway.SessionEnded, errMsg); err != nil {
			slog.Warn("session: failed to report ended state", "session_id", sessionID, "err", err)
		}
	}

	slog.Info("session: stopped", "session_id", sessionID)
	return nil
}

// GetStatus returns the status of all managed sessions.
func (h *WorkerHandler) GetStatus(_ context.Context) ([]gateway.SessionStatus, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	result := make([]gateway.SessionStatus, 0, len(h.sessions))
	for id, ms := range h.sessions {
		result = append(result, gateway.SessionStatus{
			SessionID: id,
			State:     ms.state,
			StartedAt: ms.startedAt,
		})
	}
	return result, nil
}

// ActiveSessionIDs returns the IDs of all active sessions.
// Used by the heartbeat goroutine.
func (h *WorkerHandler) ActiveSessionIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	ids := make([]string, 0, len(h.sessions))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	return ids
}

// ListNPCs returns the NPCs in a running session with their mute state.
func (h *WorkerHandler) ListNPCs(_ context.Context, sessionID string) ([]gateway.NPCStatus, error) {
	h.mu.Lock()
	ms, ok := h.sessions[sessionID]
	h.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("session: %q not found", sessionID)
	}

	orch := ms.runtime.Orchestrator()
	if orch == nil {
		return nil, fmt.Errorf("session: %q has no orchestrator", sessionID)
	}

	agents := orch.ActiveAgents()
	result := make([]gateway.NPCStatus, len(agents))
	for i, a := range agents {
		muted, _ := orch.IsMuted(a.ID())
		result[i] = gateway.NPCStatus{
			ID:    a.ID(),
			Name:  a.Name(),
			Muted: muted,
		}
	}
	return result, nil
}

// MuteNPC mutes a named NPC in a running session.
func (h *WorkerHandler) MuteNPC(_ context.Context, sessionID, npcName string) error {
	orch, err := h.sessionOrch(sessionID)
	if err != nil {
		return err
	}

	a := orch.AgentByName(npcName)
	if a == nil {
		return fmt.Errorf("session: npc %q not found in session %q", npcName, sessionID)
	}
	return orch.MuteAgent(a.ID())
}

// UnmuteNPC unmutes a named NPC in a running session.
func (h *WorkerHandler) UnmuteNPC(_ context.Context, sessionID, npcName string) error {
	orch, err := h.sessionOrch(sessionID)
	if err != nil {
		return err
	}

	a := orch.AgentByName(npcName)
	if a == nil {
		return fmt.Errorf("session: npc %q not found in session %q", npcName, sessionID)
	}
	return orch.UnmuteAgent(a.ID())
}

// MuteAllNPCs mutes all NPCs in a running session and returns the count.
func (h *WorkerHandler) MuteAllNPCs(_ context.Context, sessionID string) (int, error) {
	orch, err := h.sessionOrch(sessionID)
	if err != nil {
		return 0, err
	}
	return orch.MuteAll(), nil
}

// UnmuteAllNPCs unmutes all NPCs in a running session and returns the count.
func (h *WorkerHandler) UnmuteAllNPCs(_ context.Context, sessionID string) (int, error) {
	orch, err := h.sessionOrch(sessionID)
	if err != nil {
		return 0, err
	}
	return orch.UnmuteAll(), nil
}

// SpeakNPC forces a named NPC in a running session to speak pre-written text.
func (h *WorkerHandler) SpeakNPC(ctx context.Context, sessionID, npcName, text string) error {
	orch, err := h.sessionOrch(sessionID)
	if err != nil {
		return err
	}

	a := orch.AgentByName(npcName)
	if a == nil {
		return fmt.Errorf("session: npc %q not found in session %q", npcName, sessionID)
	}
	return a.SpeakText(ctx, text)
}

// sessionOrch returns the orchestrator for a running session.
func (h *WorkerHandler) sessionOrch(sessionID string) (*orchestrator.Orchestrator, error) {
	h.mu.Lock()
	ms, ok := h.sessions[sessionID]
	h.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("session: %q not found", sessionID)
	}

	orch := ms.runtime.Orchestrator()
	if orch == nil {
		return nil, fmt.Errorf("session: %q has no orchestrator", sessionID)
	}
	return orch, nil
}

// StopAll stops all running sessions. Used during graceful shutdown.
func (h *WorkerHandler) StopAll(ctx context.Context) {
	h.mu.Lock()
	sessions := make(map[string]*managedSession, len(h.sessions))
	maps.Copy(sessions, h.sessions)
	h.sessions = make(map[string]*managedSession)
	h.mu.Unlock()

	for id, ms := range sessions {
		ms.cancel()
		if err := ms.runtime.Stop(ctx); err != nil {
			slog.Warn("session: stop error during shutdown", "session_id", id, "err", err)
		}
	}
}
