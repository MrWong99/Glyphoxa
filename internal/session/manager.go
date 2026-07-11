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
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/highlight"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
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
	// ErrManagerClosed is returned by Start after Shutdown: the Manager is in its
	// terminal closed state and refuses new work (#157) — no store write, no loop.
	// Mapped to CodeUnavailable.
	ErrManagerClosed = errors.New("session: the session manager is shut down")
	// ErrDiscordTokenUndecryptable is returned by Start when a real saved Bot token
	// cannot be decrypted — booted without $GLYPHOXA_SECRET (nil cipher) or a
	// ciphertext the cipher won't open (#87). The underlying actionable detail is
	// wrapped in the chain. Mapped to CodeFailedPrecondition (a self-host
	// misconfig), not an opaque Internal.
	ErrDiscordTokenUndecryptable = errors.New("session: saved Discord bot token could not be decrypted")
	// ErrAgentNotInCampaign is returned by SetAgentMute when the target agent_id is
	// not a VOICED Agent of the active session's Campaign (#211) — a foreign agent,
	// an unknown id, or the Address-Only Butler, which is never voiced and so cannot
	// be muted (ADR-0009/ADR-0024). Validated atomically against the SAME session the
	// mute writes to, so a session swap can't sneak a foreign agent into the new
	// session's mute set. Mapped to CodeNotFound.
	ErrAgentNotInCampaign = errors.New("session: no such Agent in the Active Campaign")
)

