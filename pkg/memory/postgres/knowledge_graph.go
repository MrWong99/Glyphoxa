package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/MrWong99/glyphoxa/pkg/memory"
)

// ─────────────────────────────────────────────────────────────────────────────
// L3 — KnowledgeGraph + GraphRAGQuerier
// ─────────────────────────────────────────────────────────────────────────────

// AddEntity implements [memory.KnowledgeGraph]. It upserts an entity into the
// entities table. If an entity with the same (campaign_id, id) already exists
// it is completely replaced and its updated_at timestamp is refreshed.
func (s *Store) AddEntity(ctx context.Context, entity memory.Entity) error {
	attrsJSON, err := json.Marshal(entity.Attributes)
	if err != nil {
		return fmt.Errorf("knowledge graph: marshal attributes: %w", err)
	}

	q := fmt.Sprintf(`
		INSERT INTO %s (campaign_id, id, type, name, attributes, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, now(), now())
		ON CONFLICT (campaign_id, id) DO UPDATE SET
		    type        = EXCLUDED.type,
		    name        = EXCLUDED.name,
		    attributes  = EXCLUDED.attributes,
		    updated_at  = now()`,
		s.schema.TableRef("entities"))

	_, err = s.pool.Exec(ctx, q,
		s.campaignID,
		entity.ID,
		entity.Type,
		entity.Name,
		attrsJSON,
	)
	if err != nil {
		return fmt.Errorf("knowledge graph: add entity: %w", err)
	}
	return nil
}

// GetEntity implements [memory.KnowledgeGraph]. It retrieves an entity by ID.
// Returns (nil, nil) when the entity does not exist.
func (s *Store) GetEntity(ctx context.Context, id string) (*memory.Entity, error) {
	q := fmt.Sprintf(`
		SELECT id, type, name, attributes, created_at, updated_at
		FROM   %s
		WHERE  campaign_id = $1 AND id = $2`,
		s.schema.TableRef("entities"))

	rows, err := s.pool.Query(ctx, q, s.campaignID, id)
	if err != nil {
		return nil, fmt.Errorf("knowledge graph: get entity: %w", err)
	}
	entities, err := collectEntities(rows)
	if err != nil {
		return nil, fmt.Errorf("knowledge graph: get entity: %w", err)
	}
	if len(entities) == 0 {
		return nil, nil
	}
	return &entities[0], nil
}

// UpdateEntity implements [memory.KnowledgeGraph]. It merges attrs into the
// entity's Attributes map using PostgreSQL's jsonb || operator and refreshes
// updated_at. Returns an error when the entity does not exist.
func (s *Store) UpdateEntity(ctx context.Context, id string, attrs map[string]any) error {
	attrsJSON, err := json.Marshal(attrs)
	if err != nil {
		return fmt.Errorf("knowledge graph: marshal update attrs: %w", err)
	}

	q := fmt.Sprintf(`
		UPDATE %s
		SET    attributes = attributes || $3::jsonb,
		       updated_at = now()
		WHERE  campaign_id = $1 AND id = $2`,
		s.schema.TableRef("entities"))

	tag, err := s.pool.Exec(ctx, q, s.campaignID, id, attrsJSON)
	if err != nil {
		return fmt.Errorf("knowledge graph: update entity: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("knowledge graph: update entity: entity %q not found", id)
	}
	return nil
}

// DeleteEntity implements [memory.KnowledgeGraph]. It removes the entity and
// all its associated relationships (via ON DELETE CASCADE). Deleting a
// non-existent entity is not an error.
func (s *Store) DeleteEntity(ctx context.Context, id string) error {
	q := fmt.Sprintf(`DELETE FROM %s WHERE campaign_id = $1 AND id = $2`,
		s.schema.TableRef("entities"))
	if _, err := s.pool.Exec(ctx, q, s.campaignID, id); err != nil {
		return fmt.Errorf("knowledge graph: delete entity: %w", err)
	}
	return nil
}

// FindEntities implements [memory.KnowledgeGraph]. It returns all entities
// matching filter. All non-zero filter fields are applied as AND conditions.
func (s *Store) FindEntities(ctx context.Context, filter memory.EntityFilter) ([]memory.Entity, error) {
	args := []any{s.campaignID}
	next := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	conditions := []string{"campaign_id = $1"}
	if filter.Type != "" {
		conditions = append(conditions, "type = "+next(filter.Type))
	}
	if filter.Name != "" {
		conditions = append(conditions, "name ILIKE "+next("%"+filter.Name+"%"))
	}
	if len(filter.AttributeQuery) > 0 {
		attrJSON, err := json.Marshal(filter.AttributeQuery)
		if err != nil {
			return nil, fmt.Errorf("knowledge graph: marshal attribute query: %w", err)
		}
		conditions = append(conditions, "attributes @> "+next(string(attrJSON))+"::jsonb")
	}

	q := fmt.Sprintf("SELECT id, type, name, attributes, created_at, updated_at\nFROM   %s\nWHERE %s\nORDER BY name",
		s.schema.TableRef("entities"),
		strings.Join(conditions, "\n  AND "))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("knowledge graph: find entities: %w", err)
	}
	result, err := collectEntities(rows)
	if err != nil {
		return nil, fmt.Errorf("knowledge graph: find entities: %w", err)
	}
	return result, nil
}

