-- +goose Up

-- transcript_line has no index on campaign_id (00007/00015/00020 — 00020 even
-- notes "NO index — no query filters on it"). That held until the roster prep
-- dashboard's "last spoke" read (#544), which filters by campaign_id and runs on
-- every Cast-tab render: without this index it is a sequential scan of EVERY
-- tenant's transcript, so one GM opening a tab reads the whole platform's Line
-- history. The cost scales with total transcript volume rather than with the
-- campaign, which is the wrong shape for a multi-tenant deployment (ADR-0057).
--
-- (campaign_id, kind) matches the read exactly — it groups Agent turns
-- (kind IN ('npc','butler')) per campaign — and also serves any later
-- campaign-scoped Line read without a second index.
CREATE INDEX transcript_line_campaign_kind_idx ON transcript_line (campaign_id, kind);

-- +goose Down

DROP INDEX IF EXISTS transcript_line_campaign_kind_idx;