// Store is the narrow storage surface the Manager needs: the saved Discord
// guild/channel (deployment config), the voice_sessions lifecycle writes, and
// the boot-time orphan reconciliation (#143). *storage.Store satisfies it;
// tests use a fake.
type Store interface {
	GetDeploymentConfig(ctx context.Context, tenantID uuid.UUID) (storage.DeploymentConfig, error)
	CreateVoiceSession(ctx context.Context, campaignID uuid.UUID) (storage.VoiceSession, error)
	// CloseVoiceSession writes the terminal row: 'ended' (NULL reason) for a clean
	// stop, or 'failed' + the readable reason for a fatal gateway rejection (#123).
	// One write preserves the #143 end-write atomicity.
	CloseVoiceSession(ctx context.Context, id uuid.UUID, status storage.VoiceSessionStatus, lineCount int, endReason *string) (storage.VoiceSession, error)
	ReconcileOrphanedVoiceSessions(ctx context.Context) (int64, error)
	// ListAgents returns the Active Campaign's full roster (Butler + Character
	// NPCs). The mute subsystem (#211) narrows it to the voiced Character NPCs via
	// voicedAgents — the Butler is voiced now (ADR-0009 #299 amendment) but stays
	// Address-Only and is not a mute target (mute is matcher-owned and
	// Character-only).
	ListAgents(ctx context.Context, campaignID uuid.UUID) ([]storage.Agent, error)
	// GetTenantSpendCaps returns the Tenant's soft/hard spend caps (#130, ADR-0046),
	// snapshot at Start to build the session's spend meter. ErrNotFound (no tenant
	// row) is treated as no caps configured — today's behavior.
	GetTenantSpendCaps(ctx context.Context, tenantID uuid.UUID) (storage.SpendCaps, error)
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

// Highlighter is the Session Highlights persistence pipeline (#308, Epic 8): the
// Manager Begins it at Start (binding the session's owning ids), the live detector
// feeds it Triggers through cfg.Highlights, and the Manager Finalizes it at loop
// exit (scheduling the 7-day candidate purge) — beside transcript.Finalize, at
// EVERY exit path. *highlight.Saver satisfies it. It embeds highlight.Sink so the
// SAME value wires onto the base voice config's cfg.Highlights. Defined here (not
// imported as a concrete type) to keep the seam narrow; nil (highlights off, or a
// web-only Manager that drives no voice) makes Begin/Finalize no-ops.
type Highlighter interface {
	highlight.Sink
	Begin(voiceSessionID, campaignID, tenantID uuid.UUID)
	Finalize(ctx context.Context) error
}

// ChunkFinalizer closes a session's open Transcript Chunk on Stop / loop exit
// (#104, ADR-0011): a lone trailing utterance is flushed ONLY here, at session
// end. *transcript.Chunker satisfies it; defined here (not imported) so the
// manager does NOT depend on the transcript package — the chunker already depends
// on the manager via Sessions, and the reverse import would cycle (mirrors
// TranscriptFinalizer). nil (no chunking / voice-standalone) is a no-op.
type ChunkFinalizer interface {
	FlushSession(ctx context.Context, sessionID uuid.UUID) error
}

// LoopRunner runs the live voice loop until ctx is cancelled. Production wraps
// wirenpc.RunFromDB (which loads the campaign roster and resolves the
// credential-bridge keys, #69) bound to the app pool + cipher; tests inject a
// fake so no Discord/Postgres is touched. The Manager builds the cfg, sourcing
// guild/channel from the saved deployment config (#72).
type LoopRunner func(ctx context.Context, cfg wirenpc.Config) error

// activeSession is the Manager's record of the one running session: the cancel
// func that unwinds its loop, the voice_sessions row, and a done channel closed
// once the loop has exited and the ended_at write has landed (or failed —
// endErr carries that failure so Stop reports it instead of a stale success,
// #143 Defect A). session/endErr are written before close(done) and read only
// after <-done, so done is the synchronization point.
type activeSession struct {
	campaignID uuid.UUID
	session    storage.VoiceSession
	endErr     error
	cancel     context.CancelFunc
	done       chan struct{}
	// muted is the volatile, session-local per-Agent mute set (#211), keyed by
	// AgentID. Fresh-empty per Start, so every new Voice Session begins with all
	// Agents unmuted (AC5) — there is NO DB column and NO migration; the set dies
	// with the session. Guarded by Manager.mu.
	muted map[string]struct{}

	// meter is the session's spend meter (#130, ADR-0046), non-nil only when the
	// Tenant configured at least one cap at Start. It is the cfg.Gate and rides the
	// teed StageMetrics, and backs Manager.Spend(). Dies with the session.
	meter *spend.Meter
	// endReasonOverride records a deliberate policy end_reason for a session that
	// ended cleanly (status 'ended') rather than by a fault — set by the hard-cap
	// trip before it cancels the run ctx (#130). runLoop reads it under Manager.mu
	// and stamps it onto the clean-close write. Empty for an ordinary stop.
	endReasonOverride string
}

// spendHardReason is the end_reason a hard-cap-ended session records: an 'ended'
// row (a deliberate policy stop, ADR-0046 — NOT 'failed', which is reserved for
// faults) carrying WHY it stopped. Prefix 'spend_cap_hard' mirrors the classified
// prefixes #123 uses so the Session screen renders a readable cause.
const spendHardReason = "spend_cap_hard: estimated spend crossed the hard cap"

// Manager owns at most one live voice session at a time (the single-active
// guard). It is safe for concurrent use.
type Manager struct {
	store      Store
	run        LoopRunner
	base       wirenpc.Config      // Token (env fallback)/Logger/Metrics template; Guild/Channel come from saved config
	cipher     *crypto.Cipher      // decrypts the saved deployment Bot token (#87); nil without $GLYPHOXA_SECRET
	transcript TranscriptFinalizer // finalizes persisted lines on Stop (#74); nil keeps line_count at 0
	chunker    ChunkFinalizer      // closes the open Transcript Chunk on Stop (#104); nil = no chunking
	highlights Highlighter         // persists Session Highlights + schedules purge on Stop (#308); nil = highlights off
	log        *slog.Logger
	enabled    bool          // false in web-only mode: Start is rejected (ADR-0039)
	endTimeout time.Duration // per-step end budget (Finalize, end-write); endTimeout in prod, shrunk in tests

	mu     sync.Mutex
	active *activeSession
	closed bool // terminal: set by Shutdown; Start refuses with ErrManagerClosed (#157)

	// pubMu serializes the whole write-set→publish-MuteChanged sequence across the
	// mute ops (#211), so two overlapping ops (a per-Agent toggle racing a mute-all)
	// can never publish events in the reverse order of their set writes — which
	// would let a stale event de-sync the wirenpc matcher from the authoritative
	// set. Ordered AFTER m.mu: an op takes pubMu, then m.mu for the set write, drops
	// m.mu, publishes while still holding pubMu, then drops pubMu. Muted() (the
	// re-read subscribers do during that publish) takes only m.mu, which is free —
	// no inversion.
	pubMu sync.Mutex
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
	m := &Manager{store: store, run: run, base: base, cipher: cipher, log: log, enabled: enabled, endTimeout: endTimeout}
	// The Manager IS the live mute view (#211): it owns the session-local mute set,
	// so wire it as the base voice config's MuteView. Every session Start copies
	// base, so each session's Conversation reads this Manager's set.
	m.base.Mutes = m
	return m
}

// SetTranscript wires the transcript finalizer the Manager calls on Stop / loop
// exit (#74). It is set once at boot — after both the Manager and the relay are
// built (the relay needs the Manager via Sessions, so the Manager is built first
// and the finalizer back-wired) — before any session can start, so no lock is
// needed.
func (m *Manager) SetTranscript(t TranscriptFinalizer) {
	m.transcript = t
}

// SetChunkFlusher wires the chunk finalizer the Manager calls on Stop / loop exit
// (#104). Like SetTranscript it is set once at boot, after the chunker is built
// (the chunker needs the Manager via Sessions), before any session can start, so
// no lock is needed. nil leaves chunking off (voice-standalone, same posture as
// line persistence).
func (m *Manager) SetChunkFlusher(f ChunkFinalizer) {
	m.chunker = f
}

// SetMemory wires the NPC memory recaller onto the base voice config every
// manager-started session copies (#122): it flows through Start → RunFromDB →
// connectAndServe → buildConversation into each NPC's Agent loop. Like
// SetTranscript / SetChunkFlusher it is set once at boot — the recaller needs the
// Manager as its Sessions source (the active Campaign), so the Manager is built
// first and the recaller back-wired — before any session can start, so no lock is
// needed. nil (an unavailable embeddings/DB path) leaves recall off (AC6).
func (m *Manager) SetMemory(r agent.MemoryRecaller) {
	m.base.Memory = r
}

// SetFacts wires the NPC KG-facts recaller onto the base voice config every
// manager-started session copies (#126): it flows through Start → RunFromDB →
// connectAndServe → buildConversation into each NPC's Agent loop, filling the
// reserved Hot Context KG-facts slot. Like SetMemory it is set once at boot — the
// recaller needs the Manager as its Sessions source (the active Campaign), so the
// Manager is built first and the recaller back-wired — before any session can
// start, so no lock is needed. nil leaves facts off (the prompt stays byte-identical).
func (m *Manager) SetFacts(f agent.FactsRecaller) {
	m.base.Facts = f
}

// SetToolDeps wires the built-in knowledge Tools' read sources onto the base
// voice config every manager-started session copies (#296, S1): the adapter
// backing transcript_search and kg_query. Like SetFacts it flows through Start →
// RunFromDB → connectAndServe → buildConversation → tool.BuiltinRegistry, so a
// live NPC granted a knowledge Tool actually reaches the DB. The adapter needs
// the Manager as its active-session source, so the Manager is built first and the
// deps back-wired before any session starts — no lock needed. The zero value
// leaves the Tools registered but unavailable at Execute (the prompt/loop still
// behave, the model just gets an "unavailable" tool result).
func (m *Manager) SetToolDeps(d tool.Deps) {
	m.base.ToolDeps = d
}

// SetHighlights wires the Session Highlights persistence pipeline (#308): the
// Manager Begins/Finalizes it per session, and the SAME value flows onto the base
// voice config as cfg.Highlights (the detector's Sink) every session copies. Like
// SetTranscript it is set once at boot — the Saver needs the store + blob backend,
// built alongside the Manager — before any session can start, so no lock is
// needed. nil leaves highlights off (the detector, if any, gets a nil Sink).
func (m *Manager) SetHighlights(h Highlighter) {
	m.highlights = h
	m.base.Highlights = h
}

// ReconcileOrphans closes voice_sessions rows still marked 'running' that no
// live loop owns (#143). Called once at boot, before any session can start: at
// that point NO loop is live, so every 'running' row is an orphan — stranded by
// a crash (kill -9 / OOM) or a failed end-write — and is marked ended with the
// distinguishing storage.VoiceSessionReasonOrphaned. A web-only Manager
// (enabled=false) never owns rows and skips: another process may be driving
// voice against the same DB.
func (m *Manager) ReconcileOrphans(ctx context.Context) error {
	if !m.enabled {
		return nil
	}
	n, err := m.store.ReconcileOrphanedVoiceSessions(ctx)
	if err != nil {
		return fmt.Errorf("session: reconcile orphaned voice sessions: %w", err)
	}
	if n > 0 {
		m.log.Warn("closed orphaned voice sessions left 'running' by a previous run", "count", n)
	}
	return nil
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
	// The closed check lives under the same lock Shutdown takes, so a Start that
	// wins the lock after Shutdown returned can never insert a row or spawn a
	// loop nothing will cancel (#157).
	if m.closed {
		return storage.VoiceSession{}, ErrManagerClosed
	}
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

	// Snapshot the Tenant's spend caps for this session (#130, ADR-0046): a
	// mid-session edit applies to the NEXT session, never this one. A missing tenant
	// row (ErrNotFound) is no caps; any other error fails Start before a row is left
	// stuck, mirroring the deployment-config precondition above.
	storeCaps, err := m.store.GetTenantSpendCaps(ctx, tenantID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return storage.VoiceSession{}, fmt.Errorf("session: load spend caps: %w", err)
	}

	cfg := m.base
	cfg.Token = token
	cfg.Guild = dep.GuildID
	cfg.Channel = dep.VoiceChannelID
	// Carry the selected campaign into the loop (#323): the same id stamped onto the
	// voice_sessions row (CreateVoiceSession above) drives RunFromDB's campaign-scoped
	// roster/language load, so the voiced roster can never diverge from the bound
	// Active Campaign.
	cfg.CampaignID = campaignID

	// A background context so the loop survives the HTTP request that started it;
	// the Manager holds cancel and reaps it on Stop / Shutdown.
	runCtx, cancel := context.WithCancel(context.Background())
	as := &activeSession{
		campaignID: campaignID,
		session:    vs,
		cancel:     cancel,
		done:       make(chan struct{}),
		muted:      map[string]struct{}{}, // fresh: every new session starts all-unmuted (AC5)
	}

	// Spend meter (#130, ADR-0046): only when the Tenant configured at least one
	// cap. It rides the EXISTING recorder config copy — the tee wraps cfg's
	// StageMetrics, so the meter reads the same usage calls #127 records with ZERO
	// new pipeline plumbing — and is the turn gate. No caps ⇒ nil meter, no tee, no
	// gate: byte-for-byte today's behavior.
	if caps := (spend.Caps{SoftUSD: storeCaps.SoftUSD, HardUSD: storeCaps.HardUSD}); caps.SoftUSD != nil || caps.HardUSD != nil {
		meter := spend.NewMeter(caps, m.log, m.softCapTrip(as), m.hardCapTrip(as))
		base := cfg.StageMetrics
		if base == nil {
			base = observe.Discard{}
		}
		cfg.StageMetrics = observe.TeeUsage(base, meter)
		cfg.Gate = meter
		as.meter = meter
	}

	// Bind the Highlights pipeline to this session before the loop (and its
	// detector) can fire a Trigger (#308): Begin records the owning ids the Saver
	// stamps onto every candidate row + its blob key. Finalize (runLoop) unbinds and
	// schedules the purge. A nil Highlighter (highlights off / web-only) is skipped.
	if m.highlights != nil {
		m.highlights.Begin(vs.ID, campaignID, tenantID)
	}

	m.active = as
	go m.runLoop(runCtx, as, cfg)
	return vs, nil
}

// softCapTrip returns the meter's onSoft callback for session as: publish
// SpendCapReached{soft} on the shared bus (#130). The soft cap refuses NEW turns at
// the replier gate (the meter's AllowTurn already reports false by the time this
// fires); this callback only surfaces the state to the Session screen. It runs on
// the pipeline goroutine that recorded the crossing usage, never under Manager.mu,
// so publishing (the relay takes its own lock) can't deadlock.
func (m *Manager) softCapTrip(as *activeSession) func() {
	return func() {
		if m.base.Bus != nil {
			m.base.Bus.Publish(voiceevent.SpendCapReached{At: time.Now(), Level: voiceevent.SpendCapSoft})
		}
	}
}

// hardCapTrip returns the meter's onHard callback for session as: end the session
// itself cleanly (#130, ADR-0046). It runs on a FRESH goroutine (the #211
// lock-order pattern): the meter fires it outside its own mutex, but it must take
// Manager.mu to record the deliberate end_reason override, then publish + cancel
// OUTSIDE Manager.mu — the relay's SpendCapReached handler calls Snapshot (Manager.mu),
// so publishing under the lock would deadlock. Guarded by m.active == as so a trip
// arriving after the session already rolled over is a no-op.
func (m *Manager) hardCapTrip(as *activeSession) func() {
	return func() {
		go func() {
			m.mu.Lock()
			if m.active != as {
				m.mu.Unlock()
				return // this session already ended / rolled over
			}
			as.endReasonOverride = spendHardReason
			cancel := as.cancel
			bus := m.base.Bus
			m.mu.Unlock()

			if bus != nil {
				bus.Publish(voiceevent.SpendCapReached{At: time.Now(), Level: voiceevent.SpendCapHard})
			}
			// Cancel the run ctx: runLoop then closes the row via the CLEAN path
			// (ctx.Err() != nil ⇒ not 'failed') and stamps the endReasonOverride —
			// 'ended' + spend_cap_hard reason, a deliberate policy stop.
			cancel()
		}()
	}
}

// Spend returns a snapshot of the active session's spend meter (#130): estimated
// USD, cap state, and configured caps. Idle, or a session with no caps configured,
// reports the zero Status (no state, zero spend) — the feature-off surface.
func (m *Manager) Spend() spend.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil || m.active.meter == nil {
		return spend.Status{}
	}
	return m.active.meter.Status()
}