// AddRelationship implements [memory.KnowledgeGraph]. It upserts a directed
// edge between two entities. If the edge (campaign_id, SourceID, TargetID,
// RelType) already exists it is completely replaced.
func (s *Store) AddRelationship(ctx context.Context, rel memory.Relationship) error {
	attrsJSON, err := json.Marshal(rel.Attributes)
	if err != nil {
		return fmt.Errorf("knowledge graph: marshal relationship attributes: %w", err)
	}
	provJSON, err := json.Marshal(rel.Provenance)
	if err != nil {
		return fmt.Errorf("knowledge graph: marshal relationship provenance: %w", err)
	}

	q := fmt.Sprintf(`
		INSERT INTO %s
		    (campaign_id, source_id, target_id, rel_type, attributes, provenance, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (campaign_id, source_id, target_id, rel_type) DO UPDATE SET
		    attributes = EXCLUDED.attributes,
		    provenance = EXCLUDED.provenance`,
		s.schema.TableRef("relationships"))

	_, err = s.pool.Exec(ctx, q,
		s.campaignID,
		rel.SourceID,
		rel.TargetID,
		rel.RelType,
		attrsJSON,
		provJSON,
	)
	if err != nil {
		return fmt.Errorf("knowledge graph: add relationship: %w", err)
	}
	return nil
}

// GetRelationships implements [memory.KnowledgeGraph]. It returns relationships
// associated with entityID. By default only outgoing edges are returned; use
// [memory.WithIncoming] to include inbound edges and [memory.WithRelTypes] to
// filter by edge type.
func (s *Store) GetRelationships(ctx context.Context, entityID string, opts ...memory.RelQueryOpt) ([]memory.Relationship, error) {
	params := memory.ApplyRelQueryOpts(opts)
	dirIn := params.DirectionIn
	dirOut := params.DirectionOut
	relTypes := params.RelTypes
	limit := params.Limit

	// Default: outgoing only when neither direction is explicitly set.
	if !dirIn && !dirOut {
		dirOut = true
	}

	args := []any{s.campaignID} // $1 = campaign_id
	next := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	// Build direction filter.
	var dirParts []string
	if dirOut {
		dirParts = append(dirParts, "source_id = "+next(entityID))
	}
	if dirIn {
		dirParts = append(dirParts, "target_id = "+next(entityID))
	}
	conditions := []string{
		"campaign_id = $1",
		"(" + strings.Join(dirParts, " OR ") + ")",
	}

	if len(relTypes) > 0 {
		conditions = append(conditions, "rel_type = ANY("+next(relTypes)+"::text[])")
	}

	q := fmt.Sprintf("SELECT source_id, target_id, rel_type, attributes, provenance, created_at\n"+
		"FROM   %s\n"+
		"WHERE  %s\n"+
		"ORDER  BY created_at",
		s.schema.TableRef("relationships"),
		strings.Join(conditions, "\n  AND "))

	if limit > 0 {
		args = append(args, limit)
		q += fmt.Sprintf("\nLIMIT $%d", len(args))
	}

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("knowledge graph: get relationships: %w", err)
	}
	result, err := collectRelationships(rows)
	if err != nil {
		return nil, fmt.Errorf("knowledge graph: get relationships: %w", err)
	}
	return result, nil
}

