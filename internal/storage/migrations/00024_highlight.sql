-- +goose Up
-- Session Highlights persistence (#308, Epic 8, ADR-0051): a two-tier highlight
-- row per detected epic moment. status starts 'candidate' (7-day purge horizon,
-- ADR-0051) and only an explicit GM promotion flips it to 'promoted' (kept). The
-- audio clip lives behind the blob seam (ADR-0048) — clip_key reconstructs the
-- blob.Key, and there is deliberately NO FK to the blob: deletion goes through
-- the seam (blob.Delete), never a DB cascade.
--
-- campaign_id CASCADEs: a campaign hard-delete (#265) takes its highlight rows
-- with it, and the clip blobs are swept via ListCampaignHighlightClipKeys BEFORE
-- the delete commits (the campaign-delete tx also enqueues the blob sweep job).
--
-- voice_session_id is ON DELETE RESTRICT, NOT CASCADE, deliberately: there is NO
-- session-delete path today (sessions are only ever ended, never individually
-- removed), so RESTRICT is inert on every real flow — including campaign delete,
-- where the campaign_id CASCADE removes the highlight rows before the cascaded
-- voice_sessions delete is checked. Its job is to be a tripwire: if a future
-- session-delete path is ever added, RESTRICT makes it fail loudly rather than
-- silently cascading highlight rows away and orphaning their clip blobs (there is
-- no blob sweep on a session delete).
CREATE TABLE highlight (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenant(id),
    voice_session_id uuid NOT NULL REFERENCES voice_sessions(id) ON DELETE RESTRICT,
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
