-- +goose Up

-- Cross-pod live voice controls (#503, ADR-0057 (b)): a requested-control QUEUE
-- hanging off the voice_session_intents claim plane. The requester (presence
-- owner or split web tier) writes a row; the worker HOSTING the intent's session
-- drains all pending rows in (created_at, id) order on its own heartbeat tick,
-- executes each against its local Manager, and writes the terminal status the
-- requester polls for. DB-write-then-poll only — no LISTEN/NOTIFY (ADR-0057 (b)),
-- and the control never re-targets another instance (ADR-0006/0057 (e)): only
-- the owning worker's own loop dispatches, mirroring the stop_requested
-- handshake and ADR-0051's write-then-poll precedent.
CREATE TABLE voice_session_controls (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    intent_id   uuid NOT NULL REFERENCES voice_session_intents (id) ON DELETE CASCADE,
    tenant_id   uuid NOT NULL REFERENCES tenant (id) ON DELETE CASCADE,
    -- 'mute_agent' | 'mute_all' | 'say' | 'butler_say'. Text, not an enum: the
    -- app validates the vocabulary (mirrors voice_session_intents.status).
    kind        text NOT NULL,
    agent_id    text NOT NULL DEFAULT '',
    say_text    text NOT NULL DEFAULT '',
    muted       boolean NOT NULL DEFAULT false,
    -- pending | done | failed. Terminal writes are fenced WHERE status='pending'
    -- (ErrNotFound = lost race), the intent-row idiom.
    status      text NOT NULL DEFAULT 'pending',
    -- The muted-agent id set a mute control returns (Manager.SetAgentMute /
    -- SetAllMute result), relayed back to the requester.
    result_ids  text[] NOT NULL DEFAULT '{}',
    last_error  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    ended_at    timestamptz
);

-- The worker's per-heartbeat drain scan (pending rows of one intent, oldest
-- first) and the orphan sweep both ride this.
CREATE INDEX voice_session_controls_dispatch_idx
    ON voice_session_controls (intent_id, status, created_at);

-- +goose Down

DROP TABLE IF EXISTS voice_session_controls;
