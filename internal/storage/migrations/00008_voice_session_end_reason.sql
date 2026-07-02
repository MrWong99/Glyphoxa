-- +goose Up

-- end_reason distinguishes HOW a Voice Session ended (#143). NULL is the normal
-- Stop / loop-exit path; the boot-time reconciliation stamps 'orphaned:
-- reconciled at startup' when it closes a row still 'running' with no live loop
-- (crash / kill -9 / a failed ended_at write). Additive and nullable, so the
-- migration is safe over existing rows.
ALTER TABLE voice_sessions
    ADD COLUMN end_reason text;

-- +goose Down

ALTER TABLE voice_sessions
    DROP COLUMN IF EXISTS end_reason;
