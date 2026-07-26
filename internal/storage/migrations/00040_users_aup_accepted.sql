-- +goose Up

-- AUP/ToS acceptance record (#518, ADR-0055 open mode): the OAuth signup path
-- refuses an open-mode signup whose start did not carry the acknowledgment,
-- and stamps WHEN the user last accepted the Nutzungsbedingungen +
-- Datenschutzerklärung — the §305 BGB inclusion evidence the operator needs.
-- NULL for accounts that predate the column and for allowlist-mode logins,
-- which are not gated (#518 decision: the acknowledgment gates open-mode
-- signup only).
ALTER TABLE users
    ADD COLUMN aup_accepted_at timestamptz;

-- +goose Down

ALTER TABLE users
    DROP COLUMN IF EXISTS aup_accepted_at;