// DeleteRelationship implements [memory.KnowledgeGraph]. It removes the
// directed edge identified by (sourceID, targetID, relType). Deleting a
// non-existent edge is not an error.
func (s *Store) DeleteRelationship(ctx context.Context, sourceID, targetID, relType string) error {
	q := fmt.Sprintf(`
		DELETE FROM %s
		WHERE campaign_id = $1 AND source_id = $2 AND target_id = $3 AND rel_type = $4`,
		s.schema.TableRef("relationships"))

	if _, err := s.pool.Exec(ctx, q, s.campaignID, sourceID, targetID, relType); err != nil {
		return fmt.Errorf("knowledge graph: delete relationship: %w", err)
	}
	return nil
}

// Neighbors implements [memory.KnowledgeGraph]. It performs a bidirectional
// breadth-first traversal from entityID up to depth hops using a PostgreSQL
// recursive CTE and returns all reachable entities (the start entity is
// excluded).
func (s *Store) Neighbors(ctx context.Context, entityID string, depth int, opts ...memory.TraversalOpt) ([]memory.Entity, error) {
	tparams := memory.ApplyTraversalOpts(opts)
	relTypes := tparams.RelTypes
	nodeTypes := tparams.NodeTypes
	maxNodes := tparams.MaxNodes

	entitiesTable := s.schema.TableRef("entities")
	relsTable := s.schema.TableRef("relationships")

	var args []any
	next := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	campArg := next(s.campaignID) // $1
	startArg := next(entityID)    // $2
	depthArg := next(depth)       // $3

	relTypeFilter := ""
	if len(relTypes) > 0 {
		relTypeFilter = "\n           AND rel.rel_type = ANY(" + next(relTypes) + "::text[])"
	}

	nodeTypeFilter := ""
	if len(nodeTypes) > 0 {
		nodeTypeFilter = "\n           AND e.type = ANY(" + next(nodeTypes) + "::text[])"
	}

	q := fmt.Sprintf(`
		WITH RECURSIVE reachable AS (
		    SELECT id,
		           ARRAY[id] AS visited,
		           0          AS depth
		    FROM   %s
		    WHERE  campaign_id = %s AND id = %s

		    UNION ALL

		    SELECT e.id,
		           r.visited || e.id,
		           r.depth + 1
		    FROM   reachable r
		    JOIN   %s rel
		           ON rel.campaign_id = %s
		           AND (rel.source_id = r.id OR rel.target_id = r.id)
		    JOIN   %s e
		           ON e.campaign_id = %s
		           AND e.id = CASE
		                WHEN rel.source_id = r.id THEN rel.target_id
		                ELSE rel.source_id
		              END
		    WHERE  r.depth < %s
		      AND  NOT (e.id = ANY(r.visited))%s%s
		)
		SELECT DISTINCT ON (e.id)
		       e.id, e.type, e.name, e.attributes, e.created_at, e.updated_at
		FROM   reachable rc
		JOIN   %s  e  ON e.campaign_id = %s AND e.id = rc.id
		WHERE  rc.id != %s
		ORDER  BY e.id`,
		entitiesTable, campArg, startArg,
		relsTable, campArg,
		entitiesTable, campArg,
		depthArg, relTypeFilter, nodeTypeFilter,
		entitiesTable, campArg, startArg)

	if maxNodes > 0 {
		args = append(args, maxNodes)
		q += fmt.Sprintf("\nLIMIT $%d", len(args))
	}

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("knowledge graph: neighbors: %w", err)
	}
	result, err := collectEntities(rows)
	if err != nil {
		return nil, fmt.Errorf("knowledge graph: neighbors: %w", err)
	}
	return result, nil
}

