// Package storage is the persistence layer for Glyphoxa: goose-driven schema
// migrations (ADR-0031) plus a thin pgx query layer over the core tables.
//
// Query layer choice: pgx/v5 directly, not sqlc. The read surface this task
// needs is small (load an Agent + its Persona/Voice + bound Provider Configs),
// so hand-written queries are clearer than adding a codegen step to CI. goose
// needs a database/sql handle (see migrate.go); the app uses a *pgxpool.Pool.
package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a query matches no row.
var ErrNotFound = errors.New("storage: not found")

// Store reads and writes the core tables over a pgx connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// New wraps a pgx pool in a Store. The caller owns the pool's lifecycle.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

const agentColumns = `
	id, campaign_id, agent_role, name, persona, voice,
	voice_provider_config_id, llm_provider_config_id,
	address_only, aliases, created_at, updated_at`

func scanAgent(row pgx.Row) (Agent, error) {
	var a Agent
	err := row.Scan(
		&a.ID, &a.CampaignID, &a.Role, &a.Name, &a.Persona, &a.Voice,
		&a.VoiceProviderConfigID, &a.LLMProviderConfigID,
		&a.AddressOnly, &a.Aliases, &a.CreatedAt, &a.UpdatedAt,
	)
	return a, err
}

// GetAgent loads one Agent by id.
func (s *Store) GetAgent(ctx context.Context, id uuid.UUID) (Agent, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+agentColumns+` FROM agents WHERE id = $1`, id)
	a, err := scanAgent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("storage: get agent %s: %w", id, err)
	}
	return a, nil
}

// GetButler loads a Campaign's Butler (exactly one per Campaign, ADR-0009).
func (s *Store) GetButler(ctx context.Context, campaignID uuid.UUID) (Agent, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+agentColumns+`
		   FROM agents
		  WHERE campaign_id = $1 AND agent_role = 'butler'`, campaignID)
	a, err := scanAgent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("storage: get butler for campaign %s: %w", campaignID, err)
	}
	return a, nil
}

// ListAgents returns all Agents in a Campaign (Butler + Character NPCs).
func (s *Store) ListAgents(ctx context.Context, campaignID uuid.UUID) ([]Agent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+agentColumns+`
		   FROM agents
		  WHERE campaign_id = $1
		  ORDER BY agent_role, name`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list agents for campaign %s: %w", campaignID, err)
	}
	defer rows.Close()

	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan agent: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list agents for campaign %s: %w", campaignID, err)
	}
	return out, nil
}

const providerConfigColumns = `
	id, tenant_id, component, provider, model,
	credentials_ciphertext, credentials_last4, created_at, updated_at`

func scanProviderConfig(row pgx.Row) (ProviderConfig, error) {
	var p ProviderConfig
	err := row.Scan(
		&p.ID, &p.TenantID, &p.Component, &p.Provider, &p.Model,
		&p.CredentialsCiphertext, &p.CredentialsLast4, &p.CreatedAt, &p.UpdatedAt,
	)
	return p, err
}

// GetProviderConfig loads one Provider Config by id.
func (s *Store) GetProviderConfig(ctx context.Context, id uuid.UUID) (ProviderConfig, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+providerConfigColumns+` FROM provider_config WHERE id = $1`, id)
	p, err := scanProviderConfig(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProviderConfig{}, ErrNotFound
	}
	if err != nil {
		return ProviderConfig{}, fmt.Errorf("storage: get provider_config %s: %w", id, err)
	}
	return p, nil
}

// LoadAgent loads an Agent together with its bound LLM and TTS Provider Configs
// — the Persona/Voice/provider bundle the orchestrator needs (the core read
// this task exists to enable). Missing or unbound configs yield nil, not an
// error; only the Agent itself must exist.
func (s *Store) LoadAgent(ctx context.Context, id uuid.UUID) (LoadedAgent, error) {
	agent, err := s.GetAgent(ctx, id)
	if err != nil {
		return LoadedAgent{}, err
	}
	loaded := LoadedAgent{Agent: agent}

	if agent.LLMProviderConfigID.Valid {
		cfg, err := s.GetProviderConfig(ctx, agent.LLMProviderConfigID.UUID)
		switch {
		case errors.Is(err, ErrNotFound):
			// Config row gone (e.g. SET NULL race); treat as unbound.
		case err != nil:
			return LoadedAgent{}, err
		default:
			loaded.LLMConfig = &cfg
		}
	}

	if agent.VoiceProviderConfigID.Valid {
		cfg, err := s.GetProviderConfig(ctx, agent.VoiceProviderConfigID.UUID)
		switch {
		case errors.Is(err, ErrNotFound):
		case err != nil:
			return LoadedAgent{}, err
		default:
			loaded.TTSConfig = &cfg
		}
	}
	return loaded, nil
}
