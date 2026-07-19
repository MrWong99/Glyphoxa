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

	"github.com/disgoorg/disgo/bot"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/billing"
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
	// ErrAllowanceExhausted is returned by Start when the Tenant's plan carries a
	// monthly usage allowance (ADR-0055 gate (b)) and the month-to-date estimate
	// has already spent it. Mapped to CodeResourceExhausted. A deliberate policy
	// refusal, not a fault — and an estimate, never billing truth (ADR-0046).
	ErrAllowanceExhausted = errors.New("session: the plan's monthly usage allowance is exhausted")
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

	// ErrButlerVoiceless is returned by SpeakAsButler when the Active Campaign's
	// Butler has no synthesizable Voice (empty VoiceID — the default auto-Butler is
	// voiceless). Voicing it would publish a KindButler transcript line that never
	// produces audio (elevenlabs.Synthesize rejects an empty VoiceID), so the caller
	// degrades to text instead of claiming a phantom voicing (ADR-0012, #365).
	ErrButlerVoiceless = errors.New("session: the Butler has no voice to speak with")
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

// UsageWriter persists a session's accumulated Usage Ledger rows at loop exit
// (ADR-0054). It is a SEPARATE optional seam from [Store] — a deployment that
// wires no UsageWriter (voice-standalone, tests) records no durable usage, and
// nothing else changes. [*storage.Store] satisfies it (AddUsage).
type UsageWriter interface {
	AddUsage(ctx context.Context, rows []storage.UsageRow) error
}

// AllowanceChecker snapshots a tenant's monthly plan allowance at Start
// (ADR-0055 gate (b)). [spend.PlanAllowance] satisfies it; nil = no gate.
type AllowanceChecker interface {
	AllowanceState(ctx context.Context, tenantID uuid.UUID) (spend.AllowanceState, error)
}

// TranscriptFinalizer drains the live transcript's writer queue for a session and
// returns the authoritative persisted line_count (#74, ADR-0040). The Manager
// calls it on Stop / loop exit BEFORE EndVoiceSession so the recorded count
// matches the persisted rows. *transcript.Relay satisfies it; defined here (not
// imported) so the manager does NOT depend on the relay — the relay already
// resolves each event's session via its Sessions seam (a [Registry], #487), and
// the reverse import would cycle. nil (not wired / persistence off) leaves
// line_count at the in-memory default.
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
// resolves each event's session via its Sessions seam (a [Registry], #487), and
// the reverse import would cycle (mirrors TranscriptFinalizer). nil (no chunking /
// voice-standalone) is a no-op.
type ChunkFinalizer interface {
	FlushSession(ctx context.Context, sessionID uuid.UUID) error
}

