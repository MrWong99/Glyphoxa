-- +goose Up

-- Fulltext search over the Knowledge Graph (#131, ADR-0008 v1.0 "Fulltext search
-- (tsvector) only"). A generated tsvector weights the Node name over its body
-- (A > B), so a name hit outranks a body hit under ts_rank. The 'simple' config
-- mirrors transcript_chunk (00001_init.sql) — language-agnostic, right for mixed
-- German/English campaigns. This is an ALTER on the existing table (ADR-0031: a
-- migration never rewrites 00010).
ALTER TABLE kg_node ADD COLUMN fts tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', name), 'A') ||
    setweight(to_tsvector('simple', body), 'B')) STORED;

CREATE INDEX kg_node_fts_idx ON kg_node USING gin (fts);

-- +goose Down

DROP INDEX IF EXISTS kg_node_fts_idx;
ALTER TABLE kg_node DROP COLUMN IF EXISTS fts;
