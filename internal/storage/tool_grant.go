package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Tool Grant persistence (ADR-0029, #113): the per-Agent rows the live loop
// hydrates into a GrantSet. The methods are deliberately thin — list-by-agent
// for hydration, plus create/delete for the seed and the future grant-mutation
// RPC (#117). The grant Config is untyped jsonb; the layer keeps it a raw blob
// (the Tool handler is the only place scope is interpreted), so nothing here
// depends on pkg/tool.

const toolGrantColumns = `
	id, agent_id, tool_name, surface, config, created_at, updated_at`

func scanToolGrant(row pgx.Row) (ToolGrant, error) {
	var g ToolGrant
	err := row.Scan(
		&g.ID, &g.AgentID, &g.ToolName, &g.Surface, &g.Config, &g.CreatedAt, &g.UpdatedAt,
	)
	return g, err
}

// ListToolGrants returns an Agent's VOICE Tool Grants ordered by tool_name (a
// stable order so a hydrated GrantSet — and thus the ADR-0021 prompt_hash —
// does not thrash between runs). An Agent with no grants yields an empty
// slice, not an error: least-privilege means such an Agent is shown no Tool at
// all. Voice-only is the pre-0062 meaning every existing caller (wirenpc, the
// grant editor RPC, the bundle) relies on; the chat surface reads its disjoint
// rows via [Store.ListToolGrantsFor].
func (s *Store) ListToolGrants(ctx context.Context, agentID uuid.UUID) ([]ToolGrant, error) {
	return s.ListToolGrantsFor(ctx, agentID, GrantSurfaceVoice)
}

// ListToolGrantsFor returns an Agent's Tool Grants for one surface (#592,
// ADR-0062), ordered by tool_name. The surfaces are disjoint row sets, so a
// chat hydration can never arm a voice-granted tool and vice versa.
func (s *Store) ListToolGrantsFor(ctx context.Context, agentID uuid.UUID, surface GrantSurface) ([]ToolGrant, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+toolGrantColumns+`
		   FROM tool_agent_grant
		  WHERE agent_id = $1 AND surface = $2
		  ORDER BY tool_name`, agentID, surface)
	if err != nil {
		return nil, fmt.Errorf("storage: list tool grants for agent %s: %w", agentID, err)
	}
	defer rows.Close()

	var out []ToolGrant
	for rows.Next() {
		g, err := scanToolGrant(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan tool grant: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list tool grants for agent %s: %w", agentID, err)
	}
	return out, nil
}

// NewToolGrant is the input to CreateToolGrant. Config is the optional per-grant
// scope blob (jsonb); a nil/empty Config persists SQL NULL — "no narrowing"
// (dice). Surface picks the grant's Agent surface (#592, ADR-0062); the zero
// value means [GrantSurfaceVoice], the pre-0062 meaning of every caller.
type NewToolGrant struct {
	AgentID  uuid.UUID
	ToolName string
	Surface  GrantSurface
	Config   json.RawMessage
}

// surfaceOrVoice defaults an unset Surface to voice, so pre-0062 callers keep
// their meaning without naming a surface.
func surfaceOrVoice(s GrantSurface) GrantSurface {
	if s == "" {
		return GrantSurfaceVoice
	}
	return s
}

// CreateToolGrant inserts a Tool Grant and returns its generated ID. An empty
// Config is stored as SQL NULL. A duplicate (agent_id, tool_name, surface)
// violates the UNIQUE index — an Agent grants a Tool at most once per surface
// (ADR-0029, ADR-0062).
func (s *Store) CreateToolGrant(ctx context.Context, g NewToolGrant) (uuid.UUID, error) {
	// A nil interface encodes to SQL NULL; a non-empty blob goes in as jsonb
	// (pgx maps []byte ↔ jsonb, as agents.voice already relies on).
	var config any
	if len(g.Config) > 0 {
		config = []byte(g.Config)
	}
	var id uuid.UUID
	err := s.db.QueryRow(ctx,
		`INSERT INTO tool_agent_grant (agent_id, tool_name, surface, config)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		g.AgentID, g.ToolName, surfaceOrVoice(g.Surface), config).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("storage: create tool grant (%s/%s): %w", g.AgentID, g.ToolName, err)
	}
	return id, nil
}

// UpsertToolGrant grants a Tool to an Agent, or edits an existing grant's scope
// Config in place — the #117 mutation path. It INSERTs the (agent_id, tool_name)
// row when absent and UPDATEs its Config when present, keyed off the
// UNIQUE(agent_id, tool_name) index, so "grant on" and "edit scope" are one call
// and a repeated grant never trips the unique constraint. An empty Config stores
// SQL NULL (no narrowing — dice's shape), so re-upserting nil clears a prior
// scope. The Agent's next Voice Session hydrates the resulting row (#113).
func (s *Store) UpsertToolGrant(ctx context.Context, g NewToolGrant) error {
	// A nil interface encodes to SQL NULL; a non-empty blob goes in as jsonb.
	var config any
	if len(g.Config) > 0 {
		config = []byte(g.Config)
	}
	_, err := s.db.Exec(ctx,
		`INSERT INTO tool_agent_grant (agent_id, tool_name, surface, config)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (agent_id, tool_name, surface)
		 DO UPDATE SET config = EXCLUDED.config, updated_at = now()`,
		g.AgentID, g.ToolName, surfaceOrVoice(g.Surface), config)
	if err != nil {
		return fmt.Errorf("storage: upsert tool grant (%s/%s): %w", g.AgentID, g.ToolName, err)
	}
	return nil
}

// DeleteToolGrant removes an Agent's VOICE grant of the named Tool. Deleting a
// grant that is not present yields ErrNotFound (so the caller can tell
// "removed" from "was never there"). Removing the row is how a GM revokes a
// Tool: after hydration the Agent's GrantSet no longer carries it, so the LLM
// is never shown the Tool and cannot call it. Voice-only keeps the pre-0062
// meaning for every caller (the grant editor RPC, the bundle importer); chat
// rows have no mutation surface in v1 — they are the trigger-seeded defaults.
func (s *Store) DeleteToolGrant(ctx context.Context, agentID uuid.UUID, toolName string) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM tool_agent_grant
		  WHERE agent_id = $1 AND tool_name = $2 AND surface = 'voice'`,
		agentID, toolName)
	if err != nil {
		return fmt.Errorf("storage: delete tool grant (%s/%s): %w", agentID, toolName, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