// ClientSource resolves a Tenant's standing Discord client from the per-tenant
// client registry (#489): every manager-started Voice Session borrows the client
// keyed by that Tenant's resolved Bot token, so a session start touches only its
// own Tenant's client (never the global-latest singleton). *presence.Clients
// satisfies it. nil (web-only, or voice standalone) leaves cfg.Client on the base
// config's provider — for the standalone bench path that dials its own client.
type ClientSource interface {
	ClientForTenant(ctx context.Context, tenantID uuid.UUID) (*bot.Client, error)
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
	tenantID   uuid.UUID
	session    storage.VoiceSession
	endErr     error
	cancel     context.CancelFunc
	done       chan struct{}
	// bus is this session's OWN voiceevent.Bus (#487): the session-local reactors
	// (orchestrator, barge, mute/tape wiring, detector, observe StageSubscriber)
	// subscribe here, and every Manager control publish for this session
	// (softCap/hardCap, SayAs, mute, replay) lands here rather than on the process
	// bus. stopForward detaches the [voiceevent.Forward] bridge that republishes
	// this bus onto the process bus stamped with the session id; it is called once
	// at loop exit. Both are nil only for a session built before its bus (never in
	// practice — set in Start before the loop launches).
	bus         *voiceevent.Bus
	stopForward func()
	// ended is set true under Manager.mu the instant the loop returns — BEFORE the
	// finalizers Close the projections — so Lookup/Resolve/PublishToCampaign and
	// hardCapTrip all treat the session as gone during the multi-second end window,
	// closing the #487 resurrection race. Distinct from m.active clearing (which
	// happens only after the terminal DB write).
	ended bool
	// muted is the volatile, session-local per-Agent mute set (#211), keyed by
	// AgentID. Fresh-empty per Start, so every new Voice Session begins with all
	// Agents unmuted (AC5) — there is NO DB column and NO migration; the set dies
	// with the session. Guarded by Manager.mu.
	muted map[string]struct{}

	// meter is the session's spend meter (#130, ADR-0046), non-nil only when the
	// Tenant configured at least one cap at Start. It is the cfg.Gate and rides the
	// teed StageMetrics, and backs Manager.Spend(). Dies with the session.
	meter *spend.Meter
	// ledger is the session's durable Usage Ledger sink (ADR-0054), non-nil only
	// when the Manager was wired a UsageWriter. It rides the teed StageMetrics
	// beside the meter (attribution only, never a gate) and is flushed into the
	// usage_ledger table at loop exit.
	ledger *billing.Ledger
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

// allowanceHardReason is spendHardReason's sibling for a session ended because
// the PLAN's monthly allowance ran out mid-session (ADR-0055 gate (b)) rather
// than a tenant-set cap — a distinct prefix so operators can tell the two
// policy stops apart.
const allowanceHardReason = "allowance_exhausted: estimated spend crossed the plan's monthly allowance"

// Manager owns at most one live voice session at a time (the single-active
// guard). It is safe for concurrent use.
type Manager struct {
	store      Store
	run        LoopRunner
	base       wirenpc.Config      // Token (env fallback)/Logger/Metrics template; Guild/Channel come from saved config. Immutable after NewManager (#448), so lock-free reads (e.g. base.Bus) are safe.
	cipher     *crypto.Cipher      // decrypts the saved deployment Bot token (#87); nil without $GLYPHOXA_SECRET
	transcript TranscriptFinalizer // finalizes persisted lines on Stop (#74); nil keeps line_count at 0
	chunker    ChunkFinalizer      // closes the open Transcript Chunk on Stop (#104); nil = no chunking
	highlights Highlighter         // persists Session Highlights + schedules purge on Stop (#308); nil = highlights off
	usage      UsageWriter         // persists the Usage Ledger at loop exit (ADR-0054); nil = no durable usage
	allowance  AllowanceChecker    // the Start-time plan-allowance gate (ADR-0055 gate (b)); nil = no gate
	clients    ClientSource        // resolves the Tenant's standing Discord client per session (#489); nil = base provider
	log        *slog.Logger
	enabled    bool          // false in web-only mode: Start is rejected (ADR-0039)
	endTimeout time.Duration // per-step end budget (Finalize, end-write); endTimeout in prod, shrunk in tests

	mu     sync.Mutex
	active *activeSession
	closed bool // terminal: set by Shutdown; Start refuses with ErrManagerClosed (#157)

	// pubMu serializes the whole write-set→publish-MuteChanged sequence across the
	// [LiveSession] mute ops (#211), so two overlapping ops (a per-Agent toggle
	// racing a mute-all) can never publish events in the reverse order of their set
	// writes — which would let a stale event de-sync the wirenpc matcher from the
	// authoritative set. Ordered AFTER m.mu: an op takes pubMu, then m.mu for the
	// set write, drops m.mu, publishes while still holding pubMu, then drops pubMu.
	// Muted() (the re-read subscribers do during that publish) takes only m.mu,
	// which is free — no inversion. It lives on the Manager (not the handle) so the
	// ordering is global across ALL handles to the session.
	pubMu sync.Mutex
}

// Deps are the Manager's construction-time collaborator seams (#448): the six
// formerly post-construction setters folded into one struct, so "wired before
// the first Start" is structural — the Manager (and its base voice config) is
// immutable after NewManager returns — instead of comment-enforced call
// ordering in the composition root. Every field is optional; the zero value is
// a Manager with every collaborator off (the voice-standalone / test posture).
//
// Several collaborators themselves need to resolve the session behind a bus event
// (or the single-active read) — the old Manager ↔ projector construction cycle
// that forced the setters. The cycle breaks at its true seam: they depend only on
// a narrow [Sessions] interface, so they are constructed FIRST against a
// [Registry], and each Manager registers itself in it here at construction (#487).
type Deps struct {
	// Transcript is the finalizer that drains the live transcript's writer queue on
	// Stop / loop exit and returns the authoritative persisted line_count (#74,
	// ADR-0040). nil (not wired / persistence off) keeps line_count at the
	// in-memory default.
	Transcript TranscriptFinalizer
	// Chunker closes the session's open Transcript Chunk on Stop / loop exit
	// (#104, ADR-0011). nil = no chunking (voice-standalone, same posture as line
	// persistence).
	Chunker ChunkFinalizer
	// Highlights is the Session Highlights persistence pipeline (#308): the
	// Manager Begins/Finalizes it per session, and the SAME value flows onto the
	// base voice config as cfg.Highlights (the detector's Sink) every session
	// copies. nil = highlights off (the detector, if any, gets a nil Sink).
	Highlights Highlighter
	// Memory is the NPC memory recaller wired onto the base voice config every
	// manager-started session copies (#122): it flows through Start → RunFromDB →
	// connectAndServe → buildConversation into each NPC's Agent loop. nil (an
	// unavailable embeddings/DB path) leaves recall off (AC6).
	Memory agent.MemoryRecaller
	// Facts is the NPC KG-facts recaller wired onto the base voice config (#126),
	// filling the reserved Hot Context KG-facts slot per turn. nil leaves facts
	// off (the prompt stays byte-identical).
	Facts agent.FactsRecaller
	// Tools are the built-in knowledge Tools' backing sources wired onto the base
	// voice config (#296): the adapters behind transcript_search, kg_query, the
	// remember_knowledge proposal writer and the recap Tool, flowing through
	// buildConversation → tool.BuiltinRegistry. The zero value leaves the Tools
	// registered but unavailable at Execute (the prompt/loop still behave, the
	// model just gets an "unavailable" tool result).
	Tools tool.Deps
	// Usage, when non-nil, persists each session's accumulated Usage Ledger rows
	// at loop exit (ADR-0054): per-Tenant durable usage for SaaS cost attribution.
	// [*storage.Store] satisfies it. nil = no durable usage (voice-standalone /
	// test posture), byte-for-byte today's behavior.
	Usage UsageWriter
	// Allowance, when non-nil, is the monthly plan-allowance gate (ADR-0055 gate
	// (b)) consulted at Start: an exhausted allowance refuses the start
	// (ErrAllowanceExhausted) and a remaining one tightens the session's hard
	// cap, riding the ADR-0046 meter wholesale. The composition root wires
	// [spend.PlanAllowance] only in `open` Admission Mode; nil (allowlist /
	// self-host / voice-standalone) is a no-op — byte-for-byte today's behavior.
	Allowance AllowanceChecker
	// Registry, when non-nil, is the process-wide index the new Manager registers
	// itself in (#487, replacing the single-bind View): process-wide bus consumers
	// Resolve this Manager's live session by the SessionID stamped on each event,
	// and control surfaces PublishToCampaign into it. Registration is additive and
	// never panics, so any number of Managers (concurrent Voice Sessions) coexist.
	Registry *Registry
	// Clients, when non-nil, is the per-tenant Discord client registry (#489):
	// every manager-started session borrows the standing client keyed by the
	// session's Tenant's resolved Bot token, so a start touches only that Tenant's
	// client. nil (web-only / voice standalone) leaves cfg.Client on the base
	// config's provider.
	Clients ClientSource
}

// NewManager wraps the store, loop runner, base config and collaborator deps in
// a Manager. base carries the env-fallback Discord token, logger and metrics
// recorders; Start overlays the saved guild/channel onto a copy and resolves
// the Bot token (the saved deployment token decrypted via cipher, else the base
// env token — #87). cipher may be nil (boot without $GLYPHOXA_SECRET): the
// env-fallback path still works, but a real saved token then fails Start with a
// clear precondition (ErrDiscordTokenUndecryptable). enabled is false in
// web-only mode, where the process does not drive voice (Start fails
// ErrVoiceUnavailable).
//
// The Manager and its base config are immutable once NewManager returns (#448):
// every collaborator arrives via deps, so nothing needs a lock to read base and
// no wiring can land after the first Start.
func NewManager(store Store, run LoopRunner, base wirenpc.Config, cipher *crypto.Cipher, log *slog.Logger, enabled bool, deps Deps) *Manager {
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{
		store:      store,
		run:        run,
		base:       base,
		cipher:     cipher,
		transcript: deps.Transcript,
		chunker:    deps.Chunker,
		highlights: deps.Highlights,
		usage:      deps.Usage,
		allowance:  deps.Allowance,
		clients:    deps.Clients,
		log:        log,
		enabled:    enabled,
		endTimeout: endTimeout,
	}
	// The Manager IS the live mute view (#211): it owns the session-local mute set,
	// so wire it as the base voice config's MuteView. Every session Start copies
	// base, so each session's Conversation reads this Manager's set.
	m.base.Mutes = m
	m.base.Memory = deps.Memory
	m.base.Facts = deps.Facts
	m.base.ToolDeps = deps.Tools
	// The SAME Highlighter the Manager Begins/Finalizes per session is the base
	// config's detector Sink (#308), so triggers land in the pipeline the Manager
	// owns the lifecycle of.
	m.base.Highlights = deps.Highlights
	if deps.Registry != nil {
		deps.Registry.register(m)
	}
	return m
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

	// Snapshot the Tenant's spend caps for this session (#130, ADR-0046): a
	// mid-session edit applies to the NEXT session, never this one. A missing tenant
	// row (ErrNotFound) is no caps; any other error fails Start. The read has no
	// dependency on the voice_sessions row, so it happens BEFORE the insert —
	// mirroring the deployment-config precondition above — and a caps-load failure
	// can never strand a 'running' row (#433).
	storeCaps, err := m.store.GetTenantSpendCaps(ctx, tenantID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return storage.VoiceSession{}, fmt.Errorf("session: load spend caps: %w", err)
	}

	// Plan-allowance gate (ADR-0055 gate (b)), snapshot beside the caps: when a
	// checker is wired (`open` Admission Mode) and the plan carries an
	// allowance, an exhausted one refuses the start outright and a remaining
	// one tightens this session's hard cap below. Like the caps read, it runs
	// BEFORE the insert so a failure never strands a 'running' row (#433), and
	// a read failure fails the start CLOSED. The month-to-date figure is the
	// flushed ledger only — the running session's own spend is exactly what the
	// tightened meter bounds. Concurrent-session caveat: each Start snapshots
	// the same remainder (the D0/D6 concurrency epic inherits this, noted).
	var allowanceRemaining *float64
	if m.allowance != nil {
		state, err := m.allowance.AllowanceState(ctx, tenantID)
		if err != nil {
			return storage.VoiceSession{}, fmt.Errorf("session: load plan allowance: %w", err)
		}
		if state.Exhausted() {
			return storage.VoiceSession{}, ErrAllowanceExhausted
		}
		allowanceRemaining = state.RemainingUSD()
	}

	vs, err := m.store.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		return storage.VoiceSession{}, fmt.Errorf("session: create voice session: %w", err)
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

	// Borrow this Tenant's standing Discord client from the per-tenant registry
	// (#489): the loop resolves the client keyed by THIS Tenant's resolved Bot
	// token, so a start never touches another Tenant's client. nil registry
	// (web-only / standalone) leaves cfg.Client on the base provider.
	if m.clients != nil {
		clients := m.clients
		cfg.Client = func(cctx context.Context) (*bot.Client, error) {
			return clients.ClientForTenant(cctx, tenantID)
		}
	}

	// A background context so the loop survives the HTTP request that started it;
	// the Manager holds cancel and reaps it on Stop / Shutdown.
	runCtx, cancel := context.WithCancel(context.Background())

	// This session gets its OWN bus (#487): the session-local reactors subscribe
	// here, and a Forward bridge republishes every session event onto the process
	// bus (m.base.Bus) stamped with this session's id, so process-wide consumers
	// (relay, chunker, recall speculation) attribute it. A nil process bus (bench /
	// voice-standalone) makes Forward a no-op — the session bus still drives the
	// session-local reactors. The loop reads cfg.Bus, so point it at the session bus.
	sessionBus := voiceevent.NewBus()
	cfg.Bus = sessionBus
	stopForward := voiceevent.Forward(sessionBus, m.base.Bus, vs.ID.String())

	// Install the session Identity on the run context (#487): the per-turn consumers
	// that do NOT ride the bus (memory Recall, KG-facts) resolve their session from
	// here instead of a global snapshot, exactly as bus consumers resolve it from the
	// event's stamped SessionID.
	runCtx = NewContext(runCtx, Identity{SessionID: vs.ID, CampaignID: campaignID, TenantID: tenantID})

	as := &activeSession{
		campaignID:  campaignID,
		tenantID:    tenantID,
		session:     vs,
		cancel:      cancel,
		done:        make(chan struct{}),
		bus:         sessionBus,
		stopForward: stopForward,
		muted:       map[string]struct{}{}, // fresh: every new session starts all-unmuted (AC5)
	}

	// Spend meter (#130, ADR-0046): only when the Tenant configured at least one
	// cap. It rides the EXISTING recorder config copy — the tee wraps cfg's
	// StageMetrics, so the meter reads the same usage calls #127 records with ZERO
	// new pipeline plumbing — and is the turn gate. No caps ⇒ nil meter, no tee, no
	// gate: byte-for-byte today's behavior.
	// The plan allowance rides the same meter (ADR-0055 gate (b)): a remaining
	// allowance below the tenant's own hard cap (or with no tenant cap at all)
	// BECOMES the hard cap, and the end_reason then names the allowance, not
	// the cap. Equal values read as the tenant's own cap.
	caps := spend.Caps{SoftUSD: storeCaps.SoftUSD, HardUSD: storeCaps.HardUSD}
	hardReason := spendHardReason
	if allowanceRemaining != nil && (caps.HardUSD == nil || *allowanceRemaining < *caps.HardUSD) {
		caps.HardUSD = allowanceRemaining
		hardReason = allowanceHardReason
	}
	if caps.SoftUSD != nil || caps.HardUSD != nil {
		meter := spend.NewMeter(caps, m.log, m.softCapTrip(as), m.hardCapTrip(as, hardReason))
		base := cfg.StageMetrics
		if base == nil {
			base = observe.Discard{}
		}
		cfg.StageMetrics = observe.TeeUsage(base, meter)
		cfg.Gate = meter
		as.meter = meter
	}

	// Usage Ledger (ADR-0054): when a UsageWriter is wired, the session's metered
	// usage is additionally bucketed per (day, component, provider, model) for
	// durable per-Tenant cost attribution — flushed at loop exit, never a gate.
	// It tees onto whatever StageMetrics already is (the recorder, or the
	// recorder+meter tee above — TeeUsage composes), so the capture points are
	// unchanged. No UsageWriter ⇒ no ledger, byte-for-byte today's behavior.
	if m.usage != nil {
		ledger := billing.NewLedger(tenantID, nil)
		base := cfg.StageMetrics
		if base == nil {
			base = observe.Discard{}
		}
		cfg.StageMetrics = observe.TeeUsage(base, ledger)
		as.ledger = ledger
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
		// Publish onto THIS session's bus (#487): Forward bridges it onto the
		// process bus stamped, so the SSE relay attributes the spendcap to the
		// right session even with a second session live.
		as.bus.Publish(voiceevent.SpendCapReached{At: time.Now(), Level: voiceevent.SpendCapSoft})
	}
}

// hardCapTrip returns the meter's onHard callback for session as: end the session
// itself cleanly (#130, ADR-0046). It runs on a FRESH goroutine (the #211
// lock-order pattern): the meter fires it outside its own mutex, but it must take
// Manager.mu to record the deliberate end_reason override, then publish + cancel
// OUTSIDE Manager.mu — the relay's SpendCapReached handler calls Snapshot (Manager.mu),
// so publishing under the lock would deadlock. Guarded by m.active == as AND
// !as.ended so a trip arriving after the session already rolled over — or during
// the end window after the loop returned (#487) — is a no-op: publishing then
// would race the bridge cut and try to stamp a hard-cap onto an ended session.
func (m *Manager) hardCapTrip(as *activeSession, reason string) func() {
	return func() {
		go func() {
			m.mu.Lock()
			if m.active != as || as.ended {
				m.mu.Unlock()
				return // this session already ended / rolled over
			}
			as.endReasonOverride = reason
			cancel := as.cancel
			bus := as.bus
			m.mu.Unlock()

			// Onto this session's own bus (#487): Forward stamps it onto the process
			// bus so the relay attributes the hard-cap to the right session.
			bus.Publish(voiceevent.SpendCapReached{At: time.Now(), Level: voiceevent.SpendCapHard})
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

	// End the session's bridge and resolvability IMMEDIATELY — before the
	// multi-second finalizers (transcript/highlight/chunk flush, CloseVoiceSession)
	// run and before m.active clears (#487 resurrection window). Bus.Publish is
	// synchronous, so every tail event the loop produced (final TurnEnded, …) is
	// already forwarded onto the process bus; nothing is lost by cutting here.
	//
	// Two cuts, together closing the window a straggler could resurrect a Closed
	// projection through: (1) stopForward detaches the session→process bridge, so a
	// late publish on the session bus (a tape-consent PublishToCampaign, a web
	// mute/say, a racing hardCapTrip) no longer reaches the process-wide consumers;
	// (2) ended=true makes Lookup/Resolve report this session gone at once, so even
	// a direct Resolve during the finalizer window returns false. Without this a
	// straggler arriving after relay.Finalize/chunker.FlushSession Closed their
	// entry would re-create it — a fresh "status: live" frame after the terminal
	// idle (#144 regression) and a permanently leaked entry (Close never fires
	// again). ended is written under m.mu; hardCapTrip reads it there too.
	m.mu.Lock()
	as.ended = true
	m.mu.Unlock()
	as.stopForward()

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

	// Flush the session's Usage Ledger into the usage_ledger table (ADR-0054).
	// Best-effort with its OWN budget, like the finalizers above: a flush failure
	// loses only this session's usage attribution (an estimates-only ledger) and
	// never blocks the row from ending.
	if as.ledger != nil {
		usageCtx, usageCancel := context.WithTimeout(base, m.endTimeout)
		if err := as.ledger.Flush(usageCtx, m.usage.AddUsage); err != nil {
			m.log.Warn("flush usage ledger before end", "err", err, "voice_session", as.session.ID)
		}
		usageCancel()
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

// Lookup returns this Manager's active Voice Session and true when its id is
// sessionID, or the zero value and false otherwise (idle, or running a different
// session). It is the per-Manager read [Registry.Resolve] fans out across every
// registered Manager to attribute a stamped bus event to its origin session.
func (m *Manager) Lookup(sessionID uuid.UUID) (storage.VoiceSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil || m.active.ended || m.active.session.ID != sessionID {
		return storage.VoiceSession{}, false
	}
	return m.active.session, true
}

// PublishToCampaign publishes e onto this Manager's live session bus when it is
// running campaignID, returning true, or false when idle / running a different
// Campaign. The publish runs OUTSIDE Manager.mu (the session bus's Forward bridge
// re-enters the process bus, whose consumers Resolve back through the Manager —
// holding mu across the publish would deadlock): the bus pointer is captured
// under the lock, then published unlocked. It is the per-Manager leg of
// [Registry.PublishToCampaign].
func (m *Manager) PublishToCampaign(campaignID uuid.UUID, e voiceevent.Event) bool {
	m.mu.Lock()
	if m.active == nil || m.active.ended || m.active.campaignID != campaignID {
		m.mu.Unlock()
		return false
	}
	bus := m.active.bus
	m.mu.Unlock()
	bus.Publish(e)
	return true
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
// the true current mute state from here. It routes through the current
// [LiveSession] (#448).
func (m *Manager) MutedAgentIDs() []string {
	l := m.Live()
	if l == nil {
		return nil
	}
	return l.MutedAgentIDs()
}
