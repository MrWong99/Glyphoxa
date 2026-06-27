// Package session holds the in-process SessionManager that drives the live voice
// loop from the web tier (ADR-0039): the Session screen's Start/Stop call into a
// Manager that launches the wirenpc loop, holds its cancel func, and records the
// run in the voice_sessions table. A single-active-session guard prevents
// overlap. There is no loopback RPC or multi-replica backplane — `all` mode runs
// the loop in the same process (ADR-0005/0039); the deferred voice.v1 control
// service is explicitly NOT built here.
package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// endTimeout bounds the ended_at write after the loop exits, so a Stop / shutdown
// can't hang on a slow DB while the run context is already cancelled.
const endTimeout = 5 * time.Second

// Sentinel errors the RPC layer maps onto Connect status codes.
var (
	// ErrSessionActive is returned by Start when a session is already running —
	// the single-active-session guard (AC2). Mapped to CodeAlreadyExists.
	ErrSessionActive = errors.New("session: a voice session is already active")
	// ErrNoActiveSession is returned by Stop when nothing is running. Mapped to
	// CodeFailedPrecondition.
	ErrNoActiveSession = errors.New("session: no active voice session")
	// ErrDiscordNotConfigured is returned by Start when the saved deployment
	// config has no guild/voice channel (#72). Mapped to CodeFailedPrecondition.
	ErrDiscordNotConfigured = errors.New("session: Discord guild/channel not configured")
	// ErrVoiceUnavailable is returned by Start when this process does not drive
	// voice (web-only mode, ADR-0039). Mapped to CodeFailedPrecondition.
	ErrVoiceUnavailable = errors.New("session: voice is not available in this mode")
	// ErrDiscordTokenMissing is returned by Start when neither a saved deployment
	// Bot token nor a DISCORD_BOT_TOKEN env token is available (#87). Mapped to
	// CodeFailedPrecondition, mirroring ErrDiscordNotConfigured.
	ErrDiscordTokenMissing = errors.New("session: no Discord bot token configured")
	// ErrDiscordTokenUndecryptable is returned by Start when a real saved Bot token
	// cannot be decrypted — booted without $GLYPHOXA_SECRET (nil cipher) or a
	// ciphertext the cipher won't open (#87). The underlying actionable detail is
	// wrapped in the chain. Mapped to CodeFailedPrecondition (a self-host
	// misconfig), not an opaque Internal.
	ErrDiscordTokenUndecryptable = errors.New("session: saved Discord bot token could not be decrypted")
)

// Store is the narrow storage surface the Manager needs: the saved Discord
// guild/channel (deployment config) and the voice_sessions lifecycle writes.
// *storage.Store satisfies it; tests use a fake.
type Store interface {
	GetDeploymentConfig(ctx context.Context, tenantID uuid.UUID) (storage.DeploymentConfig, error)
	CreateVoiceSession(ctx context.Context, campaignID uuid.UUID) (storage.VoiceSession, error)
	EndVoiceSession(ctx context.Context, id uuid.UUID, lineCount int) (storage.VoiceSession, error)
}

// TranscriptFinalizer drains the live transcript's writer queue for a session and
// returns the authoritative persisted line_count (#74, ADR-0040). The Manager
// calls it on Stop / loop exit BEFORE EndVoiceSession so the recorded count
// matches the persisted rows. *transcript.Relay satisfies it; defined here (not
// imported) so the manager does NOT depend on the relay — the relay already
// depends on the manager via Sessions, and the reverse import would cycle. nil
// (not wired / persistence off) leaves line_count at the in-memory default.
type TranscriptFinalizer interface {
	Finalize(ctx context.Context, id uuid.UUID) (int, error)
}

// LoopRunner runs the live voice loop until ctx is cancelled. Production wraps
// wirenpc.RunFromDB (which loads the campaign roster and resolves the
// credential-bridge keys, #69) bound to the app pool + cipher; tests inject a
// fake so no Discord/Postgres is touched. The Manager builds the cfg, sourcing
// guild/channel from the saved deployment config (#72).
type LoopRunner func(ctx context.Context, cfg wirenpc.Config) error

