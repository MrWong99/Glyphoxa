-- +goose Up
-- Every statement here takes a lock that conflicts with writes on a table the
-- live voice loop inserts into. On a live database a long-running transaction
-- on either table would make this migration queue behind it and, worse, make
-- every later writer queue behind the migration. Give up after five seconds
-- instead (the migration fails and is simply re-run); the index builds
-- themselves are short: the partial indexes cover a handful of rows.
SET LOCAL lock_timeout = '5s';

-- The embedding backlog queue (ADR-0011): the backfill worker lists the oldest
-- rows `WHERE embedding IS NULL ORDER BY created_at, id` every pass, and the
-- backlog gauges count the same rows. Without an index both were sequential
-- scans of the whole table (and of its TOASTed vectors' heap pages) every ten
-- seconds and after every chunk insert, growing with the deployment's entire
-- transcript history although the backlog itself is a handful of rows.
--
-- A partial index over exactly the backlog serves the ordered LIMIT read as an
-- index scan and the count as an index-only scan. It holds only the rows still
-- waiting, so it stays tiny and a row leaves it the moment it is embedded.
CREATE INDEX transcript_chunk_unembedded_idx
    ON transcript_chunk (created_at, id)
    WHERE embedding IS NULL;

CREATE INDEX kg_node_unembedded_idx
    ON kg_node (created_at, id)
    WHERE embedding IS NULL;

-- The global HNSW indexes are no longer read (ADR-0011 amendment 2026-11-20):
-- every similarity search is scoped to one Campaign and now scans that
-- Campaign's rows exactly, because a campaign filter applied after a global
-- approximate scan returns nothing once other Campaigns crowd the query. An
-- unread HNSW index still costs a graph insert on every embedding write, so
-- they go.
DROP INDEX IF EXISTS transcript_chunk_embedding_hnsw_idx;
DROP INDEX IF EXISTS kg_node_embedding_hnsw_idx;

-- +goose Down
SET LOCAL lock_timeout = '5s';

CREATE INDEX transcript_chunk_embedding_hnsw_idx
    ON transcript_chunk USING hnsw (embedding vector_cosine_ops)
    WHERE embedding IS NOT NULL;

CREATE INDEX kg_node_embedding_hnsw_idx
    ON kg_node USING hnsw (embedding vector_cosine_ops)
    WHERE embedding IS NOT NULL;

DROP INDEX IF EXISTS kg_node_unembedded_idx;
DROP INDEX IF EXISTS transcript_chunk_unembedded_idx;
