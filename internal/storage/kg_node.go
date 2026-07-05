package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Knowledge Graph Node persistence (#126, ADR-0008 v1.0): the structured wiki /
// GM notes for a Campaign. This slice ships the full 7-value node_type enum and
// the create/list reads; typed Edges (#132) and fulltext search (#131) layer on
// the same table without a migration.

// KGNodeType is a Knowledge Graph Node's type (CONTEXT.md "Node", ADR-0008). It
// mirrors the kg_node_type Postgres enum; the value is immutable after create.
type KGNodeType string

const (
	KGNodeCharacter  KGNodeType = "character"
	KGNodeNPC        KGNodeType = "npc"
	KGNodeLocation   KGNodeType = "location"
	KGNodeFaction    KGNodeType = "faction"
	KGNodeItem       KGNodeType = "item"
	KGNodePlotThread KGNodeType = "plot_thread"
	KGNodeNote       KGNodeType = "note"
)

// KGNode is one persisted Knowledge Graph Node in a Campaign. GMPrivate hides the
// Node from any NPC's Hot Context (#126); Body is the Node's prose (empty by
// default).
type KGNode struct {
	ID         uuid.UUID
	CampaignID uuid.UUID
	Type       KGNodeType
	Name       string
	Body       string
	GMPrivate  bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// NewKGNode is the input to CreateNode. node_type is set once at insert and never
// updated (ADR-0008: type is immutable).
type NewKGNode struct {
	CampaignID uuid.UUID
	Type       KGNodeType
	Name       string
	Body       string
	GMPrivate  bool
}

const kgNodeColumns = `
	id, campaign_id, node_type, name, body, gm_private, created_at, updated_at`

func scanKGNode(row pgx.Row) (KGNode, error) {
	var n KGNode
	err := row.Scan(
		&n.ID, &n.CampaignID, &n.Type, &n.Name, &n.Body, &n.GMPrivate,
		&n.CreatedAt, &n.UpdatedAt,
	)
	return n, err
}

// CreateNode inserts a Knowledge Graph Node and returns the persisted row. The
// node_type is cast server-side to the kg_node_type enum, so an out-of-enum type
// is rejected by Postgres rather than silently stored.
func (s *Store) CreateNode(ctx context.Context, n NewKGNode) (KGNode, error) {
	row := s.db.QueryRow(ctx,
		`INSERT INTO kg_node (campaign_id, node_type, name, body, gm_private)
		 VALUES ($1, $2::kg_node_type, $3, $4, $5)
		 RETURNING `+kgNodeColumns,
		n.CampaignID, n.Type, n.Name, n.Body, n.GMPrivate)
	created, err := scanKGNode(row)
	if err != nil {
		return KGNode{}, fmt.Errorf("storage: create kg node: %w", err)
	}
	return created, nil
}

// KGNodeUpdate is the input to UpdateNode — the Knowledge panel's editor fields
// (#129). It carries no Type (node_type is immutable, ADR-0008) and no CampaignID
// (a Node never moves between campaigns); the row is addressed by ID alone.
type KGNodeUpdate struct {
	ID        uuid.UUID
	Name      string
	Body      string
	GMPrivate bool
}

// UpdateNode saves a Knowledge Graph Node's editor fields (name/body/gm_private)
// and returns the updated row, stamping updated_at = now(). node_type is never
// touched (immutable, ADR-0008). A missing id yields ErrNotFound.
func (s *Store) UpdateNode(ctx context.Context, u KGNodeUpdate) (KGNode, error) {
	row := s.db.QueryRow(ctx,
		`UPDATE kg_node SET
		    name = $2,
		    body = $3,
		    gm_private = $4,
		    updated_at = now()
		  WHERE id = $1
		 RETURNING `+kgNodeColumns,
		u.ID, u.Name, u.Body, u.GMPrivate)
	updated, err := scanKGNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return KGNode{}, ErrNotFound
	}
	if err != nil {
		return KGNode{}, fmt.Errorf("storage: update kg node %s: %w", u.ID, err)
	}
	return updated, nil
}

// DeleteNode removes a Knowledge Graph Node by id. A missing id yields
// ErrNotFound so the RPC can distinguish "gone" from "never existed".
func (s *Store) DeleteNode(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM kg_node WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("storage: delete kg node %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListNodes returns every Knowledge Graph Node in a Campaign in a stable display
// order (node_type enum order, then case-insensitive name, then id) — the
// Knowledge panel's list. An empty result is not an error.
func (s *Store) ListNodes(ctx context.Context, campaignID uuid.UUID) ([]KGNode, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+kgNodeColumns+`
		   FROM kg_node
		  WHERE campaign_id = $1
		  ORDER BY node_type, lower(name), id`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list kg nodes for campaign %s: %w", campaignID, err)
	}
	defer rows.Close()

	var out []KGNode
	for rows.Next() {
		n, err := scanKGNode(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan kg node: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list kg nodes for campaign %s: %w", campaignID, err)
	}
	return out, nil
}

// kgFactsCap bounds the prompt-injection read so a large wiki can never blow the
// Hot Context budget at the SQL layer (#126 AC4); the kgfacts renderer applies
// the finer per-block/per-fact caps on top.
const kgFactsCap = 50

// ListPublicNodes returns the Campaign's gm-public Nodes ordered newest-first
// (updated_at DESC, id), capped — the Hot Context prompt-injection read (#126).
// gm_private Nodes are excluded so a GM-only fact never reaches an NPC's prompt.
func (s *Store) ListPublicNodes(ctx context.Context, campaignID uuid.UUID) ([]KGNode, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+kgNodeColumns+`
		   FROM kg_node
		  WHERE campaign_id = $1 AND NOT gm_private
		  ORDER BY updated_at DESC, id
		  LIMIT $2`, campaignID, kgFactsCap)
	if err != nil {
		return nil, fmt.Errorf("storage: list public kg nodes for campaign %s: %w", campaignID, err)
	}
	defer rows.Close()

	var out []KGNode
	for rows.Next() {
		n, err := scanKGNode(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan public kg node: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list public kg nodes for campaign %s: %w", campaignID, err)
	}
	return out, nil
}
