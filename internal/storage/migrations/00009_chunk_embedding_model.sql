-- +goose Up

-- The async embedding worker (#116, ADR-0011) stamps WHICH model produced a
-- chunk's vector when it fills the embedding. That provenance matters because
-- switching the embedding model (or Matryoshka dimensions) changes the vector
-- space and requires a backfill — a mixed-model column is how a re-embed pass
-- knows which rows are stale. Additive with a '' default so the migration is
-- safe over existing rows (the chunk writer inserts with embedding NULL, so the
-- default empty model is correct until the worker embeds the row).
ALTER TABLE transcript_chunk
    ADD COLUMN embedding_model text NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE transcript_chunk
    DROP COLUMN IF EXISTS embedding_model;