// runLoop runs the voice loop to completion (cancel or self-exit), then writes
// the ended_at/status row and clears the active slot. close(done) runs last, so
// a Stop waiting on done observes both the updated session and the freed guard.
func (m *Manager) runLoop(ctx context.Context, as *activeSession, cfg wirenpc.Config) {
	defer close(as.done)

	loopErr := m.run(ctx, cfg)
	// A non-nil loop error while ctx was NOT cancelled is a real failure, not the
	// expected Stop/Shutdown cancellation: the session ends 'failed' with a readable
	// reason instead of the clean 'ended' (#123). A fatal gateway rejection carries a
	// classified reason ("invalid_bot_token: …"); any other loop error is recorded as
	// "loop_error: …". Either way a single terminal write lands, so the row is never
	// stuck 'running' (#143).
	failed := loopErr != nil && ctx.Err() == nil
	if failed {
		m.log.Error("voice session loop exited with error", "err", loopErr, "voice_session", as.session.ID)
	}

	// The run ctx is cancelled on a Stop, so both end steps run on detached,
	// bounded contexts — otherwise the writes would themselves be cancelled.
	base := context.WithoutCancel(ctx)

	// Drain the live transcript's writer queue and read the authoritative count
	// BEFORE ending the row, so line_count matches the persisted rows (#74). A
	// finalize failure logs and falls back to the in-memory count rather than
	// blocking the session from ending. Finalize gets its OWN budget: its flush
	// barrier can queue behind slow line UPSERTs and eat the whole deadline
	// (#143 Defect B), and the end-write below must not inherit that.
	lineCount := as.session.LineCount
	if m.transcript != nil {
		finCtx, finCancel := context.WithTimeout(base, m.endTimeout)
		n, ferr := m.transcript.Finalize(finCtx, as.session.ID)
		finCancel()
		if ferr != nil {
			m.log.Error("finalize transcript before end", "err", ferr, "voice_session", as.session.ID)
		} else {
			lineCount = n
		}
	}

	// Finalize the Session Highlights pipeline beside the transcript (#308): drain
	// the Saver's worker (persisting any queued clips) and schedule the 7-day
	// candidate purge, then unbind. Its OWN budget (like Finalize/chunk flush) — a
	// slow drain / enqueue logs and never blocks the row from ending. Runs at EVERY
	// loop exit (Stop, self-exit, Shutdown), so a session's candidates always get a
	// purge horizon (ADR-0051). nil (highlights off) is skipped.
	if m.highlights != nil {
		hlCtx, hlCancel := context.WithTimeout(base, m.endTimeout)
		if err := m.highlights.Finalize(hlCtx); err != nil {
			m.log.Warn("finalize highlights before end", "err", err, "voice_session", as.session.ID)
		}
		hlCancel()
	}

	// Close the session's open Transcript Chunk (#104, ADR-0011): a lone trailing
	// utterance is flushed ONLY here, at session end. Best-effort with its OWN
	// budget (like Finalize) — a flush failure logs and never blocks the row from
	// ending. Distinct from the line grain above (ADR-0040): the two persist
	// independently.
	if m.chunker != nil {
		chunkCtx, chunkCancel := context.WithTimeout(base, m.endTimeout)
		if err := m.chunker.FlushSession(chunkCtx, as.session.ID); err != nil {
			m.log.Warn("flush transcript chunk before end", "err", err, "voice_session", as.session.ID)
		}
		chunkCancel()
	}

	// A hard-cap trip records a deliberate end_reason override before it cancels the
	// run ctx (#130): the cancel makes ctx.Err() non-nil, so the clean path below
	// runs (status 'ended', NOT 'failed' — a policy stop is not a fault, ADR-0046),
	// but the row still records WHY it stopped. Read under Manager.mu (the trip
	// writes it there) so the race detector sees the synchronization.
	m.mu.Lock()
	override := as.endReasonOverride
	m.mu.Unlock()

	// A FRESH deadline for the terminal write, regardless of what Finalize consumed
	// (#143): the row landing terminal is the invariant that matters. One
	// CloseVoiceSession stamps 'failed' + the readable reason, 'ended' + the policy
	// override reason (hard cap), or 'ended' + NULL for a clean stop — the single
	// write preserves #143's end-write atomicity.
	status := storage.VoiceSessionEnded
	var endReason *string
	if failed {
		status = storage.VoiceSessionFailed
		reason := failureReason(loopErr)
		endReason = &reason
	} else if override != "" {
		reason := override
		endReason = &reason
	}
	endCtx, cancel := context.WithTimeout(base, m.endTimeout)
	defer cancel()
	ended, err := m.store.CloseVoiceSession(endCtx, as.session.ID, status, lineCount, endReason)

	m.mu.Lock()
	if err != nil {
		// The row is still 'running' in the DB. Remember the failure so a waiting
		// Stop surfaces it (#143 Defect A); the startup reconciliation repairs the
		// row on the next boot. Still clear the active slot — the loop is gone.
		as.endErr = fmt.Errorf("session: close voice session %s: %w", as.session.ID, err)
		m.log.Error("close voice session", "err", err, "voice_session", as.session.ID)
	} else {
		as.session = ended
	}
	if m.active == as {
		m.active = nil
	}
	m.mu.Unlock()
}

