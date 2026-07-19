-- +goose Up

-- Voice-session claim plane (#491, ADR-0057 (b)): a start writes an INTENT row
-- here instead of driving the in-process Manager directly, and a -mode voice
-- worker claims it with the SAME FOR UPDATE SKIP LOCKED + heartbeat idiom the
-- job runner proves (ADR-0049, internal/storage/job.go). Poll only — no
-- LISTEN/NOTIFY (ADR-0057 (b)). Tenant-keyed: multi-guild tenant mapping is
-- deferred at the epic level, so one live intent per Tenant is the invariant.
--
-- No mid-session takeover (ADR-0006/0057 (e)): a claim's heartbeat going stale
-- means the owning Voice Instance is DEAD, so the row is marked 'dead' and the
-- Tenant restarts — the session is NEVER handed to another instance (DAVE/MLS
-- state cannot migrate). ClaimVoiceSessionIntent therefore claims 'pending' rows
-- ONLY; it never re-claims a 'claimed'/'live' one whose worker crashed.
CREATE TABLE voice_session_intents (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid NOT NULL REFERENCES tenant (id) ON DELETE CASCADE,
    campaign_id      uuid NOT NULL REFERENCES campaign (id) ON DELETE CASCADE,
    -- pending | claimed | live | done | dead | failed. Text, not an enum: the app
    -- validates the vocabulary and an older binary reading a future value must not
    -- fail at the type layer (mirrors deployment_settings.admission_mode).
    status           text NOT NULL DEFAULT 'pending',
    -- The Voice Instance that holds the claim: hostname-uuid8, minted per boot in
    -- cmd/glyphoxa. Empty until claimed; the fence for heartbeat/finish writes.
    instance_id      text NOT NULL DEFAULT '',
    -- The voice_sessions row the worker created once it went live. SET NULL if that
    -- lifecycle row is ever deleted — the intent's own lifecycle is authoritative.
    voice_session_id uuid REFERENCES voice_sessions (id) ON DELETE SET NULL,
    -- The web tier flags this to wind a live session down; the owning worker sees it
    -- on the next heartbeat and stops its local session (ADR-0057 (b) release).
    stop_requested   boolean NOT NULL DEFAULT false,
    last_error       text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    claimed_at       timestamptz,
    heartbeat_at     timestamptz,
    ended_at         timestamptz
);

-- One live intent per Tenant (the tenant-keyed invariant): a second start while a
-- pending/claimed/live intent exists trips 23505 → ErrIntentActive. Terminal rows
-- (done/dead/failed) fall out of the predicate, so a Tenant can start again once
-- its prior intent finished.
CREATE UNIQUE INDEX voice_session_intents_one_live_per_tenant
    ON voice_session_intents (tenant_id)
    WHERE status IN ('pending', 'claimed', 'live');

-- The claim scan: oldest pending first (ORDER BY created_at), and the reaper's
-- stale-heartbeat sweep both ride this.
CREATE INDEX voice_session_intents_claim_idx
    ON voice_session_intents (status, created_at);

-- +goose Down

DROP TABLE IF EXISTS voice_session_intents;
