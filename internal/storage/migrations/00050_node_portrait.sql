-- +goose Up

-- Node portraits (#590): one portrait image per Knowledge Graph Node, following
-- the campaign_map pattern — the image lives in the blob seam (ADR-0048), never
-- in the row; the row carries only the key. '' means "no portrait" (the same
-- posture as a Map restored from an imageless bundle), so the column needs no
-- NULL state. Deletion goes through the seam, not an FK cascade: the row's
-- disappearance is not what frees the bytes.
ALTER TABLE kg_node ADD COLUMN portrait_blob_key text NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE kg_node DROP COLUMN portrait_blob_key;
