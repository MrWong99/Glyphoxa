package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Write helpers for the core tables. They are deliberately thin and generic
// (no voice/agent-domain types) so the control-plane (#6) and the live-NPC seed
// (#5) share one insert path. The auto-Butler trigger (migration 00002) fires
// on CreateCampaign, so a Butler row appears without an explicit insert here.

// CreateTenant inserts a Tenant and returns its generated ID.
func (s *Store) CreateTenant(ctx context.Context, name string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx,
		`INSERT INTO tenant (name) VALUES ($1) RETURNING id`, name).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("storage: create tenant: %w", err)
	}
	return id, nil
}

// NewCampaign is the input to CreateCampaign. GMMemberID is left zero (the
// members table is task #6); the column is nullable.
type NewCampaign struct {
	TenantID uuid.UUID
	Name     string
	System   string
	Language string
}

// CreateCampaign inserts a Campaign and returns its generated ID. The
// auto-Butler trigger (ADR-0009) inserts the campaign's 'Glyphoxa' Butler as a
// side effect.
func (s *Store) CreateCampaign(ctx context.Context, c NewCampaign) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name, system, language)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		c.TenantID, c.Name, c.System, c.Language).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("storage: create campaign: %w", err)
	}
	return id, nil
}

// CampaignUpdate is the input to UpdateCampaign. It carries the operator-editable
// fields only — name/system/language; tenant_id, gm_member_id and the timestamps
// are not settable here. System/Language are written verbatim as opaque free-text
// strings (no validation at this layer), mirroring how they are stored on create.
type CampaignUpdate struct {
	ID       uuid.UUID
	Name     string
	System   string
	Language string
}