// failureReason renders the persisted end_reason for a failed session (#123): a
// fatal gateway rejection carries its classified "<reason>: <prose>" verbatim (so
// the Session screen shows "invalid_bot_token: …"), while any other loop error is
// tagged "loop_error: …" — distinguishing a classified fatal from an unexpected
// exit in the durable record.
func failureReason(loopErr error) string {
	var fe *wirenpc.FatalError
	if errors.As(loopErr, &fe) {
		return fe.Error()
	}
	return "loop_error: " + loopErr.Error()
}

// Stop cancels the active session's loop and waits for it to end, returning the
// ended voice_sessions row. ErrNoActiveSession when nothing is running. If the
// ended_at write failed, Stop returns the row's true (still-'running') state
// WITH the failure — never a plain success carrying status='running' (#143). If
// ctx is cancelled while waiting, it returns ctx.Err() — the loop still unwinds
// in the background.
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
		// done closes after runLoop set as.session/as.endErr; safe to read.
		return as.session, as.endErr
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

// Shutdown moves the Manager to its terminal closed state (any later Start
// fails ErrManagerClosed, #157), then cancels any active session and waits for
// its loop to end and the ended_at write to land. The web tier calls it on
// process shutdown, before the DB pool closes, so a SIGTERM never leaves a row
// stuck 'running'. Idempotent: a second call finds no active session.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closed = true
	as := m.active
	m.mu.Unlock()
	if as == nil {
		return
	}
	as.cancel()
	<-as.done
}

