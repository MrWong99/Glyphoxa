CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE IF NOT EXISTS sessions (
    id              TEXT         PRIMARY KEY,
    tenant_id       TEXT         NOT NULL,
    campaign_id     TEXT         NOT NULL,
    guild_id        TEXT         NOT NULL,
    channel_id      TEXT         NOT NULL DEFAULT '',
    license_tier    TEXT         NOT NULL,
    state           TEXT         NOT NULL DEFAULT 'pending',
    error           TEXT,
    worker_pod      TEXT,
    worker_node     TEXT,
    started_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    ended_at        TIMESTAMPTZ,
    last_voice      TIMESTAMPTZ,
    last_heartbeat  TIMESTAMPTZ,
    metadata        JSONB        DEFAULT '{}'
);

-- Prevent two active sessions for the same campaign.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'unique_active_campaign'
    ) THEN
        ALTER TABLE sessions ADD CONSTRAINT unique_active_campaign
            EXCLUDE USING gist (
                campaign_id WITH =,
                tstzrange(started_at, COALESCE(ended_at, 'infinity'), '[)') WITH &&
            ) WHERE (state != 'ended');
    END IF;
END$$;

-- Shared tier: at most 1 active session per tenant.
CREATE UNIQUE INDEX IF NOT EXISTS idx_one_active_session_shared
    ON sessions (tenant_id)
    WHERE state != 'ended'
    AND license_tier = 'shared';

-- Dedicated tier: at most 1 active session per guild.
CREATE UNIQUE INDEX IF NOT EXISTS idx_one_active_session_per_guild_dedicated
    ON sessions (tenant_id, guild_id)
    WHERE state != 'ended'
    AND license_tier = 'dedicated';

CREATE INDEX IF NOT EXISTS idx_sessions_tenant ON sessions (tenant_id);
CREATE INDEX IF NOT EXISTS idx_sessions_active ON sessions (state)
    WHERE state != 'ended';