// activeSession is the Manager's record of the one running session: the cancel
// func that unwinds its loop, the voice_sessions row, and a done channel closed
// once the loop has exited and the ended_at write has landed.
type activeSession struct {
	campaignID uuid.UUID
	session    storage.VoiceSession
	cancel     context.CancelFunc
	done       chan struct{}
}

// Manager owns at most one live voice session at a time (the single-active
// guard). It is safe for concurrent use.
type Manager struct {
	store      Store
	run        LoopRunner
	base       wirenpc.Config      // Token (env fallback)/Logger/Metrics template; Guild/Channel come from saved config
	cipher     *crypto.Cipher      // decrypts the saved deployment Bot token (#87); nil without $GLYPHOXA_SECRET
	transcript TranscriptFinalizer // finalizes persisted lines on Stop (#74); nil keeps line_count at 0
	log        *slog.Logger
	enabled    bool // false in web-only mode: Start is rejected (ADR-0039)

	mu     sync.Mutex
	active *activeSession
}

// NewManager wraps the store, loop runner and base config in a Manager. base
// carries the env-fallback Discord token, logger and metrics recorders; Start
// overlays the saved guild/channel onto a copy and resolves the Bot token (the
// saved deployment token decrypted via cipher, else the base env token — #87).
// cipher may be nil (boot without $GLYPHOXA_SECRET): the env-fallback path still
// works, but a real saved token then fails Start with a clear precondition
// (ErrDiscordTokenUndecryptable). enabled is false in
// web-only mode, where the process does not drive voice (Start fails
// ErrVoiceUnavailable).
func NewManager(store Store, run LoopRunner, base wirenpc.Config, cipher *crypto.Cipher, log *slog.Logger, enabled bool) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{store: store, run: run, base: base, cipher: cipher, log: log, enabled: enabled}
}

// SetTranscript wires the transcript finalizer the Manager calls on Stop / loop
// exit (#74). It is set once at boot — after both the Manager and the relay are
// built (the relay needs the Manager via Sessions, so the Manager is built first
// and the finalizer back-wired) — before any session can start, so no lock is
// needed.
func (m *Manager) SetTranscript(t TranscriptFinalizer) {
	m.transcript = t
}

