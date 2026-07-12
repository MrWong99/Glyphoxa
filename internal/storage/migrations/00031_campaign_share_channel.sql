-- +goose Up
-- Discord highlight delivery (#310, Epic 8, ADR-0051 GM-only sharing posture):
-- when the GM shares a promoted Highlight as a file to a Discord text channel, the
-- chosen channel is remembered PER CAMPAIGN so the next share pre-selects it (the
-- decision comment's "last choice remembered per campaign — no separate config
-- surface"). It is a single scalar on the campaign, NOT a new config table: the
-- share dialog lists the guild's live text channels each time and this column only
-- seeds the default selection.
--
-- Empty string means "never shared to a channel yet" — the dialog then pre-selects
-- nothing and the GM picks. A channel id is a Discord snowflake (plain text, like
-- deployment_config.voice_channel_id); there is deliberately NO FK (Discord owns
-- the channel, not this DB) — a stale id just fails the next PostFile with a
-- readable Discord 4xx.
ALTER TABLE campaign ADD COLUMN highlight_share_channel_id text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE campaign DROP COLUMN IF EXISTS highlight_share_channel_id;
