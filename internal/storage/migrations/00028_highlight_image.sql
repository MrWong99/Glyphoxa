-- +goose Up
-- AI image enrichment for Session Highlights (#311, Epic 8, ADR-0004 amendment):
-- the `image` provider Component (Gemini) generates a dramatic scene for a
-- promoted Highlight, and the result lands on the row through the blob seam
-- (ADR-0048) exactly like the audio clip. image_key reconstructs the blob.Key;
-- there is deliberately NO FK to the blob (deletion goes through blob.Delete,
-- never a DB cascade). An empty image_key means "no image yet" — a promoted
-- Highlight whose enrichment has not run, is not configured, or failed (the row
-- stays intact without media, AC).
--
-- The provider_component enum gains 'image' so a Provider Config can bind the new
-- Component. ADD VALUE is NOT referenced in this migration (the columns are plain
-- text/bigint), so it is safe inside goose's transaction; a re-run is a no-op via
-- IF NOT EXISTS.
--
-- NOTE (merge order): 00027 belongs to #372 (recap tool, in flight); this
-- migration is numbered 00028 and merges AFTER it.
ALTER TYPE provider_component ADD VALUE IF NOT EXISTS 'image';

ALTER TABLE highlight ADD COLUMN image_key text NOT NULL DEFAULT '';
ALTER TABLE highlight ADD COLUMN image_content_type text NOT NULL DEFAULT '';
ALTER TABLE highlight ADD COLUMN image_size_bytes bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE highlight DROP COLUMN IF EXISTS image_size_bytes;
ALTER TABLE highlight DROP COLUMN IF EXISTS image_content_type;
ALTER TABLE highlight DROP COLUMN IF EXISTS image_key;
-- The 'image' enum value cannot be dropped: Postgres has no ALTER TYPE ... DROP
-- VALUE. Leaving it is harmless (nothing references it after the columns go), so
-- the Down deliberately does not attempt it.
