-- +goose Up
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

-- +goose Down
DROP INDEX IF EXISTS kg_node_unembedded_idx;
DROP INDEX IF EXISTS transcript_chunk_unembedded_idx;
