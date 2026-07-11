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
	// AgentID is the optional NPC-Node ↔ Character NPC Agent link (#132, ADR-0008
	// amendment): the "voiced by" cast Agent. Only an NPC Node may carry it (DB
	// CHECK); NULL when the Node is wiki-only or not an NPC.
	AgentID   uuid.NullUUID
	CreatedAt time.Time
	UpdatedAt time.Time
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
	id, campaign_id, node_type, name, body, gm_private, agent_id, created_at, updated_at`

func scanKGNode(row pgx.Row) (KGNode, error) {
	var n KGNode
	err := row.Scan(
		&n.ID, &n.CampaignID, &n.Type, &n.Name, &n.Body, &n.GMPrivate, &n.AgentID,
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
// (#129). It carries no Type (node_type is immutable, ADR-0008). CampaignID is the
// owning Campaign the write is scoped to (#342): the UPDATE matches (id,
// campaign_id), so a Node in another Campaign is invisible and yields ErrNotFound —
// a Node never moves between campaigns, and cross-campaign mutation is refused.
type KGNodeUpdate struct {
	ID         uuid.UUID
	CampaignID uuid.UUID
	Name       string
	Body       string
	GMPrivate  bool
}

// UpdateNode saves a Knowledge Graph Node's editor fields (name/body/gm_private)
// and returns the updated row, stamping updated_at = now(). node_type is never
// touched (immutable, ADR-0008). The write is scoped to (id, campaign_id) (#342),
// so a Node in another Campaign matches no row and yields ErrNotFound — a
// cross-campaign mutation is refused server-side. A missing id yields ErrNotFound.
func (s *Store) UpdateNode(ctx context.Context, u KGNodeUpdate) (KGNode, error) {
	// A wiki edit invalidates any existing embedding: reset it to NULL and clear
	// the model stamp so the row re-enters the embedworker backfill queue (#300,
	// ADR-0011) and future similarity hints reflect the new text.
	row := s.db.QueryRow(ctx,
		`UPDATE kg_node SET
		    name = $2,
		    body = $3,
		    gm_private = $4,
		    embedding = NULL,
		    embedding_model = '',
		    updated_at = now()
		  WHERE id = $1 AND campaign_id = $5
		 RETURNING `+kgNodeColumns,
		u.ID, u.Name, u.Body, u.GMPrivate, u.CampaignID)
	updated, err := scanKGNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return KGNode{}, ErrNotFound
	}
	if err != nil {
		return KGNode{}, fmt.Errorf("storage: update kg node %s: %w", u.ID, err)
	}
	return updated, nil
}

// DeleteNode removes a Knowledge Graph Node by id, scoped to its owning Campaign
// (#342): the DELETE matches (id, campaign_id), so a Node in another Campaign is
// not deleted and yields ErrNotFound — a cross-campaign delete is refused. A
// missing id likewise yields ErrNotFound so the RPC can distinguish "gone" from
// "never existed".
func (s *Store) DeleteNode(ctx context.Context, campaignID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM kg_node WHERE id = $1 AND campaign_id = $2`, id, campaignID)
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

// kgNodeColumnsN is kgNodeColumns aliased for the neighbourhood join, which reads
// Node columns from `kg_node n` joined to a ranked CTE — the bare list would be
// ambiguous.
const kgNodeColumnsN = `
	n.id, n.campaign_id, n.node_type, n.name, n.body, n.gm_private, n.agent_id, n.created_at, n.updated_at`

