package voice

import (
	"context"
	"log/slog"
	"sync"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// Manager owns one disgo [bot.Client] and a [Session] per Guild. Construct it
// with [NewManager]; it is safe for concurrent use across many Guilds.
type Manager struct {
	vm     voiceManager
	logger *slog.Logger

	defaults sessionConfig

	mu       sync.Mutex
	sessions map[snowflake.ID]*Session
	// opening holds one mutex per guild, serializing concurrent Opens for the
	// SAME guild end-to-end (close-stale → CreateConn → newSession → store).
	// Without it two racing Opens both pass the existing-session check, both
	// CreateConn for the guild, and the loser's Session is overwritten in the
	// map without ever being Closed — leaking its Inbound consumer forever.
	// Opens for different guilds stay concurrent. Entries are tiny and guilds
	// are few, so the map is never pruned.
	opening map[snowflake.ID]*sync.Mutex
}

// ManagerOption configures a [Manager] in [NewManager].
type ManagerOption func(*managerConfig)

type managerConfig struct {
	logger   *slog.Logger
	dave     bool
	defaults sessionConfig
}

// SessionOption overrides a per-[Session] setting in [Manager.Open], falling
// back to the Manager's defaults.
type SessionOption func(*sessionConfig)

// sessionConfig is the resolved per-Session settings, seeded from Manager
// defaults and overridden by Open's options.
type sessionConfig struct {
	logger        *slog.Logger
	metrics       MetricsRecorder
	selfMute      bool
	selfDeaf      bool
	inboundBuffer int
}

const defaultInboundBuffer = 64

// WithLogger sets the logger for the Manager and as the default for Sessions.
func WithLogger(logger *slog.Logger) ManagerOption {
	return func(c *managerConfig) {
		if logger != nil {
			c.logger = logger
			c.defaults.logger = logger
		}
	}
}

// WithDave declares whether this Manager expects DAVE/MLS end-to-end voice
// encryption (default true). It is an intent gate, not the wiring: the DAVE
// session-create func is installed at client construction via [DaveOption]
// (disgo builds the voice manager before [NewManager] runs). When DAVE is
// expected but this build cannot provide it (see [DaveAvailable]), NewManager
// logs a warning so a misconfigured production Voice Instance is visible rather
// than silently unencrypted. Pass WithDave(false) for local tooling that
// intentionally runs without encryption.
func WithDave(enabled bool) ManagerOption {
	return func(c *managerConfig) { c.dave = enabled }
}

// WithMetrics sets the default [MetricsRecorder] for Sessions.
func WithMetrics(m MetricsRecorder) ManagerOption {
	return func(c *managerConfig) {
		if m != nil {
			c.defaults.metrics = m
		}
	}
}

// WithSelfDeaf sets whether Sessions join self-deafened by default.
func WithSelfDeaf(deaf bool) ManagerOption {
	return func(c *managerConfig) { c.defaults.selfDeaf = deaf }
}

// WithSelfMute sets whether Sessions join self-muted by default.
func WithSelfMute(mute bool) ManagerOption {
	return func(c *managerConfig) { c.defaults.selfMute = mute }
}

// WithInboundBuffer sets the default inbound [Frame] buffer size. Values <= 0
// fall back to the default of 64.
func WithInboundBuffer(n int) ManagerOption {
	return func(c *managerConfig) {
		if n > 0 {
			c.defaults.inboundBuffer = n
		}
	}
}

// SessionWithLogger overrides the logger for a single Session.
func SessionWithLogger(logger *slog.Logger) SessionOption {
	return func(c *sessionConfig) {
		if logger != nil {
			c.logger = logger
		}
	}
}

// SessionWithSelfDeaf overrides self-deafen for a single Session.
func SessionWithSelfDeaf(deaf bool) SessionOption {
	return func(c *sessionConfig) { c.selfDeaf = deaf }
}

// SessionWithSelfMute overrides self-mute for a single Session.
func SessionWithSelfMute(mute bool) SessionOption {
	return func(c *sessionConfig) { c.selfMute = mute }
}

// SessionWithInboundBuffer overrides the inbound buffer for a single Session.
// Values <= 0 are ignored.
func SessionWithInboundBuffer(n int) SessionOption {
	return func(c *sessionConfig) {
		if n > 0 {
			c.inboundBuffer = n
		}
	}
}

// NewManager creates a Manager over client's voice manager. DAVE is wired by the
// caller at client construction (see [DaveOption]); NewManager only borrows the
// already-built voice manager.
func NewManager(client *bot.Client, opts ...ManagerOption) *Manager {
	return newManager(disgoVoiceManager{client.VoiceManager}, opts...)
}

// newManager is the internal constructor over the voiceManager seam, so tests
// supply a fake without a live client.
func newManager(vm voiceManager, opts ...ManagerOption) *Manager {
	cfg := managerConfig{
		logger: discardLogger(),
		dave:   true, // ADR-0006: DAVE is the production default
		defaults: sessionConfig{
			logger:        discardLogger(),
			metrics:       discardMetrics{},
			inboundBuffer: defaultInboundBuffer,
		},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.dave && !DaveAvailable() {
		// The caller expects encryption but this build has no DAVE stack; surface
		// it loudly rather than connect silently unencrypted (close code 4017).
		cfg.logger.Warn("voice: DAVE expected but unavailable in this build; build with -tags dave for production")
	}
	return &Manager{
		vm:       vm,
		logger:   cfg.logger,
		defaults: cfg.defaults,
		sessions: make(map[snowflake.ID]*Session),
		opening:  make(map[snowflake.ID]*sync.Mutex),
	}
}

// guildMu returns the per-guild mutex serializing [Manager.Open] for one guild.
func (m *Manager) guildMu(guild snowflake.ID) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	mu, ok := m.opening[guild]
	if !ok {
		mu = &sync.Mutex{}
		m.opening[guild] = mu
	}
	return mu
}

// Open joins guild's voice channel and returns its Session. If a Session for
// guild already exists it is closed and replaced — one Session per Guild. Open
// blocks until the connection is ready or ctx is cancelled.
func (m *Manager) Open(ctx context.Context, guild, channel snowflake.ID, opts ...SessionOption) (*Session, error) {
	cfg := m.defaults
	for _, opt := range opts {
		opt(&cfg)
	}

	// Serialize the whole open sequence per guild (see Manager.opening). m.mu
	// itself must not be held across Close/CreateConn/newSession — Close blocks
	// on a playback Stop, and newSession blocks on the voice gateway.
	gmu := m.guildMu(guild)
	gmu.Lock()
	defer gmu.Unlock()

	m.mu.Lock()
	existing, replace := m.sessions[guild]
	if replace {
		// Drop the stale Session before opening a fresh connection so we never
		// hold two for one Guild.
		delete(m.sessions, guild)
	}
	m.mu.Unlock()
	if replace {
		_ = existing.Close()
		// Release disgo's conn for the displaced session (mirrors Manager.Close)
		// so CreateConn below hands back a fresh one, not the closed husk.
		m.vm.RemoveConn(guild)
	}

	conn := m.vm.CreateConn(guild)
	sess, err := newSession(ctx, guild, channel, conn, cfg)
	if err != nil {
		m.vm.RemoveConn(guild) // unwind the conn disgo created on our behalf
		return nil, err
	}

	m.mu.Lock()
	m.sessions[guild] = sess
	m.mu.Unlock()
	return sess, nil
}

// Get returns the Session for guild and whether one is open.
func (m *Manager) Get(guild snowflake.ID) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[guild]
	return sess, ok
}

// Close closes every open Session and forgets them. It is safe to call once at
// shutdown; the Manager should not be reused afterwards.
func (m *Manager) Close() error {
	m.mu.Lock()
	sessions := m.sessions
	m.sessions = make(map[snowflake.ID]*Session)
	m.mu.Unlock()

	for guild, sess := range sessions {
		_ = sess.Close()
		m.vm.RemoveConn(guild)
	}
	return nil
}

// disgoVoiceManager adapts disgo's voice.Manager to the voiceManager seam,
// wrapping each created voice.Conn as a voiceConn. It is a pure pass-through;
// the seam exists only so tests need not stand up a real client.
type disgoVoiceManager struct {
	m voice.Manager
}

func (d disgoVoiceManager) CreateConn(guild snowflake.ID) voiceConn {
	return d.m.CreateConn(guild)
}

func (d disgoVoiceManager) RemoveConn(guild snowflake.ID) {
	d.m.RemoveConn(guild)
}

// Compile-time guard that every disgo voice.Conn satisfies our voiceConn
// subset; if disgo's method signatures drift this fails to build, not at runtime.
var _ voiceConn = (voice.Conn)(nil)
