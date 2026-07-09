-- +goose Up

-- Campaign archive lifecycle (#269, decided on #265). archived_at is a nullable
-- timestamp, not a boolean: NULL = active, a non-NULL value = the moment the
-- campaign was archived (an audit trail the boolean would throw away). Archived
-- campaigns are excluded from ListCampaigns, the /glyphoxa use autocomplete, and
-- the GetActiveCampaign most-recent fallback, and can neither start a Voice
-- Session nor be the resolved Active Campaign. No index: a single operator has a
-- handful of campaigns, so the WHERE archived_at IS NULL filter is a trivial scan.
ALTER TABLE campaign ADD COLUMN archived_at timestamptz;

-- +goose Down

ALTER TABLE campaign DROP COLUMN archived_at;
