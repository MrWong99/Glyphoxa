// Package postgres provides a PostgreSQL-backed implementation of the three-layer
// Glyphoxa memory architecture (L1 session log, L2 semantic index, L3 knowledge graph).
//
// All three layers share a single [pgxpool.Pool] connection pool. The pgvector
// extension must be available in the target database; [Migrate] installs it
// automatically via CREATE EXTENSION IF NOT EXISTS.
//
// Usage:
//
//	schema, _ := postgres.NewSchemaName("public")
//	store, err := postgres.NewStore(ctx, dsn, 1536, schema, "my_campaign")
//	if err != nil { … }
//
//	// L1
//	_ = store.WriteEntry(ctx, sessionID, entry)
//
//	// L2
//	_ = store.IndexChunk(ctx, chunk)
//
//	// L3
//	_ = store.AddEntity(ctx, entity)
//
//	// GraphRAG
//	results, _ := store.QueryWithContext(ctx, "who is the blacksmith's ally?", scope)
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ddlSessionEntries returns the L1 DDL with schema-qualified table names.
func ddlSessionEntries(s SchemaName) string {
	t := s.TableRef("session_entries")
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
    id           BIGSERIAL    PRIMARY KEY,
    campaign_id  TEXT         NOT NULL DEFAULT 'migrated',
    session_id   TEXT         NOT NULL,
    speaker_id   TEXT         NOT NULL DEFAULT '',
    speaker_name TEXT         NOT NULL DEFAULT '',
    text         TEXT         NOT NULL,
    raw_text     TEXT         NOT NULL DEFAULT '',
    npc_id       TEXT         NOT NULL DEFAULT '',
    timestamp    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    duration_ns  BIGINT       NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_session_entries_session_id
    ON %s (session_id);

CREATE INDEX IF NOT EXISTS idx_session_entries_timestamp
    ON %s (timestamp);

CREATE INDEX IF NOT EXISTS idx_session_entries_session_timestamp
    ON %s (session_id, timestamp);

CREATE INDEX IF NOT EXISTS idx_session_entries_campaign
    ON %s (campaign_id);

CREATE INDEX IF NOT EXISTS idx_session_entries_fts
    ON %s USING GIN (to_tsvector('english', text));
`, t, t, t, t, t, t)
}

// ddlKnowledgeGraph returns the L3 DDL with schema-qualified table names.
// The entities table uses a composite primary key (campaign_id, id) so that
// two campaigns can have entities with the same name without collision.
func ddlKnowledgeGraph(s SchemaName) string {
	entities := s.TableRef("entities")
	relationships := s.TableRef("relationships")
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
    campaign_id TEXT         NOT NULL DEFAULT 'migrated',
    id          TEXT         NOT NULL,
    type        TEXT         NOT NULL,
    name        TEXT         NOT NULL,
    attributes  JSONB        NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (campaign_id, id)
);

CREATE INDEX IF NOT EXISTS idx_entities_type ON %s (type);
CREATE INDEX IF NOT EXISTS idx_entities_name ON %s (name);

CREATE TABLE IF NOT EXISTS %s (
    campaign_id TEXT         NOT NULL DEFAULT 'migrated',
    source_id   TEXT         NOT NULL,
    target_id   TEXT         NOT NULL,
    rel_type    TEXT         NOT NULL,
    attributes  JSONB        NOT NULL DEFAULT '{}',
    provenance  JSONB        NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (campaign_id, source_id, target_id, rel_type),
    FOREIGN KEY (campaign_id, source_id) REFERENCES %s (campaign_id, id) ON DELETE CASCADE,
    FOREIGN KEY (campaign_id, target_id) REFERENCES %s (campaign_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_rel_source
    ON %s (source_id);

CREATE INDEX IF NOT EXISTS idx_rel_target
    ON %s (target_id);

CREATE INDEX IF NOT EXISTS idx_rel_type
    ON %s (rel_type);

CREATE INDEX IF NOT EXISTS idx_rel_provenance_confidence
    ON %s ((provenance->>'confidence'));
`, entities, entities, entities,
		relationships, entities, entities,
		relationships, relationships, relationships, relationships)
}

// ddlL2 returns the L2 DDL with the embedding dimension substituted and
// schema-qualified table names.
func ddlL2(s SchemaName, embeddingDimensions int) string {
	t := s.TableRef("chunks")
	return fmt.Sprintf(`
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS %s (
    id          TEXT         PRIMARY KEY,
    campaign_id TEXT         NOT NULL DEFAULT 'migrated',
    session_id  TEXT         NOT NULL,
    content     TEXT         NOT NULL,
    embedding   vector(%d),
    speaker_id  TEXT         NOT NULL DEFAULT '',
    entity_id   TEXT         NOT NULL DEFAULT '',
    topic       TEXT         NOT NULL DEFAULT '',
    timestamp   TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_chunks_session_id
    ON %s (session_id);

CREATE INDEX IF NOT EXISTS idx_chunks_campaign
    ON %s (campaign_id);

CREATE INDEX IF NOT EXISTS idx_chunks_embedding
    ON %s USING hnsw (embedding vector_cosine_ops);

CREATE INDEX IF NOT EXISTS idx_chunks_fts
    ON %s USING GIN (to_tsvector('english', content));
`, t, embeddingDimensions, t, t, t, t)
}

// Migrate creates or ensures all required database tables and extensions exist
// within the given schema. It is idempotent (CREATE TABLE IF NOT EXISTS /
// CREATE INDEX IF NOT EXISTS) and safe to call on every application start.
//
// embeddingDimensions must match the vector model configured for your deployment
// (e.g., 1536 for OpenAI text-embedding-3-small, 768 for nomic-embed-text).
// Changing this value after the first migration requires a manual schema update.
func Migrate(ctx context.Context, pool *pgxpool.Pool, embeddingDimensions int, schema SchemaName) error {
	// Ensure the schema exists (no-op for "public").
	if schema.String() != "public" {
		createSchema := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %q", schema.String())
		if _, err := pool.Exec(ctx, createSchema); err != nil {
			return fmt.Errorf("postgres migrate: create schema: %w", err)
		}
	}

	statements := []string{
		ddlSessionEntries(schema),
		ddlL2(schema, embeddingDimensions),
		ddlKnowledgeGraph(schema),
	}

	for _, stmt := range statements {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("postgres migrate: %w", err)
		}
	}
	return nil
}