// Muted reports whether the Agent with agentID is muted in the live session,
// satisfying [orchestrator.MuteView] (#211). It is the authoritative live read the
// voice loop's replier gate consults per route. Idle (no active session) is
// always unmuted — the feature is off between sessions.
func (m *Manager) Muted(agentID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		return false
	}
	_, ok := m.active.muted[agentID]
	return ok
}

// MutedAgentIDs is a sorted snapshot of the currently-muted Agent ids, or nil when
// idle. It backs GetSession's reload truth (AC5): a mid-session page reload reads
// the true current mute state from here.
func (m *Manager) MutedAgentIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		return nil
	}
	return mutedIDsLocked(m.active.muted)
}

// SetAgentMute mutes or unmutes one voiced Agent in the live session, returning
// the resulting sorted muted-id set (#211). It refuses when idle
// (ErrNoActiveSession, AC4) and rejects an agentID that is not a VOICED Agent of
// the active session's Campaign — a foreign agent, an unknown id, or the
// Address-Only Butler (never voiced, ADR-0009/ADR-0024) — with
// ErrAgentNotInCampaign.
//
// Validation and the write are SESSION-ATOMIC (mirrors SetAllMute): the active
// session is captured, its Campaign is listed with m.mu released (the store read
// may block), then the set is written only if the SAME session is still active —
// so a session swap between the roster read and the write can never sneak a
// foreign agent (valid in the old Campaign, not the new) into the new session's
// mute set. The write-then-publish runs under pubMu so it is globally ordered
// against a concurrent SetAllMute (no reverse-order events).
func (m *Manager) SetAgentMute(ctx context.Context, agentID string, muted bool) ([]string, error) {
	m.mu.Lock()
	as := m.active
	if as == nil {
		m.mu.Unlock()
		return nil, ErrNoActiveSession
	}
	campaignID := as.campaignID
	m.mu.Unlock()

	agents, err := m.store.ListAgents(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf("session: list agents for mute: %w", err)
	}
	if !agentInList(voicedAgents(agents), agentID) {
		return nil, ErrAgentNotInCampaign
	}

	m.pubMu.Lock()
	defer m.pubMu.Unlock()

	m.mu.Lock()
	if m.active != as {
		// The session ended or rolled over between the roster read and the write:
		// abort rather than mutate a set that belongs to a different session.
		m.mu.Unlock()
		return nil, ErrNoActiveSession
	}
	changed := applyMuteLocked(m.active.muted, agentID, muted)
	ids := mutedIDsLocked(m.active.muted)
	bus := m.base.Bus
	m.mu.Unlock()

	if changed && bus != nil {
		bus.Publish(voiceevent.MuteChanged{At: time.Now(), AgentID: agentID, Muted: muted})
	}
	return ids, nil
}

