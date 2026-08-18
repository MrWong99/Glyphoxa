-- +goose Up
-- Sound enrichment for Session Highlights (#312, Epic 8, ADR-0004 amendment
-- 2026-07-22): the GM's opt-in "Add sound" action generates a matching audio
-- asset via ElevenLabs — a short sound-effect Sting or a composed Music track —
-- and attaches it as a SEPARATE blob next to the clip (zero DSP; the web UI
-- layers it client-side). Unlike the automatic image enrichment (#311) this is
-- deliberately NOT a new provider Component: the generation rides the Tenant's
-- existing `tts` Provider Config iff its provider is ElevenLabs, so there is no
-- provider_component enum change here.
--
-- sound_kind records the GM's standing choice ('sting' or 'music'; '' = none
-- requested) — it is what the boot reconciliation sweep and the UI's
-- "generating…" state key off, and re-running the action rewrites it.
-- sound_requested_at stamps the latest request so the web tier can bound its
-- await-media polling (the promoted_at/image pattern). sound_key/-content_type/
-- -size_bytes mirror the image triad: sound_key reconstructs the blob.Key
-- (t/<tenant>/highlight/<id>/sound); deletion goes through blob.Delete, never a
-- DB cascade. An empty sound_key with a non-empty sound_kind means "requested
-- but not landed" (generating, unconfigured, or failed — the row stays intact
-- without media). sound_enrich_claimed_at is the #406-pattern generation claim;
-- it never reaches the wire.
ALTER TABLE highlight ADD COLUMN sound_kind text NOT NULL DEFAULT '' CHECK (sound_kind IN ('', 'sting', 'music'));
ALTER TABLE highlight ADD COLUMN sound_requested_at timestamptz;
ALTER TABLE highlight ADD COLUMN sound_key text NOT NULL DEFAULT '';
ALTER TABLE highlight ADD COLUMN sound_content_type text NOT NULL DEFAULT '';
ALTER TABLE highlight ADD COLUMN sound_size_bytes bigint NOT NULL DEFAULT 0;
ALTER TABLE highlight ADD COLUMN sound_enrich_claimed_at timestamptz;

-- +goose Down
ALTER TABLE highlight DROP COLUMN IF EXISTS sound_enrich_claimed_at;
ALTER TABLE highlight DROP COLUMN IF EXISTS sound_size_bytes;
ALTER TABLE highlight DROP COLUMN IF EXISTS sound_content_type;
ALTER TABLE highlight DROP COLUMN IF EXISTS sound_key;
ALTER TABLE highlight DROP COLUMN IF EXISTS sound_requested_at;
ALTER TABLE highlight DROP COLUMN IF EXISTS sound_kind;
