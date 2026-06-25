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
)

// Tenant is the top-level isolation boundary.
type Tenant struct {
	ID        uuid.UUID
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
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
