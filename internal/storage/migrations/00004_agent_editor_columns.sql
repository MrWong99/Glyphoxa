-- +goose Up

-- Agent editor columns (#71, ADR-0009 / ADR-0039): the Campaign screen edits an
-- Agent's role subtitle and renders each speaker in a stable colour. Both columns
-- are ADDITIVE — existing rows take the defaults, the auto-Butler trigger
-- (00002) and the one-Butler partial unique index (00001) are untouched.
--
--   title         — the role subtitle the editor shows under an Agent's name
--                   (e.g. "Gruff innkeeper"); free text, defaults to ''.
--   speaker_color — a server-assigned palette SLOT (not a colour value): a small
--                   integer the web tier maps onto its speaker palette so each
--                   roster member renders in a stable hue across reloads. Slots
--                   are assigned round-robin per Campaign on Character insert
--                   (see CreateAgent); the Butler keeps slot 0 (it renders in its
--                   own reserved "Butler gold", ADR-0009).
ALTER TABLE agents ADD COLUMN title         text     NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN speaker_color smallint NOT NULL DEFAULT 0;

-- Backfill: give pre-existing Character NPCs distinct, stable slots in roster
-- order so an already-populated Campaign reads back with the same per-Agent hues
-- new inserts get. The palette has 6 slots (matches the web speaker palette);
-- the modulo wraps rosters larger than the palette. The Butler is excluded — it
-- keeps slot 0.
UPDATE agents a
   SET speaker_color = n.slot
  FROM (
        SELECT id,
               (row_number() OVER (PARTITION BY campaign_id
                                       ORDER BY created_at, id) - 1) % 6 AS slot
          FROM agents
         WHERE agent_role = 'character'
       ) n
 WHERE a.id = n.id;

-- +goose Down

ALTER TABLE agents DROP COLUMN speaker_color;
ALTER TABLE agents DROP COLUMN title;
