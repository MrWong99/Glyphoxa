-- +goose Up

-- voice_sessions backs the Session screen's Start/Stop loop (#72, ADR-0039): one
-- row per Voice Session — the Bot's presence in one Discord voice channel, bound
-- to a Campaign (CONTEXT.md). A session is INSERTed status='running' on Start and
-- UPDATEd ended_at=now()/status='ended' on Stop. line_count records how many
-- transcript lines the session produced (0 for this stage; the live transcript
-- feed is #73/SSE).
CREATE TABLE voice_sessions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id  uuid NOT NULL REFERENCES campaign (id) ON DELETE CASCADE,
    started_at   timestamptz NOT NULL DEFAULT now(),
    -- NULL while the session is running; set to now() on Stop / loop exit.
    ended_at     timestamptz,
    -- Lifecycle: 'running' then 'ended'. Plain text (no enum) keeps the stage's
    -- migration additive; the manager writes the two values it owns.
    status       text NOT NULL,
    line_count   int NOT NULL DEFAULT 0
);

CREATE INDEX voice_sessions_campaign_idx ON voice_sessions (campaign_id);

-- Wire up the transcript_chunk SEAM (#6): the voice_session_id column already
-- exists (00001_init) as a nullable uuid placeholder. Add the FK additively now
-- that the target table exists, but keep the column NULLABLE — transcript writes
-- do not set it yet (#73), so NOT NULL stays deferred. ON DELETE SET NULL: losing
-- a session must not delete its transcript chunks.
ALTER TABLE transcript_chunk
    ADD CONSTRAINT transcript_chunk_voice_session_id_fkey
    FOREIGN KEY (voice_session_id) REFERENCES voice_sessions (id) ON DELETE SET NULL;

-- +goose Down

ALTER TABLE transcript_chunk
    DROP CONSTRAINT IF EXISTS transcript_chunk_voice_session_id_fkey;
DROP TABLE IF EXISTS voice_sessions;