// UpdateCampaign writes a campaign's name/system/language and bumps updated_at,
// returning the updated row. A missing id yields ErrNotFound (the RPC layer maps
// it to Connect CodeNotFound). Like the other campaign reads/writes it is scoped
// by id alone — single-operator today; tenant scoping fills in behind the
// X-Tenant-Id pass-through later (ADR-0039).
func (s *Store) UpdateCampaign(ctx context.Context, c CampaignUpdate) (Campaign, error) {
	row := s.db.QueryRow(ctx,
		`UPDATE campaign SET
		    name = $2,
		    system = $3,
		    language = $4,
		    updated_at = now()
		  WHERE id = $1
		 RETURNING `+campaignColumns,
		c.ID, c.Name, c.System, c.Language)
	updated, err := scanCampaign(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	if err != nil {
		return Campaign{}, fmt.Errorf("storage: update campaign %s: %w", c.ID, err)
	}
	return updated, nil
}

// NewProviderConfig is the input to CreateProviderConfig. CredentialsCiphertext
// is the AES-GCM-sealed credential (see internal/storage/crypto); for the
// self-host voice path the real key lives in the OS keyring and this carries a
// sealed placeholder with CredentialsLast4="env" (ADR-0004 / #5 seam).
type NewProviderConfig struct {
	TenantID              uuid.UUID
	Component             Component
	Provider              string
	Model                 string
	CredentialsCiphertext []byte
	CredentialsLast4      string
}

// CreateProviderConfig inserts a Provider Config and returns its generated ID.
func (s *Store) CreateProviderConfig(ctx context.Context, p NewProviderConfig) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx,
		`INSERT INTO provider_config
		   (tenant_id, component, provider, model, credentials_ciphertext, credentials_last4)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		p.TenantID, p.Component, p.Provider, p.Model,
		p.CredentialsCiphertext, p.CredentialsLast4).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("storage: create provider_config: %w", err)
	}
	return id, nil
}

// speakerColorSlots is the size of the web speaker palette (#71). CreateAgent
// assigns a Character a slot round-robin in [0, speakerColorSlots) per Campaign;
// the web tier maps the slot onto its palette so each NPC renders in a stable
// hue. Kept in sync with migration 00004's backfill modulo and the web palette.
const speakerColorSlots = 6

// ErrButlerUndeletable is returned by DeleteAgent when the target Agent is a
// Butler. The Butler is an invariant of a Campaign's existence (ADR-0009): it is
// auto-created and cannot be removed. The RPC layer maps this to Connect
// CodeFailedPrecondition.
var ErrButlerUndeletable = errors.New("storage: butler cannot be deleted")

// UpsertProviderConfigs inserts-or-replaces a batch of Provider Configs in one
// transaction, keyed by (tenant_id, component, provider): a matching row has its
// model + sealed credential + last4 refreshed (the operator replacing a key, or
// the first real key overwriting the seed's "env" placeholder), otherwise a new
// row is inserted. The batch is atomic so a multi-Component provider (ElevenLabs
// → stt + tts share one key, ADR-0004) lands all-or-nothing. Returns the
// resulting rows in input order. Requires the UNIQUE (tenant_id, component,
// provider) key from migration 00005.
func (s *Store) UpsertProviderConfigs(ctx context.Context, configs []NewProviderConfig) ([]ProviderConfig, error) {
	out := make([]ProviderConfig, 0, len(configs))
	err := s.InTx(ctx, func(tx *Store) error {
		for _, c := range configs {
			row := tx.db.QueryRow(ctx,
				`INSERT INTO provider_config
				   (tenant_id, component, provider, model, credentials_ciphertext, credentials_last4)
				 VALUES ($1, $2, $3, $4, $5, $6)
				 ON CONFLICT (tenant_id, component, provider) DO UPDATE
				   SET model = EXCLUDED.model,
				       credentials_ciphertext = EXCLUDED.credentials_ciphertext,
				       credentials_last4 = EXCLUDED.credentials_last4,
				       updated_at = now()
				 RETURNING `+providerConfigColumns,
				c.TenantID, c.Component, c.Provider, c.Model,
				c.CredentialsCiphertext, c.CredentialsLast4)
			pc, err := scanProviderConfig(row)
			if err != nil {
				return fmt.Errorf("storage: upsert provider_config (%s/%s): %w", c.Component, c.Provider, err)
			}
			out = append(out, pc)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// NewAgent is the input to CreateAgent. Voice is the opaque JSONB blob the voice
// domain serializes its tts.Voice into; storage keeps it vendor-neutral.
// SpeakerColor is NOT an input — it is server-assigned on Character insert.
type NewAgent struct {
	CampaignID            uuid.UUID
	Role                  AgentRole
	Name                  string
	Title                 string
	Persona               string
	Voice                 []byte // JSON; defaults to {} when nil
	VoiceProviderConfigID uuid.NullUUID
	LLMProviderConfigID   uuid.NullUUID
	AddressOnly           bool
	Aliases               []string
}

// CreateAgent inserts an Agent and returns its generated ID. Inserting a second
// Butler in a Campaign violates the partial-unique index (ADR-0009). A Character
// is assigned the next round-robin speaker-colour slot for its Campaign (stable
// once stored); the Butler keeps slot 0.
func (s *Store) CreateAgent(ctx context.Context, a NewAgent) (uuid.UUID, error) {
	voice := a.Voice
	if len(voice) == 0 {
		voice = []byte(`{}`)
	}
	aliases := a.Aliases
	if aliases == nil {
		aliases = []string{}
	}
	var id uuid.UUID
	// $2 is cast to agent_role in BOTH uses so Postgres deduces one consistent
	// type for the parameter. The speaker_color slot is the count of existing
	// Characters in the Campaign (the new row's 0-based roster index) modulo the
	// palette size; non-Characters take slot 0.
	err := s.db.QueryRow(ctx,
		`INSERT INTO agents
		   (campaign_id, agent_role, name, title, persona, voice,
		    voice_provider_config_id, llm_provider_config_id, address_only, aliases,
		    speaker_color)
		 VALUES ($1, $2::agent_role, $3, $4, $5, $6, $7, $8, $9, $10,
		   CASE WHEN $2::agent_role = 'character'
		        THEN ((SELECT count(*) FROM agents
		                WHERE campaign_id = $1 AND agent_role = 'character') % $11)::smallint
		        ELSE 0 END)
		 RETURNING id`,
		a.CampaignID, a.Role, a.Name, a.Title, a.Persona, voice,
		a.VoiceProviderConfigID, a.LLMProviderConfigID, a.AddressOnly, aliases,
		speakerColorSlots).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("storage: create agent: %w", err)
	}
	return id, nil
}

// AgentUpdate is the input to UpdateAgent. It carries the editor-editable fields
// only — the Campaign screen edits name/title/persona/voice/address-only/aliases;
// agent_role and speaker_color are immutable here. CampaignID is the owning
// Campaign the write is scoped to (#342): the UPDATE matches (id, campaign_id), so
// an Agent in another Campaign is invisible and yields ErrNotFound — cross-campaign
// mutation is refused, and an Agent never moves between Campaigns. A Butler's
// address_only is force-kept true regardless of AddressOnly (ADR-0009 / ADR-0024).
type AgentUpdate struct {
	ID                    uuid.UUID
	CampaignID            uuid.UUID
	Name                  string
	Title                 string
	Persona               string
	Voice                 []byte // JSON; defaults to {} when nil
	VoiceProviderConfigID uuid.NullUUID
	LLMProviderConfigID   uuid.NullUUID
	AddressOnly           bool
	Aliases               []string
}

// UpdateAgent updates an Agent's editor fields and returns the updated row.
// It never changes agent_role, and it force-keeps a Butler's address_only true
// (the Butler always waits to be named, ADR-0024) — so editing the Butler can
// neither demote it nor turn off Address-Only. A missing id yields ErrNotFound.
func (s *Store) UpdateAgent(ctx context.Context, a AgentUpdate) (Agent, error) {
	voice := a.Voice
	if len(voice) == 0 {
		voice = []byte(`{}`)
	}
	aliases := a.Aliases
	if aliases == nil {
		aliases = []string{}
	}
	row := s.db.QueryRow(ctx,
		`UPDATE agents SET
		    name = $2,
		    title = $3,
		    persona = $4,
		    voice = $5,
		    voice_provider_config_id = $6,
		    llm_provider_config_id = $7,
		    address_only = CASE WHEN agent_role = 'butler' THEN true ELSE $8 END,
		    aliases = $9,
		    updated_at = now()
		  WHERE id = $1 AND campaign_id = $10
		 RETURNING `+agentColumns,
		a.ID, a.Name, a.Title, a.Persona, voice,
		a.VoiceProviderConfigID, a.LLMProviderConfigID, a.AddressOnly, aliases,
		a.CampaignID)
	updated, err := scanAgent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("storage: update agent %s: %w", a.ID, err)
	}
	return updated, nil
}

// DeleteAgent removes a Character NPC by id, scoped to its owning Campaign (#342):
// every clause matches (id, campaign_id), so an Agent in another Campaign is
// invisible — it neither exists nor deletes — and yields ErrNotFound, refusing a
// cross-campaign delete. Deleting a Butler is rejected with ErrButlerUndeletable
// (ADR-0009): the guarded DELETE leaves a Butler row untouched, and the wrapping
// CTE reports whether the id existed in the Campaign and whether it was a Butler in
// one atomic round-trip — so a missing id yields ErrNotFound and a Butler yields
// ErrButlerUndeletable, distinct from "deleted nothing".
func (s *Store) DeleteAgent(ctx context.Context, campaignID, id uuid.UUID) error {
	var existed, isButler bool
	err := s.db.QueryRow(ctx,
		`WITH found AS (
		     SELECT agent_role FROM agents WHERE id = $1 AND campaign_id = $2
		 ), del AS (
		     DELETE FROM agents WHERE id = $1 AND campaign_id = $2 AND agent_role <> 'butler' RETURNING 1
		 )
		 SELECT
		     EXISTS (SELECT 1 FROM found),
		     COALESCE((SELECT agent_role = 'butler' FROM found), false)`,
		id, campaignID).Scan(&existed, &isButler)
	if err != nil {
		return fmt.Errorf("storage: delete agent %s: %w", id, err)
	}
	if !existed {
		return ErrNotFound
	}
	if isButler {
		return ErrButlerUndeletable
	}
	return nil
}

// CharacterAgents returns the Campaign's Character NPC Agents (agent_role =
// 'character'), excluding the auto-created Butler. The live voice slice has
// exactly one (the seeded NPC); #6's web app lists many.
func (s *Store) CharacterAgents(ctx context.Context, campaignID uuid.UUID) ([]Agent, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+agentColumns+`
		   FROM agents
		  WHERE campaign_id = $1 AND agent_role = 'character'
		  ORDER BY name`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list character agents for campaign %s: %w", campaignID, err)
	}
	defer rows.Close()

	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan character agent: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list character agents for campaign %s: %w", campaignID, err)
	}
	return out, nil
}

// FindTenantByName returns the Tenant with the given name, or ErrNotFound. Used
// by the idempotent seed to detect an already-seeded database.
func (s *Store) FindTenantByName(ctx context.Context, name string) (Tenant, error) {
	var t Tenant
	err := s.db.QueryRow(ctx,
		`SELECT id, name, created_at, updated_at FROM tenant WHERE name = $1`, name).
		Scan(&t.ID, &t.Name, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tenant{}, ErrNotFound
	}
	if err != nil {
		return Tenant{}, fmt.Errorf("storage: find tenant %q: %w", name, err)
	}
	return t, nil
}

// FindCampaignByName returns the Tenant's Campaign with the given name, or
// ErrNotFound. It returns the full row rather than just the ID so a caller
// wiring a Voice Session gets the Campaign Language (the matcher's phonetic
// scheme, #199) from the same lookup.
func (s *Store) FindCampaignByName(ctx context.Context, tenantID uuid.UUID, name string) (Campaign, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+campaignColumns+`
		   FROM campaign WHERE tenant_id = $1 AND name = $2`, tenantID, name)
	c, err := scanCampaign(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	if err != nil {
		return Campaign{}, fmt.Errorf("storage: find campaign %q: %w", name, err)
	}
	return c, nil
}
