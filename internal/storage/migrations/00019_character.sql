-- +goose Up

-- character is the Player Character (PC) table (#276, E4). A Character is played
-- by exactly one Discord User; discord_user_id is MANDATORY (ADR-0003: Players
-- are not Tenant Members — they are scoped via the Characters they play, and
-- Address Detection / transcript attribution only need the Discord User ID).
-- linked_user_id is the NULLABLE dormant link set on first Discord OAuth, turning
-- a Player into a Linked Player with web access scoped to their Characters
-- (ADR-0003); it stays NULL until then. aliases feed Address Detection like an
-- Agent's aliases. Rows CASCADE with their Campaign.
--
-- The UNIQUE (campaign_id, discord_user_id) index is the decision that one
-- Discord User plays at most one Character per Campaign, so rebinding a Character
-- to a different Discord User is an UPDATE of this column, never a second row.
CREATE TABLE character (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id     uuid NOT NULL REFERENCES campaign (id) ON DELETE CASCADE,
    name            text NOT NULL,
    aliases         text[] NOT NULL DEFAULT '{}',
    discord_user_id text NOT NULL,
    linked_user_id  text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX character_campaign_discord_user_idx
    ON character (campaign_id, discord_user_id);

-- +goose Down

DROP TABLE IF EXISTS character;
