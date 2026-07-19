package storage

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AgentRole is an Agent's archetype (ADR-0009): the agents table is polymorphic
// over this enum so one orchestrator/address-detection path serves both.
type AgentRole string

const (
	AgentRoleButler    AgentRole = "butler"
	AgentRoleCharacter AgentRole = "character"
)

// Component is a Provider category a Provider Config binds to (ADR-0004).
type Component string

const (
	ComponentLLM        Component = "llm"
	ComponentSTT        Component = "stt"
	ComponentTTS        Component = "tts"
	ComponentEmbeddings Component = "embeddings"
	ComponentS2S        Component = "s2s"
	// ComponentImage is AI image generation (#311, Epic 8, ADR-0004 amendment):
	// the enum value the 00028 migration adds. Gemini is its v1 provider.
	ComponentImage Component = "image"
)

// Tenant is the top-level isolation boundary.
type Tenant struct {
	ID   uuid.UUID
	Name string
	// SpendCapSoftUSD / SpendCapHardUSD are the two independently opt-in per-Tenant
	// spend caps (#130, ADR-0046): nil = that cap is off. They gate a Voice Session's
	// estimated spend (an approximate figure, never a billed amount).
	SpendCapSoftUSD *float64
	SpendCapHardUSD *float64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SpendCaps is the get/set DTO for a Tenant's two spend caps (#130, ADR-0046),
// each nil when that cap is unset. It is the storage-layer value the session
// Manager maps onto its meter and the RPC round-trips; keeping it distinct from
// spend.Caps keeps storage free of a spend-package import.
type SpendCaps struct {
	SoftUSD *float64
	HardUSD *float64
}

// Campaign is a persistent TTRPG game owned by a Tenant and GM'd by one Member.
type Campaign struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	// GMMemberID references a Member (Member Role 'gm'). The members table is
	// task #6, so this is a bare nullable UUID for now (SEAM #6).
	GMMemberID uuid.NullUUID
	Name       string
	System     string
	Language   string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// ArchivedAt is when the campaign was archived (#269), or nil when active.
	// Archived campaigns are excluded from ListCampaigns, the /glyphoxa use
	// autocomplete, and the GetActiveCampaign most-recent fallback, and cannot back
	// a Voice Session.
	ArchivedAt *time.Time
	// TapeArmed is the GM opt-in that arms the rollover tape for this Campaign's
	// Voice Sessions (#306, ADR-0051; default false, capture hard-disabled without
	// it). Appended LAST in campaignColumns/scanCampaign (column-order coupling), so
	// any new column follows it in both places.
	TapeArmed bool
}