// Start launches the live voice loop for a campaign and records a running
// voice_sessions row. It sources the Discord guild/channel from the saved
// deployment config (#72), rejects a second concurrent start (ErrSessionActive),
// and returns the created row. The loop runs on a background context the Manager
// cancels on Stop / Shutdown — it deliberately outlives the request ctx.
func (m *Manager) Start(ctx context.Context, tenantID, campaignID uuid.UUID) (storage.VoiceSession, error) {
	if !m.enabled {
		return storage.VoiceSession{}, ErrVoiceUnavailable
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != nil {
		return storage.VoiceSession{}, ErrSessionActive
	}

	dep, err := m.store.GetDeploymentConfig(ctx, tenantID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return storage.VoiceSession{}, fmt.Errorf("session: load deployment config: %w", err)
	}
	if dep.GuildID == "" || dep.VoiceChannelID == "" {
		return storage.VoiceSession{}, ErrDiscordNotConfigured
	}

	// Resolve the Bot token under the hybrid policy (#87): a real saved deployment
	// token (decrypted via cipher) overrides ENV, else the base env token. Resolve
	// before writing the row so a missing/undecryptable token leaves no stuck row,
	// mirroring the guild/channel precondition above.
	token, err := wirenpc.ResolveDiscordToken(m.cipher, dep.DiscordBotTokenLast4, dep.DiscordBotTokenCiphertext, m.base.Token)
	if err != nil {
		// A real saved token that won't decrypt: surface ErrDiscordTokenUndecryptable
		// (errors.Is at the RPC layer) while keeping the actionable detail in the chain.
		return storage.VoiceSession{}, fmt.Errorf("%w: %w", ErrDiscordTokenUndecryptable, err)
	}
	if token == "" {
		return storage.VoiceSession{}, ErrDiscordTokenMissing
	}

	vs, err := m.store.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		return storage.VoiceSession{}, fmt.Errorf("session: create voice session: %w", err)
	}

	cfg := m.base
	cfg.Token = token
	cfg.Guild = dep.GuildID
	cfg.Channel = dep.VoiceChannelID

	// A background context so the loop survives the HTTP request that started it;
	// the Manager holds cancel and reaps it on Stop / Shutdown.
	runCtx, cancel := context.WithCancel(context.Background())
	as := &activeSession{
		campaignID: campaignID,
		session:    vs,
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	m.active = as
	go m.runLoop(runCtx, as, cfg)
	return vs, nil
}

// runLoop runs the voice loop to completion (cancel or self-exit), then writes
// the ended_at/status row and clears the active slot. close(done) runs last, so
// a Stop waiting on done observes both the updated session and the freed guard.
func (m *Manager) runLoop(ctx context.Context, as *activeSession, cfg wirenpc.Config) {
	defer close(as.done)

	if err := m.run(ctx, cfg); err != nil && ctx.Err() == nil {
		// A real loop error (not the expected cancellation) — log it; the session
		// still ends cleanly below so the row never stays stuck 'running'.
		m.log.Error("voice session loop exited with error", "err", err, "voice_session", as.session.ID)
	}

	// The run ctx is cancelled on a Stop, so end the row on a detached, bounded
	// context — otherwise the ended_at write would itself be cancelled.
	endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), endTimeout)
	defer cancel()

	// Drain the live transcript's writer queue and read the authoritative count
	// BEFORE ending the row, so line_count matches the persisted rows (#74). A
	// finalize failure logs and falls back to the in-memory count rather than
	// blocking the session from ending.
	lineCount := as.session.LineCount
	if m.transcript != nil {
		if n, ferr := m.transcript.Finalize(endCtx, as.session.ID); ferr != nil {
			m.log.Error("finalize transcript before end", "err", ferr, "voice_session", as.session.ID)
		} else {
			lineCount = n
		}
	}

	ended, err := m.store.EndVoiceSession(endCtx, as.session.ID, lineCount)

	m.mu.Lock()
	if err != nil {
		m.log.Error("end voice session", "err", err, "voice_session", as.session.ID)
	} else {
		as.session = ended
	}
	if m.active == as {
		m.active = nil
	}
	m.mu.Unlock()
}

// Stop cancels the active session's loop and waits for it to end, returning the
// ended voice_sessions row. ErrNoActiveSession when nothing is running. If ctx is
// cancelled while waiting, it returns ctx.Err() — the loop still unwinds in the
// background.
func (m *Manager) Stop(ctx context.Context) (storage.VoiceSession, error) {
	m.mu.Lock()
	as := m.active
	m.mu.Unlock()
	if as == nil {
		return storage.VoiceSession{}, ErrNoActiveSession
	}

	as.cancel()
	select {
	case <-as.done:
		// done closes after runLoop set as.session to the ended row; safe to read.
		return as.session, nil
	case <-ctx.Done():
		return storage.VoiceSession{}, ctx.Err()
	}
}

// Snapshot returns the active session and true, or the zero value and false when
// idle — the in-process read backing GetSession (the screen's live status).
func (m *Manager) Snapshot() (storage.VoiceSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		return storage.VoiceSession{}, false
	}
	return m.active.session, true
}

// Shutdown cancels any active session and waits for its loop to end and the
// ended_at write to land. The web tier calls it on process shutdown, before the
// DB pool closes, so a SIGTERM never leaves a row stuck 'running'.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	as := m.active
	m.mu.Unlock()
	if as == nil {
		return
	}
	as.cancel()
	<-as.done
}
