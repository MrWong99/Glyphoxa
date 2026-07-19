-- +goose Up

-- Presence-owner election (#492, ADR-0057 (c)): every gateway session on a shared
-- central token receives the FULL event stream, so N Voice Instances holding
-- sessions on that token would each see every INTERACTION_CREATE and each try to
-- handle it (Discord deduplicates nothing between sessions on one token, P5). This
-- singleton row elects exactly ONE Voice Instance to register command listeners
-- and dispatch interactions; non-owners drop the duplicate events they still
-- receive.
--
-- Singleton by construction: the PRIMARY KEY is a boolean pinned true by a CHECK,
-- so the table holds at most one row — the current owner's claim. Election is a
-- single upsert (AcquireOrRenewPresenceOwner) that wins when the caller already
-- owns the row OR the incumbent's heartbeat has expired; failover is thus the same
-- expiry-then-claim idiom the job runner (ADR-0049) and the voice claim plane
-- (#491) already use. Poll only — no LISTEN/NOTIFY (ADR-0057 (b)).
CREATE TABLE presence_owner (
    singleton    boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    instance_id  text NOT NULL,
    heartbeat_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down

DROP TABLE IF EXISTS presence_owner;