// FindPath implements [memory.KnowledgeGraph]. It returns the shortest sequence
// of entities (including fromID and toID) connecting fromID to toID following
// edges in both directions (bidirectional), up to maxDepth hops.
//
// Returns an empty (non-nil) slice when no path exists within maxDepth.
func (s *Store) FindPath(ctx context.Context, fromID, toID string, maxDepth int) ([]memory.Entity, error) {
	entitiesTable := s.schema.TableRef("entities")
	relsTable := s.schema.TableRef("relationships")

	q := fmt.Sprintf(`
		WITH RECURSIVE path_search AS (
		    SELECT id,
		           ARRAY[id] AS path,
		           0          AS depth
		    FROM   %s
		    WHERE  campaign_id = $1 AND id = $2

		    UNION ALL

		    SELECT e.id,
		           ps.path || e.id,
		           ps.depth + 1
		    FROM   path_search ps
		    JOIN   %s rel
		           ON rel.campaign_id = $1
		           AND (rel.source_id = ps.id OR rel.target_id = ps.id)
		    JOIN   %s e
		           ON e.campaign_id = $1
		           AND e.id = CASE
		                WHEN rel.source_id = ps.id THEN rel.target_id
		                ELSE rel.source_id
		              END
		    WHERE  ps.depth < $4
		      AND  NOT (e.id = ANY(ps.path))
		)
		SELECT path
		FROM   path_search
		WHERE  id = $3
		ORDER  BY depth
		LIMIT  1`,
		entitiesTable, relsTable, entitiesTable)

	row := s.pool.QueryRow(ctx, q, s.campaignID, fromID, toID, maxDepth)

	var path []string
	if err := row.Scan(&path); err != nil {
		if isNoRows(err) {
			return []memory.Entity{}, nil
		}
		return nil, fmt.Errorf("knowledge graph: find path: %w", err)
	}

	return s.fetchEntitiesOrdered(ctx, path)
}

// VisibleSubgraph implements [memory.KnowledgeGraph]. It returns the NPC
// entity itself, all entities it has direct relationships with, and those
// relationships (both outgoing and incoming edges).
func (s *Store) VisibleSubgraph(ctx context.Context, npcID string) ([]memory.Entity, []memory.Relationship, error) {
	qRels := fmt.Sprintf(`
		SELECT source_id, target_id, rel_type, attributes, provenance, created_at
		FROM   %s
		WHERE  campaign_id = $1 AND (source_id = $2 OR target_id = $2)
		ORDER  BY created_at`,
		s.schema.TableRef("relationships"))

	rows, err := s.pool.Query(ctx, qRels, s.campaignID, npcID)
	if err != nil {
		return nil, nil, fmt.Errorf("knowledge graph: visible subgraph: query rels: %w", err)
	}
	rels, err := collectRelationships(rows)
	if err != nil {
		return nil, nil, fmt.Errorf("knowledge graph: visible subgraph: %w", err)
	}

	// Collect unique entity IDs (the NPC + all directly related entities).
	seen := map[string]struct{}{npcID: {}}
	ids := []string{npcID}
	for _, r := range rels {
		for _, rid := range []string{r.SourceID, r.TargetID} {
			if _, ok := seen[rid]; !ok {
				seen[rid] = struct{}{}
				ids = append(ids, rid)
			}
		}
	}

	entities, err := s.fetchEntitiesIn(ctx, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("knowledge graph: visible subgraph: %w", err)
	}
	return entities, rels, nil
}