// ProviderConfig is a Tenant-scoped, encrypted BYOK credential record binding a
// Component to a Provider (ADR-0004). Credentials are write-only after save;
// CredentialsCiphertext is AES-GCM, and only Last4 is plaintext for display.
type ProviderConfig struct {
	ID                    uuid.UUID
	TenantID              uuid.UUID
	Component             Component
	Provider              string
	Model                 string
	CredentialsCiphertext []byte
	CredentialsLast4      string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// DeploymentConfig is the single-operator Discord integration the Configuration
// screen edits (#68): the deployment Bot token — a write-only secret, sealed at
// rest like a Provider Config — plus the non-secret Guild / Voice channel IDs.
// The Bot is deployment-shared (one token regardless of Tenant, CONTEXT.md), so
// this is distinct from the per-Component, Tenant-scoped provider_config
// (ADR-0004); it is keyed by tenant_id only for the MVP single operator
// (ADR-0039). DiscordBotTokenCiphertext is empty until a token is saved.
type DeploymentConfig struct {
	TenantID                  uuid.UUID
	DiscordBotTokenCiphertext []byte
	DiscordBotTokenLast4      string
	GuildID                   string
	VoiceChannelID            string
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

// VoiceSessionStatus is a Voice Session's lifecycle state (#72). A session is
// 'running' from Start until Stop (or loop exit), then 'ended' — or 'failed' when
// a fatal, non-retryable gateway rejection ended it (#123).
type VoiceSessionStatus string

const (
	VoiceSessionRunning VoiceSessionStatus = "running"
	VoiceSessionEnded   VoiceSessionStatus = "ended"
	// VoiceSessionFailed is the terminal state of a session whose Discord gateway
	// connection failed FATALLY — a non-retryable rejection (invalid Bot token,
	// disallowed intents, gateway reject) the reconnect loop stopped on rather than
	// backing off forever (#123). The row's end_reason carries the readable cause.
	// Like 'ended' it is terminal (never revived), but records that the session
	// never served — distinct from a clean stop.
	VoiceSessionFailed VoiceSessionStatus = "failed"
)

// VoiceSessionReasonOrphaned is the end_reason stamped by the boot-time
// reconciliation (#143): the row was still 'running' but no live loop owned it
// (crash / kill -9 / a failed end-write), so startup closed it. A NULL
// end_reason means the session ended through the normal Stop / loop-exit path.
const VoiceSessionReasonOrphaned = "orphaned: reconciled at startup"

// VoiceSession is one run of the live voice loop — the Bot's presence in one
// Discord voice channel, bound to a Campaign (CONTEXT.md "Voice Session", #72).
// EndedAt is nil while running; LineCount records transcript lines produced (0
// for this stage — the live feed is #73). EndReason is nil for a clean end, and
// set when the boot reconciliation closed an orphaned row (#143) or a fatal
// gateway rejection ended the session as 'failed' (#123, the readable cause).
type VoiceSession struct {
	ID         uuid.UUID
	CampaignID uuid.UUID
	StartedAt  time.Time
	EndedAt    *time.Time
	Status     VoiceSessionStatus
	LineCount  int
	EndReason  *string
}

// VoiceSessionIntentStatus is a Voice Session Intent's lifecycle state (#491,
// ADR-0057 (b)): the claim-plane row a web-tier Start writes and a -mode voice
// worker claims. 'pending' → 'claimed' (a worker took it) → 'live' (its loop is
// up) → 'done' (clean end) / 'failed' (loop fault) / 'dead' (the worker's
// heartbeat went stale — no takeover, ADR-0006). The three non-terminal states
// share the one-live-per-tenant partial UNIQUE index.
type VoiceSessionIntentStatus string

const (
	// VoiceIntentPending: written by Start, not yet claimed by any worker.
	VoiceIntentPending VoiceSessionIntentStatus = "pending"
	// VoiceIntentClaimed: a worker won the FOR UPDATE SKIP LOCKED claim and stamped
	// its instance_id; its loop is starting but not yet live.
	VoiceIntentClaimed VoiceSessionIntentStatus = "claimed"
	// VoiceIntentLive: the worker's voice loop is up and the voice_sessions row is
	// bound; the worker heartbeats while in this state.
	VoiceIntentLive VoiceSessionIntentStatus = "live"
	// VoiceIntentDone: terminal clean end (Stop honored, or the loop self-exited).
	VoiceIntentDone VoiceSessionIntentStatus = "done"
	// VoiceIntentDead: terminal — the owning worker's heartbeat went stale, so the
	// reaper marked it dead. No mid-session takeover (ADR-0006/0057 (e)): the Tenant
	// restarts; the session is NEVER handed to another Voice Instance.
	VoiceIntentDead VoiceSessionIntentStatus = "dead"
	// VoiceIntentFailed: terminal — the claiming worker could not run the session
	// (e.g. the Manager refused it), last_error carries the readable cause.
	VoiceIntentFailed VoiceSessionIntentStatus = "failed"
)

// VoiceSessionIntent is one row of the voice-session claim plane (#491): a
// tenant-keyed intent a web Start writes and a -mode voice worker claims, runs,
// and heartbeats. VoiceSessionID is set once the worker goes live; ClaimedAt /
// HeartbeatAt / EndedAt track the claim lifecycle; LastError carries a fault's
// readable cause. Mirrors the storage.Job shape (ADR-0049).
type VoiceSessionIntent struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	CampaignID     uuid.UUID
	Status         VoiceSessionIntentStatus
	InstanceID     string
	VoiceSessionID uuid.NullUUID
	StopRequested  bool
	LastError      string
	CreatedAt      time.Time
	ClaimedAt      *time.Time
	HeartbeatAt    *time.Time
	EndedAt        *time.Time
}

// Agent is an AI-controlled persona — Butler or Character NPC (ADR-0009).
type Agent struct {
	ID         uuid.UUID
	CampaignID uuid.UUID
	Role       AgentRole
	Name       string
	// Title is the Agent's role subtitle shown in the editor (e.g. "Gruff
	// innkeeper"); free text, may be empty.
	Title string
	// Persona: markdown personality/backstory/speech style.
	Persona string
	// Voice (ADR-0022/0023): TTS provider + voice-id config, stored as JSONB.
	Voice json.RawMessage
	// VoiceProviderConfigID is the TTS Provider Config backing this Agent's Voice.
	VoiceProviderConfigID uuid.NullUUID
	// LLMProviderConfigID is the LLM Provider Config this Agent reasons with.
	// May be null; resolving a tenant default when null is a #6 concern (the
	// schema has no is_default marker yet, so no fallback is wired here).
	LLMProviderConfigID uuid.NullUUID
	// AddressOnly: reachable only by explicit name/alias (ADR-0024). Butler true.
	AddressOnly bool
	// SpeakerColor is a server-assigned palette SLOT (not a colour value): the web
	// tier maps it onto its speaker palette so each roster member renders in a
	// stable hue across reloads (#71). Assigned round-robin per Campaign on
	// Character insert; the Butler keeps slot 0.
	SpeakerColor int
	Aliases      []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ToolGrant is an Agent's persisted permission to invoke one named Tool
// (ADR-0029) — the DB shape of the in-memory Grant the live loop hydrates into a
// GrantSet (#113). Config is the optional per-grant scope/config (jsonb): nil
// when the grant carries no narrowing (dice), a scope blob for a Tool granted
// differently per Agent. It reaches the Tool handler at execution time and is
// enforced there, never by the LLM.
type ToolGrant struct {
	ID        uuid.UUID
	AgentID   uuid.UUID
	ToolName  string
	Config    json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

// User is a human operator authenticated via Discord OAuth (ADR-0016). The
// Discord snowflake is the stable identity key; Name/Avatar are display-only and
// refreshed from Discord on each login.
type User struct {
	ID            uuid.UUID
	DiscordUserID string
	Name          string
	// Avatar is an absolute image URL (or empty).
	Avatar    string
	Role      string
	CreatedAt time.Time
	UpdatedAt time.Time
	// SuspendedAt, when non-nil, marks the user locked out under the
	// open-Admission-Mode revocation mechanism (ADR-0055). Appended LAST —
	// userColumns/scanUser are column-order-coupled.
	SuspendedAt *time.Time
}

// Session is a server-side login session (ADR-0016): the Token is the opaque
// random secret carried in the glyphoxa_session cookie, and this row is the
// authority. ExpiresAt gates validity; deleting the row revokes instantly.
type Session struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Token      string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	IP         string
	UA         string
}

// LoadedAgent is an Agent with its bound Provider Configs resolved — the bundle
// the orchestrator needs to bring an Agent to life (Persona, Voice, LLM/TTS
// configs). Either config may be nil if the Agent has none bound.
type LoadedAgent struct {
	Agent     Agent
	LLMConfig *ProviderConfig
	TTSConfig *ProviderConfig
}
