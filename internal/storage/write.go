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

// NewAgent is the input to CreateAgent. Voice is the opaque JSONB blob the voice
// domain serializes its tts.Voice into; storage keeps it vendor-neutral.
type NewAgent struct {
	CampaignID            uuid.UUID
	Role                  AgentRole
	Name                  string
	Persona               string
	Voice                 []byte // JSON; defaults to {} when nil
	VoiceProviderConfigID uuid.NullUUID
	LLMProviderConfigID   uuid.NullUUID
	AddressOnly           bool
	Aliases               []string
}

// CreateAgent inserts an Agent and returns its generated ID. Inserting a second
// Butler in a Campaign violates the partial-unique index (ADR-0009).
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
	err := s.db.QueryRow(ctx,
		`INSERT INTO agents
		   (campaign_id, agent_role, name, persona, voice,
		    voice_provider_config_id, llm_provider_config_id, address_only, aliases)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		a.CampaignID, a.Role, a.Name, a.Persona, voice,
		a.VoiceProviderConfigID, a.LLMProviderConfigID, a.AddressOnly, aliases).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("storage: create agent: %w", err)
	}
	return id, nil
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

// FindCampaignByName returns the ID of the Tenant's Campaign with the given
// name, or ErrNotFound.
func (s *Store) FindCampaignByName(ctx context.Context, tenantID uuid.UUID, name string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx,
		`SELECT id FROM campaign WHERE tenant_id = $1 AND name = $2`, tenantID, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("storage: find campaign %q: %w", name, err)
	}
	return id, nil
}
