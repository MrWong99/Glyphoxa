-- +goose Up

-- Knowledge Graph Node embeddings (#300 PR-b, ADR-0011/0052): the GM review
-- surface surfaces embedding-similar existing Nodes beside each Knowledge
-- Proposal so the GM merges/rewrites/rejects rather than duplicating canon. This
-- mirrors the transcript_chunk async-embed pipeline (ADR-0011): the column is
-- added NULL, the embedworker backfills it, and the ANN search filters
-- embedding IS NOT NULL. An ALTER on the existing table (ADR-0031: never rewrite
-- 00010). embedding_model records which model produced the vector, so a
-- model-switch re-embed pass can key off provenance.
--
-- The partial HNSW index (vector_cosine_ops, embedding IS NOT NULL) serves the
-- nearest-neighbour search; still-NULL rows are simply invisible to it until the
-- backfill worker fills them. Existing rows are left NULL for the worker to embed.
ALTER TABLE kg_node ADD COLUMN embedding vector(768);
ALTER TABLE kg_node ADD COLUMN embedding_model text NOT NULL DEFAULT '';

CREATE INDEX kg_node_embedding_hnsw_idx
    ON kg_node USING hnsw (embedding vector_cosine_ops)
    WHERE embedding IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS kg_node_embedding_hnsw_idx;
ALTER TABLE kg_node DROP COLUMN IF EXISTS embedding;
ALTER TABLE kg_node DROP COLUMN IF EXISTS embedding_model;
