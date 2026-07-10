-- +goose Up
-- Session Highlights persistence (#308, Epic 8, ADR-0051): a two-tier highlight
-- row per detected epic moment. status starts 'candidate' (7-day purge horizon,
-- ADR-0051) and only an explicit GM promotion flips it to 'promoted' (kept). The
-- audio clip lives behind the blob seam (ADR-0048) — clip_key reconstructs the
-- blob.Key, and there is deliberately NO FK to the blob: deletion goes through
-- the seam (blob.Delete), never a DB cascade. The campaign_id / voice_session_id
-- FKs DO cascade so a campaign/session hard-delete removes the rows (the blob
-- keys are swept via ListCampaignHighlightClipKeys BEFORE the row cascade).
CREATE TABLE highlight (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenant(id),
    voice_session_id uuid NOT NULL REFERENCES voice_sessions(id) ON DELETE CASCADE,
    campaign_id uuid NOT NULL REFERENCES campaign(id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'candidate' CHECK (status IN ('candidate','promoted')),
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    score double precision NOT NULL,
    excerpt text NOT NULL DEFAULT '',
    reason text NOT NULL DEFAULT '',
    speaker_ids text[] NOT NULL DEFAULT '{}',
    clip_key text NOT NULL,
    clip_content_type text NOT NULL DEFAULT 'audio/wav',
    clip_size_bytes bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    promoted_at timestamptz
);
CREATE INDEX highlight_session_idx ON highlight (voice_session_id);
CREATE INDEX highlight_campaign_idx ON highlight (campaign_id);

-- +goose Down
DROP TABLE IF EXISTS highlight;
