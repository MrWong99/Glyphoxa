-- +goose Up

-- Single-operator auth control-plane (ADR-0016 / ADR-0039): the `users` and
-- `sessions` tables the ADR-0016 cookie session needs, plus the thin binding of
-- the single seeded Tenant to the first operator.
--
-- Scope is deliberately narrow (ADR-0039 single-operator fast-path): NO
-- tenant_members roles, NO api_keys, NO onboarding. The multi-tenant surface
-- fills in behind these without a rewrite — the operator↔tenant link is a single
-- nullable column on `tenant`, not a membership table.

-- ── users ────────────────────────────────────────────────────────────────────
-- A human operator, authenticated via Discord OAuth (Discord-only, ADR-0016).
-- discord_user_id is the Discord snowflake and the stable identity key; name and
-- avatar are display-only and refreshed from Discord on every login.
CREATE TABLE users (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    discord_user_id  text NOT NULL UNIQUE,
    name             text NOT NULL DEFAULT '',
    -- avatar is an absolute image URL (or empty); Discord's CDN URL is composed
    -- at upsert time so the SPA renders it directly.
    avatar           text NOT NULL DEFAULT '',
    -- role is a free-text label for now (e.g. 'operator'); the tenant_members
    -- role matrix (ADR-0002) is deferred (ADR-0039).
    role             text NOT NULL DEFAULT 'operator',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- ── sessions ─────────────────────────────────────────────────────────────────
-- Server-side session (ADR-0016): an opaque random token lives in the
-- `glyphoxa_session` cookie; this row is the authority. Revocation is a row
-- delete (the reason JWT was rejected). expires_at gates validity; last_seen_at
-- is bumped on each authenticated request.
CREATE TABLE sessions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- token is the opaque, high-entropy secret in the cookie (crypto/rand, not a
    -- JWT). Unique so a token resolves to exactly one session.
    token        text NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    -- ip / ua are captured at issuance for audit; not used for validation.
    ip           text NOT NULL DEFAULT '',
    ua           text NOT NULL DEFAULT ''
);

CREATE INDEX sessions_user_idx ON sessions (user_id);

-- ── tenant ↔ operator binding (ADR-0039 thin pass-through) ───────────────────
-- The single seeded Tenant is bound to the first operator who logs in. A single
-- nullable column (not a tenant_members table) keeps the binding thin; the
-- X-Tenant-Id interceptor resolves the operator's tenant from it. ON DELETE SET
-- NULL so removing a user unbinds rather than cascading away the campaign data.
ALTER TABLE tenant
    ADD COLUMN operator_user_id uuid REFERENCES users (id) ON DELETE SET NULL;

-- +goose Down

ALTER TABLE tenant DROP COLUMN IF EXISTS operator_user_id;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