// SetAllMute mutes or unmutes every mutable Agent of the Active Campaign (the
// Character NPCs from store.ListAgents, minus the Address-Only Butler — which is
// voiced now (ADR-0009 #299 amendment) but is not a mute target, mute being
// matcher-owned and Character-only), returning the resulting sorted muted-id set
// (#211). It refuses when idle
// (ErrNoActiveSession). The campaign is captured under m.mu, the roster is listed
// with m.mu released (the store read may block), then the set is re-locked and
// applied only if the SAME session is still active — a session that ended (or a
// new one that started) while listing aborts with ErrNoActiveSession. The apply +
// its per-change MuteChanged burst run under pubMu, so the whole mute-all is
// ordered atomically against a concurrent per-Agent toggle.
func (m *Manager) SetAllMute(ctx context.Context, muted bool) ([]string, error) {
	m.mu.Lock()
	as := m.active
	if as == nil {
		m.mu.Unlock()
		return nil, ErrNoActiveSession
	}
	campaignID := as.campaignID
	m.mu.Unlock()

	agents, err := m.store.ListAgents(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf("session: list agents for mute-all: %w", err)
	}

	m.pubMu.Lock()
	defer m.pubMu.Unlock()

	m.mu.Lock()
	if m.active != as {
		// The session ended or rolled over while we listed agents: abort rather than
		// mutate a set that no longer belongs to this session.
		m.mu.Unlock()
		return nil, ErrNoActiveSession
	}
	changes := make([]voiceevent.MuteChanged, 0, len(agents))
	for _, a := range voicedAgents(agents) {
		id := a.ID.String()
		if applyMuteLocked(m.active.muted, id, muted) {
			changes = append(changes, voiceevent.MuteChanged{AgentID: id, Muted: muted})
		}
	}
	ids := mutedIDsLocked(m.active.muted)
	bus := m.base.Bus
	m.mu.Unlock()

	if bus != nil {
		now := time.Now()
		for _, c := range changes {
			c.At = now
			bus.Publish(c)
		}
	}
	return ids, nil
}