// AgentNodeFacts returns the edge-aware Hot Context fact set for a Character NPC
// Agent (#133, ADR-0008 amendment): the Agent's own linked Node plus its
// edge-adjacent Nodes (a single hop in BOTH edge directions), gm-public only,
// newest-first within hop, capped — one round trip inside the kgfacts budget.
//
// Semantics: traversal STARTS from the linked Node regardless of its gm_private,
// but gm_private filters SURFACING — the own Node and any neighbour that is
// gm_private is walked (its edges still expand) yet never returned, so a GM-only
// fact never reaches the prompt. An Agent with no linked Node yields an empty set
// (no campaign-wide fallback: the NPC injects only its own neighbourhood). The
// UNION dedupes multi-edge neighbours; min(hop) keeps the own Node at hop 0 even
// if an Edge also makes it a neighbour of itself's neighbour.
func (s *Store) AgentNodeFacts(ctx context.Context, agentID uuid.UUID) ([]KGNode, error) {
	rows, err := s.db.Query(ctx,
		`WITH own AS (
		     SELECT id FROM kg_node WHERE agent_id = $1
		 ),
		 hood AS (
		     SELECT id, 0 AS hop FROM own
		     UNION SELECT e.to_node_id,   1 FROM kg_edge e JOIN own o ON e.from_node_id = o.id
		     UNION SELECT e.from_node_id, 1 FROM kg_edge e JOIN own o ON e.to_node_id   = o.id
		 ),
		 ranked AS (
		     SELECT id, min(hop) AS hop FROM hood GROUP BY id
		 )
		 SELECT `+kgNodeColumnsN+`
		   FROM kg_node n
		   JOIN ranked r ON r.id = n.id
		  WHERE NOT n.gm_private
		  ORDER BY r.hop, n.updated_at DESC, n.id
		  LIMIT $2`, agentID, kgFactsCap)
	if err != nil {
		return nil, fmt.Errorf("storage: agent node facts for agent %s: %w", agentID, err)
	}
	defer rows.Close()

	var out []KGNode
	for rows.Next() {
		n, err := scanKGNode(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan agent node fact: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: agent node facts for agent %s: %w", agentID, err)
	}
	return out, nil
}

// SimilarNodes returns the k Knowledge Graph Nodes in campaignID nearest the
// query vector by cosine distance (<=>), nearest first — the ADR-0011 similarity
// hint the GM review surface shows beside a Knowledge Proposal (#300, ADR-0052).
// NULL-embedding rows are excluded (the partial HNSW index). Unlike the
// prompt-facing searches this INCLUDES gm_private Nodes: it is GM-facing review
// only, so the exclusion that guards NPC prompts does not apply here. NEVER reuse
// this for NPC prompt assembly — it would leak GM secrets. k <= 0 is a caller bug
// and errors. The query vector reuses encodeVector + a server-side ::vector cast,
// so storage carries no pgvector-go dependency.
func (s *Store) SimilarNodes(ctx context.Context, campaignID uuid.UUID, query []float32, k int) ([]KGNode, error) {
	if k <= 0 {
		return nil, fmt.Errorf("storage: similar nodes: k must be > 0, got %d", k)
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+kgNodeColumns+`
		   FROM kg_node
		  WHERE campaign_id = $1 AND embedding IS NOT NULL
		  ORDER BY embedding <=> $2::vector
		  LIMIT $3`, campaignID, encodeVector(query), k)
	if err != nil {
		return nil, fmt.Errorf("storage: similar kg nodes for campaign %s: %w", campaignID, err)
	}
	defer rows.Close()

	var out []KGNode
	for rows.Next() {
		n, err := scanKGNode(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan similar kg node: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: similar kg nodes for campaign %s: %w", campaignID, err)
	}
	return out, nil
}

// ListUnembeddedNodes returns up to limit Knowledge Graph Nodes still awaiting an
// embedding (embedding IS NULL), oldest first — the node half of the embedworker
// backfill queue (#300, ADR-0011), mirroring ListUnembeddedChunks. An empty
// result means the backlog is drained. The name+body is embedded by the worker;
// both columns are selected via kgNodeColumns.
func (s *Store) ListUnembeddedNodes(ctx context.Context, limit int) ([]KGNode, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+kgNodeColumns+`
		   FROM kg_node
		  WHERE embedding IS NULL
		  ORDER BY created_at, id
		  LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: list unembedded kg nodes: %w", err)
	}
	defer rows.Close()

	var out []KGNode
	for rows.Next() {
		n, err := scanKGNode(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan unembedded kg node: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list unembedded kg nodes: %w", err)
	}
	return out, nil
}

// SetNodeEmbedding fills one Node's embedding vector and stamps the model that
// produced it (#300, ADR-0011), mirroring SetChunkEmbedding. The vector is passed
// as pgvector's text form and cast server-side (::vector) so storage carries no
// pgvector-go dependency; the column is vector(768), so a wrong-length vector is
// rejected by Postgres. Once set, the row leaves the NULL-embedding backlog and
// becomes returnable by SimilarNodes.
func (s *Store) SetNodeEmbedding(ctx context.Context, id uuid.UUID, vec []float32, model string) error {
	if _, err := s.db.Exec(ctx,
		`UPDATE kg_node
		    SET embedding = $2::vector, embedding_model = $3
		  WHERE id = $1`, id, encodeVector(vec), model); err != nil {
		return fmt.Errorf("storage: set kg node embedding %s: %w", id, err)
	}
	return nil
}

// CountUnembeddedNodes returns the number of Knowledge Graph Nodes still awaiting
// an embedding (embedding IS NULL) — the node embedding-backlog gauge value (#300,
// ADR-0032), mirroring CountUnembeddedChunks. Process-wide (no campaign filter) to
// keep the metric's cardinality bounded.
func (s *Store) CountUnembeddedNodes(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM kg_node WHERE embedding IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("storage: count unembedded kg nodes: %w", err)
	}
	return n, nil
}
