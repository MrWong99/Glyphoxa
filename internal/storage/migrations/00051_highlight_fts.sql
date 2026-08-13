-- +goose Up

-- Highlight keyword search for the Ctrl+K campaign palette (#591). A generated
-- tsvector over excerpt + reason: the excerpt (the caption-worthy transcript
-- text of the moment) is what a GM remembers, so it outweighs the classifier's
-- one-line reason — the same A/B weighting kg_node (00011) gives name over body.
-- The 'simple' config mirrors kg_node (00011), transcript_chunk (00001) and
-- transcript_line (00015) — language-agnostic, right for mixed German/English
-- tables. This is an ALTER on the existing table (ADR-0031: a migration never
-- rewrites 00024).
ALTER TABLE highlight ADD COLUMN fts tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', excerpt), 'A') ||
    setweight(to_tsvector('simple', reason), 'B')) STORED;

CREATE INDEX highlight_fts_idx ON highlight USING gin (fts);

-- +goose Down

DROP INDEX IF EXISTS highlight_fts_idx;
ALTER TABLE highlight DROP COLUMN IF EXISTS fts;
