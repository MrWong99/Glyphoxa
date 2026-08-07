-- +goose Up

-- Per-session voice-channel selection: the Session screen picks which voice
-- channel a start joins, so the chosen channel must cross the claim plane from
-- the web tier to the claiming Voice Instance (ADR-0057 (b)). '' means "no
-- explicit pick" — the worker falls back to the guild's Default Voice Channel
-- (deployment_config.voice_channel_id), matching the ''-as-unconfigured
-- sentinel both deployment_config ID columns already use.
ALTER TABLE voice_session_intents
    ADD COLUMN voice_channel_id text NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE voice_session_intents
    DROP COLUMN voice_channel_id;