// IdentitySnapshot implements [memory.KnowledgeGraph]. It assembles a compact
// [memory.NPCIdentity] for npcID containing the NPC's entity record, all its
// direct relationships, and the entities those relationships reference.
func (s *Store) IdentitySnapshot(ctx context.Context, npcID string) (*memory.NPCIdentity, error) {
	entity, err := s.GetEntity(ctx, npcID)
	if err != nil {
		return nil, fmt.Errorf("knowledge graph: identity snapshot: %w", err)
	}
	if entity == nil {
		return nil, fmt.Errorf("knowledge graph: identity snapshot: entity %q not found", npcID)
	}

	rels, err := s.GetRelationships(ctx, npcID, memory.WithOutgoing(), memory.WithIncoming())
	if err != nil {
		return nil, fmt.Errorf("knowledge graph: identity snapshot: %w", err)
	}

	// Collect all related entity IDs (exclude the NPC itself).
	seen := map[string]struct{}{npcID: {}}
	var relatedIDs []string
	for _, r := range rels {
		for _, rid := range []string{r.SourceID, r.TargetID} {
			if _, ok := seen[rid]; !ok {
				seen[rid] = struct{}{}
				relatedIDs = append(relatedIDs, rid)
			}
		}
	}

	var related []memory.Entity
	if len(relatedIDs) > 0 {
		related, err = s.fetchEntitiesIn(ctx, relatedIDs)
		if err != nil {
			return nil, fmt.Errorf("knowledge graph: identity snapshot: %w", err)
		}
	}
	if related == nil {
		related = []memory.Entity{}
	}

	return &memory.NPCIdentity{
		Entity:          *entity,
		Relationships:   rels,
		RelatedEntities: related,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GraphRAGQuerier
// ─────────────────────────────────────────────────────────────────────────────

// QueryWithContext implements [memory.GraphRAGQuerier]. It performs a
// graph-augmented retrieval query combining L3 entity data with L2 chunk text
// in a single SQL round-trip using PostgreSQL full-text search.
func (s *Store) QueryWithContext(ctx context.Context, query string, graphScope []string) ([]memory.ContextResult, error) {
	chunksTable := s.schema.TableRef("chunks")
	entitiesTable := s.schema.TableRef("entities")

	args := []any{s.campaignID, query} // $1 = campaign_id, $2 = FTS query
	next := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	scopeFilter := ""
	if len(graphScope) > 0 {
		scopeFilter = "\n  AND  c.entity_id = ANY(" + next(graphScope) + "::text[])"
	}

	q := fmt.Sprintf(`
		SELECT e.id, e.type, e.name, e.attributes, e.created_at, e.updated_at,
		       c.content,
		       ts_rank(to_tsvector('english', c.content),
		               plainto_tsquery('english', $2)) AS score
		FROM   %s  c
		JOIN   %s e ON e.campaign_id = $1 AND e.id = c.entity_id
		WHERE  c.campaign_id = $1
		  AND  to_tsvector('english', c.content) @@ plainto_tsquery('english', $2)%s
		ORDER  BY score DESC
		LIMIT  20`, chunksTable, entitiesTable, scopeFilter)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("knowledge graph: query with context: %w", err)
	}

	results, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (memory.ContextResult, error) {
		var (
			cr        memory.ContextResult
			attrsJSON []byte
		)
		if err := row.Scan(
			&cr.Entity.ID,
			&cr.Entity.Type,
			&cr.Entity.Name,
			&attrsJSON,
			&cr.Entity.CreatedAt,
			&cr.Entity.UpdatedAt,
			&cr.Content,
			&cr.Score,
		); err != nil {
			return memory.ContextResult{}, err
		}
		if err := json.Unmarshal(attrsJSON, &cr.Entity.Attributes); err != nil {
			return memory.ContextResult{}, fmt.Errorf("unmarshal entity attributes: %w", err)
		}
		return cr, nil
	})
	if err != nil {
		return nil, fmt.Errorf("knowledge graph: query with context: scan: %w", err)
	}
	if results == nil {
		results = []memory.ContextResult{}
	}
	return results, nil
}

// QueryWithEmbedding implements [memory.GraphRAGQuerier]. It performs a
// graph-augmented retrieval query using pgvector cosine similarity.
func (s *Store) QueryWithEmbedding(ctx context.Context, embedding []float32, topK int, graphScope []string) ([]memory.ContextResult, error) {
	chunksTable := s.schema.TableRef("chunks")
	entitiesTable := s.schema.TableRef("entities")

	queryVec := pgvector.NewVector(embedding)

	args := []any{queryVec, s.campaignID} // $1 = query vector, $2 = campaign_id
	next := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	scopeFilter := ""
	if len(graphScope) > 0 {
		scopeFilter = "\n  AND  c.entity_id = ANY(" + next(graphScope) + "::text[])"
	}

	args = append(args, topK)
	limitArg := fmt.Sprintf("$%d", len(args))

	q := fmt.Sprintf(`
		SELECT e.id, e.type, e.name, e.attributes, e.created_at, e.updated_at,
		       c.content,
		       c.embedding <=> $1 AS distance
		FROM   %s  c
		JOIN   %s e ON e.campaign_id = $2 AND e.id = c.entity_id
		WHERE  c.campaign_id = $2
		  AND  c.embedding IS NOT NULL%s
		ORDER  BY distance
		LIMIT  %s`, chunksTable, entitiesTable, scopeFilter, limitArg)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("knowledge graph: query with embedding: %w", err)
	}

	results, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (memory.ContextResult, error) {
		var (
			cr        memory.ContextResult
			attrsJSON []byte
			distance  float64
		)
		if err := row.Scan(
			&cr.Entity.ID,
			&cr.Entity.Type,
			&cr.Entity.Name,
			&attrsJSON,
			&cr.Entity.CreatedAt,
			&cr.Entity.UpdatedAt,
			&cr.Content,
			&distance,
		); err != nil {
			return memory.ContextResult{}, err
		}
		if err := json.Unmarshal(attrsJSON, &cr.Entity.Attributes); err != nil {
			return memory.ContextResult{}, fmt.Errorf("unmarshal entity attributes: %w", err)
		}
		// Convert distance (lower = better) to score (higher = better).
		cr.Score = 1.0 - distance
		return cr, nil
	})
	if err != nil {
		return nil, fmt.Errorf("knowledge graph: query with embedding: scan: %w", err)
	}
	if results == nil {
		results = []memory.ContextResult{}
	}
	return results, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Private scan helpers
