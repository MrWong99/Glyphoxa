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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a query matches no row.
var ErrNotFound = errors.New("storage: not found")

// querier is the subset of the pgx API the Store needs; both *pgxpool.Pool and
// pgx.Tx satisfy it, so a Store can run against a pool directly or inside a
// transaction (see [Store.InTx]).
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Store reads and writes the core tables over a pgx connection pool (or a
// transaction, when created by [Store.InTx]).
type Store struct {
	db   querier
	pool *pgxpool.Pool // nil for a tx-bound Store; used only to begin transactions
}

// New wraps a pgx pool in a Store. The caller owns the pool's lifecycle.
func New(pool *pgxpool.Pool) *Store {
	return &Store{db: pool, pool: pool}
}

// InTx runs fn against a Store bound to a single transaction, committing if fn
// returns nil and rolling back otherwise. Used by multi-row operations that must
// be atomic (e.g. the live-NPC seed).
//
// On a Store ALREADY bound to a transaction (created by an enclosing InTx), it
// FLATTENS: fn runs against the same tx-bound Store, with no nested Begin and no
// savepoint (#291). This lets a method that uses InTx internally (e.g.
// CreateEdge) compose inside a larger import transaction. The caveat is that the
// inner call's atomicity then becomes the OUTER transaction's: an error raised
// after such an inner "commit" still rolls the whole outer tx back — there is no
// independent inner rollback boundary. That is exactly what the bundle importer
// wants (one all-or-nothing import), but a caller relying on partial-commit
// semantics from a nested InTx would be surprised.
func (s *Store) InTx(ctx context.Context, fn func(*Store) error) error {
	if s.pool == nil {
		// Already tx-bound: run in the ambient transaction (flatten). No commit or
		// rollback here — the outermost InTx owns the transaction boundary.
		return fn(s)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("storage: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op
	if err := fn(&Store{db: tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("storage: commit tx: %w", err)
	}
	return nil
}

const campaignColumns = `
	id, tenant_id, gm_member_id, name, system, language,
	created_at, updated_at, archived_at`

func scanCampaign(row pgx.Row) (Campaign, error) {
	var c Campaign
	err := row.Scan(
		&c.ID, &c.TenantID, &c.GMMemberID, &c.Name, &c.System, &c.Language,
		&c.CreatedAt, &c.UpdatedAt, &c.ArchivedAt,
	)
	return c, err
}

// GetActiveCampaign returns the "active" campaign: the most-recently-created
// one. Glyphoxa is single-operator today (one Tenant), so the global latest is
// the sole Tenant's latest. Tenant scoping fills in behind the X-Tenant-Id
// pass-through later (ADR-0039), at which point this gains a WHERE tenant_id = $1.
// Archived campaigns are excluded from this fallback (#269): an only-archived DB
// resolves to ErrNotFound, so an archived campaign can never be the implicit
// Active Campaign nor start a Voice Session. No campaign yields ErrNotFound (the
// RPC layer maps it to Connect CodeNotFound).
func (s *Store) GetActiveCampaign(ctx context.Context) (Campaign, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+campaignColumns+`
		   FROM campaign
		  WHERE archived_at IS NULL
		  ORDER BY created_at DESC, id DESC
		  LIMIT 1`)
	c, err := scanCampaign(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	if err != nil {
		return Campaign{}, fmt.Errorf("storage: get active campaign: %w", err)
	}
	return c, nil
}

// GetCampaign loads one Campaign by id, or ErrNotFound. It backs the /glyphoxa
// use resolution step that turns a live Voice Session's campaign_id back into the
// full Campaign (#108).
func (s *Store) GetCampaign(ctx context.Context, id uuid.UUID) (Campaign, error) {
	row := s.db.QueryRow(ctx, `SELECT `+campaignColumns+` FROM campaign WHERE id = $1`, id)
	c, err := scanCampaign(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	if err != nil {
		return Campaign{}, fmt.Errorf("storage: get campaign %s: %w", id, err)
	}
	return c, nil
}

// ListCampaigns returns every ACTIVE Campaign ordered by name (then id for a
// stable tie-break) — the /glyphoxa use autocomplete source (#108). Archived
// campaigns are excluded (#269): the autocomplete inherits that filter with no
// code change of its own. Single-operator today, so it is unscoped, mirroring
// GetActiveCampaign; tenant scoping fills in behind the X-Tenant-Id pass-through
// later (ADR-0039). See ListAllCampaigns for the archive-inclusive read.
func (s *Store) ListCampaigns(ctx context.Context) ([]Campaign, error) {
	rows, err := s.db.Query(ctx, `SELECT `+campaignColumns+` FROM campaign WHERE archived_at IS NULL ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("storage: list campaigns: %w", err)
	}
	defer rows.Close()
	var out []Campaign
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan campaign: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list campaigns: %w", err)
	}
	return out, nil
}

const agentColumns = `
	id, campaign_id, agent_role, name, title, persona, voice,
	voice_provider_config_id, llm_provider_config_id,
	address_only, speaker_color, aliases, created_at, updated_at`

func scanAgent(row pgx.Row) (Agent, error) {
	var a Agent
	err := row.Scan(
		&a.ID, &a.CampaignID, &a.Role, &a.Name, &a.Title, &a.Persona, &a.Voice,
		&a.VoiceProviderConfigID, &a.LLMProviderConfigID,
		&a.AddressOnly, &a.SpeakerColor, &a.Aliases, &a.CreatedAt, &a.UpdatedAt,
	)
	return a, err
}

// GetAgent loads one Agent by id.
func (s *Store) GetAgent(ctx context.Context, id uuid.UUID) (Agent, error) {
	row := s.db.QueryRow(ctx,
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
	row := s.db.QueryRow(ctx,
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
	rows, err := s.db.Query(ctx,
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
	row := s.db.QueryRow(ctx,
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

// ListProviderConfigs returns all of a Tenant's Provider Configs, ordered by
// Component then Provider (deterministic for the Configuration screen, #68). An
// empty result is not an error.
func (s *Store) ListProviderConfigs(ctx context.Context, tenantID uuid.UUID) ([]ProviderConfig, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+providerConfigColumns+`
		   FROM provider_config
		  WHERE tenant_id = $1
		  ORDER BY component, provider`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("storage: list provider_config for tenant %s: %w", tenantID, err)
	}
	defer rows.Close()

	var out []ProviderConfig
	for rows.Next() {
		p, err := scanProviderConfig(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan provider_config: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list provider_config for tenant %s: %w", tenantID, err)
	}
	return out, nil
}

// GetProviderConfigByComponent returns the Tenant's most-recently-updated
// Provider Config for a Component, or ErrNotFound when none is bound. A Component
// can have more than one Provider in the matrix (ADR-0004); this resolves the one
// the operator last saved.
func (s *Store) GetProviderConfigByComponent(ctx context.Context, tenantID uuid.UUID, component Component) (ProviderConfig, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+providerConfigColumns+`
		   FROM provider_config
		  WHERE tenant_id = $1 AND component = $2
		  ORDER BY updated_at DESC, id DESC
		  LIMIT 1`, tenantID, component)
	p, err := scanProviderConfig(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProviderConfig{}, ErrNotFound
	}
	if err != nil {
		return ProviderConfig{}, fmt.Errorf("storage: get provider_config (tenant %s, component %s): %w", tenantID, component, err)
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