// SayAs publishes a GM-puppeteered direct-speech request (#295, ADR-0010): the
// voiced Character NPC with agentID speaks text verbatim in the live Voice Session.
// It refuses when idle (ErrNoActiveSession) and rejects an agentID that is not a
// voiced CHARACTER NPC of the active session's Campaign — a foreign agent, an
// unknown id, or the Butler (voiced now per ADR-0009 #299, but Address-Only and
// excluded from the /say Character roster by voicedAgents, ADR-0010) — with
// ErrAgentNotInCampaign.
//
// Validation and the publish are SESSION-ATOMIC (mirrors SetAgentMute): the active
// session is captured, its Campaign is listed with m.mu released (the store read may
// block), then the event is published only if the SAME session is still active — so
// a session swap between the roster read and the publish can never voice a foreign
// agent into the new session. It publishes [voiceevent.SpeakRequested] carrying the
// agent's Target (id + character role + display name), a fresh TurnID, and the text
// — NOT [voiceevent.AddressRouted], which would trigger the LLM Replier (ADR-0024).
// The GM mute is deliberately NOT consulted here (puppeteering is a GM override, so
// a muted NPC still speaks a /say — the DirectSpeech reactor bypasses the mute gate).
func (m *Manager) SayAs(ctx context.Context, agentID, text string) error {
	m.mu.Lock()
	as := m.active
	if as == nil {
		m.mu.Unlock()
		return ErrNoActiveSession
	}
	campaignID := as.campaignID
	m.mu.Unlock()

	agents, err := m.store.ListAgents(ctx, campaignID)
	if err != nil {
		return fmt.Errorf("session: list agents for say: %w", err)
	}
	var target storage.Agent
	found := false
	for _, a := range voicedAgents(agents) {
		if a.ID.String() == agentID {
			target = a
			found = true
			break
		}
	}
	if !found {
		return ErrAgentNotInCampaign
	}

	m.mu.Lock()
	if m.active != as {
		// The session ended or rolled over between the roster read and the publish:
		// abort rather than voice into a different (or no) session.
		m.mu.Unlock()
		return ErrNoActiveSession
	}
	bus := m.base.Bus
	m.mu.Unlock()

	if bus != nil {
		bus.Publish(voiceevent.SpeakRequested{
			At:     time.Now(),
			TurnID: voiceevent.NewTurnID(),
			Target: voiceevent.AddressTarget{
				AgentID:   agentID,
				AgentRole: voiceevent.AgentRoleCharacter,
				Name:      target.Name,
			},
			Text: text,
		})
	}
	return nil
}

