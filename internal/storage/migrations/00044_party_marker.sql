-- +goose Up

-- The Party Marker (#540, ADR-0060 / ADR-0059 note): where the party currently
-- is, per VOICE SESSION.
--
-- Session-scoped, not campaign-scoped, for the same reason every voice event is
-- (ADR-0057): the Voice Instance pool is shared, so two concurrent sessions must
-- never see each other's position. A campaign-level column would be exactly that
-- leak.
--
-- current_pin_id points at a Pin when the party is AT a known place; current_x/y
-- carry a free position when they are between pins (crossing the moor, mid-ocean).
-- Both are nullable and independent of each other; current_map_id NULL means no
-- marker is set at all, which is the state every session starts in.
ALTER TABLE voice_sessions ADD COLUMN current_map_id uuid NULL
    REFERENCES campaign_map (id) ON DELETE SET NULL;
ALTER TABLE voice_sessions ADD COLUMN current_pin_id uuid NULL
    REFERENCES map_pin (id) ON DELETE SET NULL;
ALTER TABLE voice_sessions ADD COLUMN current_x double precision NULL;
ALTER TABLE voice_sessions ADD COLUMN current_y double precision NULL;

-- ON DELETE SET NULL on both: deleting the Map or unpinning the place the party
-- is standing in must not delete the session's history, and a marker pointing at
-- nothing degrades to "no marker" rather than to a dangling reference.

-- +goose Down

ALTER TABLE voice_sessions DROP COLUMN IF EXISTS current_y;
ALTER TABLE voice_sessions DROP COLUMN IF EXISTS current_x;
ALTER TABLE voice_sessions DROP COLUMN IF EXISTS current_pin_id;
ALTER TABLE voice_sessions DROP COLUMN IF EXISTS current_map_id;