// ─────────────────────────────────────────────────────────────────────────────

// collectEntities scans pgx rows into a slice of Entity values.
func collectEntities(rows pgx.Rows) ([]memory.Entity, error) {
	entities, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (memory.Entity, error) {
		var (
			e         memory.Entity
			attrsJSON []byte
		)
		if err := row.Scan(
			&e.ID,
			&e.Type,
			&e.Name,
			&attrsJSON,
			&e.CreatedAt,
			&e.UpdatedAt,
		); err != nil {
			return memory.Entity{}, err
		}
		if len(attrsJSON) > 0 {
			if err := json.Unmarshal(attrsJSON, &e.Attributes); err != nil {
				return memory.Entity{}, fmt.Errorf("unmarshal entity attributes: %w", err)
			}
		}
		if e.Attributes == nil {
			e.Attributes = map[string]any{}
		}
		return e, nil
	})
	if err != nil {
		return nil, err
	}
	if entities == nil {
		entities = []memory.Entity{}
	}
	return entities, nil
}

// collectRelationships scans pgx rows into a slice of Relationship values.
func collectRelationships(rows pgx.Rows) ([]memory.Relationship, error) {
	rels, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (memory.Relationship, error) {
		var (
			r         memory.Relationship
			attrsJSON []byte
			provJSON  []byte
		)
		if err := row.Scan(
			&r.SourceID,
			&r.TargetID,
			&r.RelType,
			&attrsJSON,
			&provJSON,
			&r.CreatedAt,
		); err != nil {
			return memory.Relationship{}, err
		}
		if len(attrsJSON) > 0 {
			if err := json.Unmarshal(attrsJSON, &r.Attributes); err != nil {
				return memory.Relationship{}, fmt.Errorf("unmarshal rel attributes: %w", err)
			}
		}
		if r.Attributes == nil {
			r.Attributes = map[string]any{}
		}
		if len(provJSON) > 0 {
			if err := json.Unmarshal(provJSON, &r.Provenance); err != nil {
				return memory.Relationship{}, fmt.Errorf("unmarshal rel provenance: %w", err)
			}
		}
		return r, nil
	})
	if err != nil {
		return nil, err
	}
	if rels == nil {
		rels = []memory.Relationship{}
	}
	return rels, nil
}

// fetchEntitiesIn returns entities whose IDs are in the provided list.
func (s *Store) fetchEntitiesIn(ctx context.Context, ids []string) ([]memory.Entity, error) {
	if len(ids) == 0 {
		return []memory.Entity{}, nil
	}
	q := fmt.Sprintf(`
		SELECT id, type, name, attributes, created_at, updated_at
		FROM   %s
		WHERE  campaign_id = $1 AND id = ANY($2::text[])`,
		s.schema.TableRef("entities"))

	rows, err := s.pool.Query(ctx, q, s.campaignID, ids)
	if err != nil {
		return nil, fmt.Errorf("fetch entities in: %w", err)
	}
	return collectEntities(rows)
}

// fetchEntitiesOrdered returns entities in the same order as the provided ids
// slice, fetching them in a single query and re-ordering in Go.
func (s *Store) fetchEntitiesOrdered(ctx context.Context, ids []string) ([]memory.Entity, error) {
	if len(ids) == 0 {
		return []memory.Entity{}, nil
	}
	entities, err := s.fetchEntitiesIn(ctx, ids)
	if err != nil {
		return nil, err
	}

	byID := make(map[string]memory.Entity, len(entities))
	for _, e := range entities {
		byID[e.ID] = e
	}

	ordered := make([]memory.Entity, 0, len(ids))
	for _, id := range ids {
		if e, ok := byID[id]; ok {
			ordered = append(ordered, e)
		}
	}
	return ordered, nil
}

// isNoRows reports whether err is the pgx "no rows" sentinel.
func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
