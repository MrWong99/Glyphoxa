-- +goose Up

-- User-facing transcript search at the Line grain (#120, ADR-0011 amendment
-- 2026-07-04). A generated tsvector over the rendered line text backs the web
-- search box + `/glyphoxa search`; a line hit carries an exact speaker/timestamp
-- and deep-links to the rendered line, which a chunk hit cannot. The 'simple'
-- config mirrors kg_node (00011) and transcript_chunk (00001) — language-agnostic,
-- right for mixed German/English tables. This is an ALTER on the existing table
-- (ADR-0031: a migration never rewrites 00007). The chunk grain's own fts column
-- (00001) stays reserved for the later embedding-augmented overlay; this is the
-- user-facing path.
ALTER TABLE transcript_line ADD COLUMN fts tsvector GENERATED ALWAYS AS (
    to_tsvector('simple', text)) STORED;

CREATE INDEX transcript_line_fts_idx ON transcript_line USING gin (fts);

-- +goose Down

DROP INDEX IF EXISTS transcript_line_fts_idx;
ALTER TABLE transcript_line DROP COLUMN IF EXISTS fts;
