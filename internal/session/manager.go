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
	// ErrSessionActive is returned by Start when THIS Tenant already has a running
	// session — the per-Tenant single-active guard (AC2, #488): a Tenant is capped
	// at one live Voice Session, so a second Start for the same Tenant (even for a
	// different Campaign) collides. Mapped to CodeAlreadyExists.
	ErrSessionActive = errors.New("session: a voice session is already active")
	// ErrSessionLimit is returned by Start when the process is already running its
	// configured maximum of concurrent Voice Sessions (Deps.MaxSessions, #488): a
	// distinct, user-visible refusal separate from the per-Tenant ErrSessionActive.
	// Mapped to CodeResourceExhausted (the ErrAllowanceExhausted precedent).
	ErrSessionLimit = errors.New("session: the process concurrent voice session limit is reached")
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

	// ErrIntentPending is returned by IntentControl.Start when the claim plane wrote
	// the intent but no -mode voice worker claimed and drove it live within the Start
	// budget (#491, split mode): the session is queued, not failed. The RPC layer
	// maps it to CodeUnavailable ("voice worker has not claimed the session yet") so
	// the operator retries.
	ErrIntentPending = errors.New("session: voice worker has not claimed the session yet")
	// ErrIntentCancelled is returned by IntentControl.Start when the intent it was
	// waiting on went to a terminal 'done' WITHOUT ever going live (#491 review item
	// 7): a stop landed on the still-pending row, so the start was cancelled — a
	// distinct outcome from ErrIntentPending (still queued). The RPC maps it to
	// CodeAborted so the operator sees "start was cancelled", not "not claimed yet".
	ErrIntentCancelled = errors.New("session: the voice session start was cancelled before a worker claimed it")
	// ErrStopPending is returned by IntentControl.Stop when the owning worker did not
	// confirm the wind-down within the Stop budget (#491 review item 7): the session
	// may still be running, so it is surfaced as an error (→ CodeUnavailable, retry)
	// rather than a false success carrying a still-'running' row.
	ErrStopPending = errors.New("session: the voice worker has not confirmed the stop yet")
	// ErrSplitMode is returned by the IntentControl methods that only the in-process
	// Manager can serve — live mute/say/replay/spend reads (#491): in a split
	// (-mode web + -mode voice) deployment the web tier holds no live session state,
	// so these degrade rather than lie. The RPC layer maps it to
	// CodeFailedPrecondition ("not available in a split deployment").
	ErrSplitMode = errors.New("session: not available in a split deployment")

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
// Manager Begins it at Start (binding the session's owning ids) and receives back
// the PER-SESSION [highlight.Sink] it wires as that session's cfg.Highlights (#488),
// so N concurrent sessions each feed their own binding; the Manager Finalizes it at
// loop exit BY SESSION ID (scheduling the 7-day candidate purge) — beside
// transcript.Finalize, at EVERY exit path. *highlight.Saver satisfies it. Defined
// here (not imported as a concrete type) to keep the seam narrow; nil (highlights
// off, or a web-only Manager that drives no voice) makes Begin/Finalize no-ops.
type Highlighter interface {
	Begin(voiceSessionID, campaignID, tenantID uuid.UUID) highlight.Sink
	Finalize(ctx context.Context, voiceSessionID uuid.UUID) error
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

	// pubMu serializes the whole write-set→publish-MuteChanged sequence across THIS
	// session's mute ops (#211), so two overlapping ops (a per-Agent toggle racing a
	// mute-all) can never publish events in the reverse order of their set writes —
	// which would let a stale event de-sync the wirenpc matcher from the
	// authoritative set. It lives PER SESSION (#488): #487 kept one global pubMu on
	// the Manager, but with N concurrent sessions the ordering guarantee is
	// per-session (each session has its own bus + mute set), so splitting it here
	// keeps two tenants' mute bursts from needlessly serializing against each other.
	// Ordered AFTER Manager.mu: an op takes pubMu, then Manager.mu for the set write,
	// drops Manager.mu, publishes while still holding pubMu, then drops pubMu.
	pubMu sync.Mutex
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

// Manager owns up to MaxSessions concurrent live Voice Sessions, at most one per
// Tenant (the per-Tenant single-active guard, #488). It is safe for concurrent use.
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
	// gmSpeakerForTenant overlays cfg.GMSpeaker per Start with the session's Tenant
	// (#490); nil leaves the base deployment-wide gate.
	gmSpeakerForTenant func(tenantID uuid.UUID, discordUserID string) bool
	// speakerNameForCampaign overlays cfg.SpeakerName per Start with the session's
	// Campaign (#488): agent.Config.SpeakerName carries no ctx, so a base closure
	// shared by N sessions would resolve every one against a single-active global
	// read. The Manager rebinds it here per session, pinning THIS session's Campaign.
	// nil leaves the base cfg.SpeakerName untouched (voice-standalone / test).
	speakerNameForCampaign func(campaignID uuid.UUID, speakerID string) string
	log                    *slog.Logger
	enabled                bool          // false in web-only mode: Start is rejected (ADR-0039)
	endTimeout             time.Duration // per-step end budget (Finalize, end-write); endTimeout in prod, shrunk in tests
	// maxSessions is the process-wide cap on concurrent live Voice Sessions
	// (Deps.MaxSessions, #488, ADR-0057's per-process K): a Start when
	// len(active) == maxSessions is refused with ErrSessionLimit. Always >= 1
	// (NewManager clamps a 0/negative Deps value to 1 — today's single-session
	// default, byte-identical behaviour).
	maxSessions int

	mu     sync.Mutex
	active map[uuid.UUID]*activeSession // keyed by tenantID; at most maxSessions entries
	// reservations holds the Tenants with a Start in its I/O phase (#488 review item
	// 3): Start reserves a slot under mu, RELEASES mu for the store round-trips
	// (deployment config, token, caps, allowance, CreateVoiceSession), then re-takes
	// mu only to commit — so one Tenant's slow Start never freezes Muted()/Lookup/
	// Active/Stop for the others. A reservation counts toward both guards (a reserving
	// Tenant collides ErrSessionActive; a reservation fills a cap slot), so the
	// per-Tenant-single and cap-K invariants hold even while the I/O is unlocked.
	reservations map[uuid.UUID]struct{}
	closed       bool // terminal: set by Shutdown; Start refuses with ErrManagerClosed (#157)
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
	// GMSpeakerForTenant, when non-nil, is the per-Tenant GM-address verdict wired
	// to auth.GMIdentity.IsGMInTenant (#490): Start overlays cfg.GMSpeaker with a
	// closure that pins THIS session's Tenant, so the Butler voice-address gate
	// scopes GM standing to the session's Tenant (a Tenant A operator is not GM in a
	// Tenant B session). nil (voice-standalone / test / web-only) leaves the base
	// cfg.GMSpeaker untouched.
	GMSpeakerForTenant func(tenantID uuid.UUID, discordUserID string) bool
	// SpeakerNameForCampaign, when non-nil, is the per-Campaign Speaker-Lane display
	// resolver (#488): Start overlays cfg.SpeakerName with a closure pinning THIS
	// session's Campaign, so N concurrent sessions each attribute user lines against
	// their OWN roster instead of a single-active global read. nil leaves the base
	// cfg.SpeakerName untouched (voice-standalone / test / web-only).
	SpeakerNameForCampaign func(campaignID uuid.UUID, speakerID string) string
	// MaxSessions is the process-wide cap on concurrent live Voice Sessions (#488,
	// ADR-0057's per-process K): a Start beyond it is refused ErrSessionLimit. The
	// zero value (unset) means 1 — today's single-session default, byte-identical
	// behaviour; the composition root reads GLYPHOXA_MAX_VOICE_SESSIONS into it.
	MaxSessions int
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
	maxSessions := deps.MaxSessions
	if maxSessions < 1 {
		maxSessions = 1 // unset / invalid ⇒ today's single-session default (byte-identical)
	}
	m := &Manager{
		store:                  store,
		run:                    run,
		base:                   base,
		cipher:                 cipher,
		transcript:             deps.Transcript,
		chunker:                deps.Chunker,
		highlights:             deps.Highlights,
		usage:                  deps.Usage,
		allowance:              deps.Allowance,
		clients:                deps.Clients,
		gmSpeakerForTenant:     deps.GMSpeakerForTenant,
		speakerNameForCampaign: deps.SpeakerNameForCampaign,
		log:                    log,
		enabled:                enabled,
		endTimeout:             endTimeout,
		maxSessions:            maxSessions,
		active:                 map[uuid.UUID]*activeSession{},
		reservations:           map[uuid.UUID]struct{}{},
	}
	// The Manager IS the live mute view (#211): it owns the session-local mute set,
	// so wire it as the base voice config's MuteView. Every session Start copies
	// base, so each session's Conversation reads this Manager's set.
	m.base.Mutes = m
	m.base.Memory = deps.Memory
	m.base.Facts = deps.Facts
	m.base.ToolDeps = deps.Tools
	// cfg.Highlights is NO LONGER wired on the base config (#488): with N concurrent
	// sessions the detector Sink must be per-session, so Start sets cfg.Highlights to
	// the session-bound Sink that Highlighter.Begin returns instead of a single
	// shared value.
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

	// Reserve a slot under mu, then RELEASE mu for the store I/O below (#488 review
	// item 3): holding mu across the deployment/token/caps/allowance/CreateVoiceSession
	// round-trips would freeze Muted() (the voice hot path), Lookup (every bus event),
	// Active and Stop for EVERY other Tenant behind one Tenant's slow store. The
	// reservation counts toward both guards, so the per-Tenant-single and cap-K
	// invariants hold even while the I/O runs unlocked.
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return storage.VoiceSession{}, ErrManagerClosed
	}
	// Per-Tenant single-active guard FIRST (#488): a Tenant already live OR reserving
	// collides with ErrSessionActive even below the cap — and even for a different
	// Campaign (decision: one live session per Tenant).
	if _, live := m.active[tenantID]; live {
		m.mu.Unlock()
		return storage.VoiceSession{}, ErrSessionActive
	}
	if _, reserving := m.reservations[tenantID]; reserving {
		m.mu.Unlock()
		return storage.VoiceSession{}, ErrSessionActive
	}
	// Process-wide cap SECOND (#488, ADR-0057 K): live + reserving both fill slots.
	if len(m.active)+len(m.reservations) >= m.maxSessions {
		m.mu.Unlock()
		return storage.VoiceSession{}, ErrSessionLimit
	}
	m.reservations[tenantID] = struct{}{}
	m.mu.Unlock()

	// releaseReservation drops this Tenant's slot; called on EVERY early return in
	// the unlocked I/O phase (and at commit, replaced by the active entry).
	releaseReservation := func() {
		m.mu.Lock()
		delete(m.reservations, tenantID)
		m.mu.Unlock()
	}

	dep, err := m.store.GetDeploymentConfig(ctx, tenantID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		releaseReservation()
		return storage.VoiceSession{}, fmt.Errorf("session: load deployment config: %w", err)
	}
	if dep.GuildID == "" || dep.VoiceChannelID == "" {
		releaseReservation()
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
		releaseReservation()
		return storage.VoiceSession{}, fmt.Errorf("%w: %w", ErrDiscordTokenUndecryptable, err)
	}
	if token == "" {
		releaseReservation()
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
		releaseReservation()
		return storage.VoiceSession{}, fmt.Errorf("session: load spend caps: %w", err)
	}

	// Plan-allowance gate (ADR-0055 gate (b)), snapshot beside the caps: when a
	// checker is wired (`open` Admission Mode) and the plan carries an
	// allowance, an exhausted one refuses the start outright and a remaining
	// one tightens this session's hard cap below. Like the caps read, it runs
	// BEFORE the insert so a failure never strands a 'running' row (#433), and
	// a read failure fails the start CLOSED.
	//
	// ALLOWANCE SNAPSHOT CAVEAT (#488, RE-DOCUMENTED not fixed): the month-to-date
	// figure is the FLUSHED usage_ledger only. A running session's own spend is not in
	// it until the ledger flushes at loop exit — and that flush is BEST-EFFORT
	// (see runLoop: a flush failure only warns). So the real residual is a
	// flush-undercount, orthogonal to concurrency: whenever a prior session's ledger
	// flush failed (or has not yet run), THIS Start's remaining-allowance snapshot is
	// computed against a month-to-date total that understates true spend, so the gate
	// admits a start it might have refused (or tightens the hard cap less than it
	// should). It never OVER-refuses. The meter still hard-stops this session at its
	// own snapshot, and the estimate resets monthly (ADR-0046/0055). A durable
	// per-Tenant running-total that survives a failed flush is a later epic.
	var allowanceRemaining *float64
	if m.allowance != nil {
		state, err := m.allowance.AllowanceState(ctx, tenantID)
		if err != nil {
			releaseReservation()
			return storage.VoiceSession{}, fmt.Errorf("session: load plan allowance: %w", err)
		}
		if state.Exhausted() {
			releaseReservation()
			return storage.VoiceSession{}, ErrAllowanceExhausted
		}
		allowanceRemaining = state.RemainingUSD()
	}

	vs, err := m.store.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		releaseReservation()
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

	// Per-Tenant Butler GM-address gate (#490, ADR-0055): overlay the base's
	// deployment-wide GMSpeaker with THIS session's Tenant, so a Tenant A operator
	// is not a GM in a Tenant B session's voice channel. nil seam (voice-standalone /
	// test / web-only) leaves the base gate untouched.
	if m.gmSpeakerForTenant != nil {
		gate := m.gmSpeakerForTenant
		cfg.GMSpeaker = func(discordUserID string) bool { return gate(tenantID, discordUserID) }
	}

	// Per-Campaign Speaker-Lane display resolver (#488): agent.Config.SpeakerName
	// carries no ctx, so a base closure shared by N sessions would resolve every one
	// against a single-active global read. Rebind it here, pinning THIS session's
	// Campaign, so each concurrent session attributes user lines against its own
	// roster. nil seam (voice-standalone / test / web-only) leaves the base untouched.
	if m.speakerNameForCampaign != nil {
		resolve := m.speakerNameForCampaign
		cfg.SpeakerName = func(speakerID string) string { return resolve(campaignID, speakerID) }
	}

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
	// detector) can fire a Trigger (#308, #488): Begin records the owning ids the
	// Saver stamps onto every candidate row + its blob key and returns THIS session's
	// Sink, which becomes cfg.Highlights (the detector's per-session sink — never a
	// shared base value now that N sessions run). Finalize (runLoop) drains + unbinds
	// this session by id and schedules the purge. A nil Highlighter (highlights off /
	// web-only) leaves cfg.Highlights nil (no detector wiring).
	if m.highlights != nil {
		cfg.Highlights = m.highlights.Begin(vs.ID, campaignID, tenantID)
	}

	// COMMIT (#488 review item 3): re-take mu only now, after all the store I/O, to
	// swap this Tenant's reservation for its live session and launch the loop. The
	// reservation blocked any concurrent same-Tenant Start and held the cap slot the
	// whole time, so no second session for this Tenant can exist here — only Shutdown
	// can have raced in, which the closed check catches.
	m.mu.Lock()
	delete(m.reservations, tenantID)
	if m.closed {
		m.mu.Unlock()
		// A Shutdown landed during our I/O phase: never launch a loop nothing will
		// cancel (#157). Tear down what we built and close the freshly-created row so
		// it is not left 'running' (the boot reconciliation is the backstop, #143).
		as.cancel()
		stopForward()
		if m.highlights != nil {
			hlCtx, hlCancel := context.WithTimeout(context.Background(), m.endTimeout)
			_ = m.highlights.Finalize(hlCtx, vs.ID)
			hlCancel()
		}
		endCtx, endCancel := context.WithTimeout(context.Background(), m.endTimeout)
		if _, cerr := m.store.CloseVoiceSession(endCtx, vs.ID, storage.VoiceSessionEnded, vs.LineCount, nil); cerr != nil {
			m.log.Error("close voice session after shutdown-race Start", "err", cerr, "voice_session", vs.ID)
		}
		endCancel()
		return storage.VoiceSession{}, ErrManagerClosed
	}
	m.active[tenantID] = as
	go m.runLoop(runCtx, as, cfg)
	m.mu.Unlock()
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
			if m.active[as.tenantID] != as || as.ended {
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

// Spend returns a snapshot of tenantID's active session spend meter (#130, #488):
// estimated USD, cap state, and configured caps. No session for that Tenant, or one
// with no caps configured, reports the zero Status (no state, zero spend) — the
// feature-off surface. Per-session, so one Tenant's spend never reads another's.
func (m *Manager) Spend(tenantID uuid.UUID) spend.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	as := m.active[tenantID]
	if as == nil || as.meter == nil {
		return spend.Status{}
	}
	return as.meter.Status()
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
		if err := m.highlights.Finalize(hlCtx, as.session.ID); err != nil {
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
	if m.active[as.tenantID] == as {
		delete(m.active, as.tenantID)
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
func (m *Manager) Stop(ctx context.Context, tenantID uuid.UUID) (storage.VoiceSession, error) {
	m.mu.Lock()
	as := m.active[tenantID]
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

// Active returns tenantID's live Voice Session and true, or the zero value and
// false when that Tenant has none (the S3 read backing GetSession, mute/say/end
// guards, and the slash Active-Campaign resolver, #488). It reports false the
// instant the session begins ending (as.ended, the #487 tombstone) so a caller
// never operates on a session mid-teardown. The ctx/error shape matches the S3
// contract (and #491's DB-backed sibling); this in-process impl never errors.
func (m *Manager) Active(_ context.Context, tenantID uuid.UUID) (storage.VoiceSession, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	as := m.active[tenantID]
	if as == nil || as.ended {
		return storage.VoiceSession{}, false, nil
	}
	return as.session, true, nil
}

// IsCampaignLive reports whether ANY live Voice Session across all Tenants is bound
// to campaignID (#488 review item 2). It backs the archive/delete live-guard
// (rpc/campaign_archive.go): at cap >1 the correct guard is "is this Campaign live
// in ANY session", not "is it the single active session" — so it scans the whole
// map rather than one arbitrary session. A session already ending (as.ended) no
// longer counts as live. Correct at any cap.
func (m *Manager) IsCampaignLive(campaignID uuid.UUID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, as := range m.active {
		if !as.ended && as.campaignID == campaignID {
			return true
		}
	}
	return false
}

// HasCapacity reports whether the Manager can accept another Voice Session — its
// live + reserving count is below MaxSessions (#491). The -mode voice claim loop
// consults it before claiming an intent, so it never claims work it cannot run
// (avoiding an ErrSessionLimit strand): claim only while there is a free slot. A
// snapshot under the lock; in the single-worker interim (#492 elects the sole
// claimer) nothing else fills a slot between this read and the following Start.
func (m *Manager) HasCapacity() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.active)+len(m.reservations) < m.maxSessions
}

// AnyLive reports whether ANY Voice Session is currently running in this process
// (#150, #488): the tenant-agnostic health signal the Discord probe reads. A
// session in its end window (as.ended) no longer counts. Correct at any cap.
func (m *Manager) AnyLive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, as := range m.active {
		if !as.ended {
			return true
		}
	}
	return false
}

// Lookup returns the live Voice Session with id sessionID and true, scanning every
// Tenant's active session (session ids are globally unique), or the zero value and
// false when none matches (idle, ended, or a foreign id). It is the per-Manager
// read [Registry.Resolve] fans out to attribute a stamped bus event to its origin
// session — now scanning N concurrent sessions rather than the single-active one.
func (m *Manager) Lookup(sessionID uuid.UUID) (storage.VoiceSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, as := range m.active {
		if !as.ended && as.session.ID == sessionID {
			return as.session, true
		}
	}
	return storage.VoiceSession{}, false
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
	var bus *voiceevent.Bus
	for _, as := range m.active {
		if !as.ended && as.campaignID == campaignID {
			bus = as.bus
			break
		}
	}
	m.mu.Unlock()
	if bus == nil {
		return false
	}
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
	sessions := make([]*activeSession, 0, len(m.active))
	for _, as := range m.active {
		sessions = append(sessions, as)
	}
	m.mu.Unlock()
	// Cancel every live session, then wait for each loop to end and its ended_at
	// write to land (#488): a SIGTERM must leave NO Tenant's row stuck 'running'.
	for _, as := range sessions {
		as.cancel()
	}
	for _, as := range sessions {
		<-as.done
	}
}

// Muted reports whether the Agent with agentID is muted in ANY live session,
// satisfying [orchestrator.MuteView] (#211). Agent ids are globally unique (a
// Campaign's roster), and the voice loop that consults this per route belongs to
// exactly one session, so scanning every Tenant's mute set (#488) is correct and
// unambiguous: a muted agent belongs to only one live session's set. It stays
// tenant-free precisely because the loop already knows which agent it is asking
// about. No live session ⇒ always unmuted (the feature is off between sessions).
func (m *Manager) Muted(agentID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, as := range m.active {
		if _, ok := as.muted[agentID]; ok {
			return true
		}
	}
	return false
}

// MutedAgentIDs is a sorted snapshot of tenantID's currently-muted Agent ids, or
// nil when that Tenant has no live session. It backs GetSession's reload truth
// (AC5): a mid-session page reload reads the true current mute state from here. It
// routes through that Tenant's [LiveSession] (#448, #488).
func (m *Manager) MutedAgentIDs(tenantID uuid.UUID) []string {
	l := m.Live(tenantID)
	if l == nil {
		return nil
	}
	return l.MutedAgentIDs()
}
