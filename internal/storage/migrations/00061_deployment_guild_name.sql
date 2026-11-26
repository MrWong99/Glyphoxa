-- +goose Up
-- The linked Discord server's display name (ADR-0004 amendment 2026-11-26):
-- Setup names the server a table is linked to instead of showing a bare
-- snowflake. The guild-admin proof already reads the guild (GET /guilds/{id})
-- when a server is linked, so the name is stored with the link, kept while the
-- same server is re-linked, and cleared when the link changes or is released.
-- NULL means "not known": no server linked, or a link made before this column
-- existed (the screen then falls back to the id). It is display data only —
-- nothing routes on it, and a rename on Discord shows after the next re-link.
ALTER TABLE deployment_config ADD COLUMN guild_name text;

-- +goose Down
ALTER TABLE deployment_config DROP COLUMN guild_name;
