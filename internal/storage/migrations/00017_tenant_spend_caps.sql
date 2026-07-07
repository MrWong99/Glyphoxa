-- +goose Up

-- Per-Tenant spend caps (#130, ADR-0046): two independently opt-in USD thresholds
-- that stop a Voice Session's Agent turns once its estimated spend crosses them.
-- Both NULL by default → today's behavior (no cap). Either alone is valid; when
-- both are set the RPC enforces hard >= soft. double precision holds a currency
-- estimate — an approximate figure, never a billed amount (ADR-0046).
ALTER TABLE tenant
    ADD COLUMN spend_cap_soft_usd double precision,
    ADD COLUMN spend_cap_hard_usd double precision;

-- +goose Down

ALTER TABLE tenant
    DROP COLUMN spend_cap_soft_usd,
    DROP COLUMN spend_cap_hard_usd;