// agentInList reports whether agentID (a UUID string) names an Agent in agents.
func agentInList(agents []storage.Agent, agentID string) bool {
	for _, a := range agents {
		if a.ID.String() == agentID {
			return true
		}
	}
	return false
}

// voicedAgents returns only the Agents the mute subsystem can act on — the
// Character NPCs. The auto-created Butler (agent_role='butler') now enters the
// voiced wirenpc Roster/Matcher/Cast (ADR-0009 #299 amendment), but it stays
// Address-Only and mute is matcher-owned and Character-only: muting the Butler is
// refused, so filtering it here could only ever record a phantom id that silences
// nothing. Filtering it here is the single chokepoint both SetAgentMute (which
// then rejects the Butler with ErrAgentNotInCampaign) and SetAllMute (which then
// skips it) share, so the live mute set is exactly the set of Character Agents —
// and GetSession's reload truth (muted_agent_ids) never lists the Butler.
func voicedAgents(agents []storage.Agent) []storage.Agent {
	out := make([]storage.Agent, 0, len(agents))
	for _, a := range agents {
		if a.Role == storage.AgentRoleButler {
			continue
		}
		out = append(out, a)
	}
	return out
}

// applyMuteLocked sets or clears agentID in the mute set, reporting whether the
// set actually changed (so an idempotent re-mute publishes nothing). Caller holds
// Manager.mu.
func applyMuteLocked(set map[string]struct{}, agentID string, muted bool) bool {
	_, was := set[agentID]
	if muted == was {
		return false
	}
	if muted {
		set[agentID] = struct{}{}
	} else {
		delete(set, agentID)
	}
	return true
}

// mutedIDsLocked returns the muted ids as a sorted slice. Caller holds Manager.mu.
func mutedIDsLocked(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
