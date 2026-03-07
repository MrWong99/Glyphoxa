package session

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/MrWong99/glyphoxa/internal/agent"
	"github.com/MrWong99/glyphoxa/internal/agent/orchestrator"
	"github.com/MrWong99/glyphoxa/internal/engine"
	"github.com/MrWong99/glyphoxa/pkg/audio"
	"github.com/MrWong99/glyphoxa/pkg/memory"
)

// Runtime owns the voice pipeline lifecycle for a single session.
// It manages agents, engines, the mixer, and transcript recording.
//
// Both --mode=full (via local WorkerClient) and --mode=worker use Runtime
// to run the voice pipeline. The gateway does NOT own a Runtime — it only
// orchestrates sessions.
//
// All exported methods are safe for concurrent use.
type Runtime struct {
	mu sync.Mutex

	sessionID string
	agents    []agent.NPCAgent
	engines   []engine.VoiceEngine
	orch      *orchestrator.Orchestrator
	mixer     audio.Mixer
	conn      audio.Connection

	// closers are called in reverse order during Stop.
	closers []func() error

	// recorderWG tracks in-flight transcript recorder goroutines.
	recorderWG sync.WaitGroup

	// cancel stops all background goroutines.
	cancel context.CancelFunc

	running bool
}

// RuntimeConfig holds the dependencies for creating a Runtime.
type RuntimeConfig struct {
	SessionID    string
	Agents       []agent.NPCAgent
	Engines      []engine.VoiceEngine
	Orchestrator *orchestrator.Orchestrator
	Mixer        audio.Mixer
	Connection   audio.Connection
	SessionStore memory.SessionStore
}

// NewRuntime creates a Runtime. Call Start to begin processing, Stop to tear down.
func NewRuntime(cfg RuntimeConfig) *Runtime {
	return &Runtime{
		sessionID: cfg.SessionID,
		agents:    cfg.Agents,
		engines:   cfg.Engines,
		orch:      cfg.Orchestrator,
		mixer:     cfg.Mixer,
		conn:      cfg.Connection,
	}
}

// AddCloser registers a cleanup function called during Stop (in reverse order).
func (rt *Runtime) AddCloser(fn func() error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.closers = append(rt.closers, fn)
}

// Start begins transcript recording for all agents. The provided context
// controls the lifetime of background goroutines.
func (rt *Runtime) Start(ctx context.Context, sessionStore memory.SessionStore) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if rt.running {
		return fmt.Errorf("session: runtime already running for %s", rt.sessionID)
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	rt.cancel = cancel
	rt.running = true

	// Start transcript recorders for each NPC agent.
	if sessionStore != nil {
		for _, ag := range rt.agents {
			rt.recorderWG.Go(func() {
				recordTranscripts(sessionCtx, sessionStore, ag, rt.sessionID)
			})
		}
	}

	slog.Info("session runtime started",
		"session_id", rt.sessionID,
		"agents", len(rt.agents),
	)

	return nil
}

// Stop tears down the runtime: cancels background goroutines, runs closers
// in reverse order, disconnects audio.
func (rt *Runtime) Stop(ctx context.Context) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if !rt.running {
		return fmt.Errorf("session: runtime not running for %s", rt.sessionID)
	}

	// Cancel background goroutines.
	if rt.cancel != nil {
		rt.cancel()
	}

	// Run closers (engines, pipeline, mixer) in reverse order.
	for i := len(rt.closers) - 1; i >= 0; i-- {
		if err := rt.closers[i](); err != nil {
			slog.Warn("session: runtime closer error",
				"session_id", rt.sessionID, "index", i, "err", err)
		}
	}

	// Wait for transcript recorders to finish draining.
	rt.recorderWG.Wait()

	// Disconnect audio.
	if rt.conn != nil {
		if err := rt.conn.Disconnect(); err != nil {
			slog.Warn("session: runtime disconnect error",
				"session_id", rt.sessionID, "err", err)
		}
	}

	rt.running = false
	slog.Info("session runtime stopped", "session_id", rt.sessionID)
	return nil
}

// SessionID returns the session identifier.
func (rt *Runtime) SessionID() string {
	return rt.sessionID
}

// Orchestrator returns the session's orchestrator.
func (rt *Runtime) Orchestrator() *orchestrator.Orchestrator {
	return rt.orch
}

// Agents returns the loaded NPC agents.
func (rt *Runtime) Agents() []agent.NPCAgent {
	return rt.agents
}

// recordTranscripts drains an agent's transcript channel and writes entries
// to the session store. On context cancellation it drains remaining buffered
// entries before returning.
func recordTranscripts(ctx context.Context, store memory.SessionStore, ag agent.NPCAgent, sessionID string) {
	ch := ag.Engine().Transcripts()
	for {
		select {
		case <-ctx.Done():
			for entry := range ch {
				if err := store.WriteEntry(context.Background(), sessionID, entry); err != nil {
					slog.Warn("session: failed to record transcript on drain",
						"npc", ag.Name(), "err", err)
				}
			}
			return
		case entry, ok := <-ch:
			if !ok {
				return
			}
			if err := store.WriteEntry(ctx, sessionID, entry); err != nil {
				slog.Warn("session: failed to record transcript",
					"npc", ag.Name(), "err", err)
			}
		}
	}
}
