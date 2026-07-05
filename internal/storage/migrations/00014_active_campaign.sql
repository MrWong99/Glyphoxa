-- +goose Up

-- The operator's durable Active Campaign selection (#108, ADR-0009). Until now
-- the Active Campaign was implicitly the most-recently-created campaign
-- (storage.GetActiveCampaign); /glyphoxa use makes it an explicit per-operator
-- choice that both the slash-command surface and the web Session screen honor.
-- It lives on the users row keyed by discord_user_id — the same identity the
-- OAuth web tier upserts (00003) — so one column serves both surfaces.
--
-- ON DELETE SET NULL: deleting the chosen campaign clears the selection rather
-- than cascading the user away; Active Campaign resolution then falls through to
-- the most-recently-created fallback (ADR-0009 step 3), so a stale pointer can
-- never wedge the two commands that depend on it.

ALTER TABLE users
    ADD COLUMN active_campaign_id uuid REFERENCES campaign (id) ON DELETE SET NULL;

-- +goose Down

ALTER TABLE users DROP COLUMN IF EXISTS active_campaign_id;
