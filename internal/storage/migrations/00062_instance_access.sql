-- +goose Up

-- Web-managed instance access (ADR-0065). Until now the ONLY way to admit a
-- Discord User on an `allowlist` deployment was to edit GLYPHOXA_OPERATOR_IDS
-- and restart. That env list stays, re-scoped to what ADR-0055 already called
-- it — the Platform Admin list — and everything else moves into the database
-- so a Platform Admin manages it from the web UI.

-- invitation is a shareable `/join/<token>` link. One table serves both kinds
-- (ADR-0065): an `access` invitation admits its redeemer to the instance (an
-- Access Grant, founding their own Tenant); a `campaign` invitation makes its
-- redeemer a Campaign Guest. The token is 256 bits of crypto/rand and only its
-- SHA-256 is stored (the sessions.token reasoning), so a database read yields
-- no usable link.
CREATE TABLE invitation (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind             text NOT NULL CHECK (kind IN ('access', 'campaign')),
    token_hash       text NOT NULL UNIQUE,
    label            text NOT NULL DEFAULT '',
    -- campaign_id / rights carry a `campaign` invitation's grant; an `access`
    -- invitation has neither.
    campaign_id      uuid REFERENCES campaign (id) ON DELETE CASCADE,
    rights           text[] NOT NULL DEFAULT '{}',
    -- platform_keys is what an `access` invitation grants the Tenant it founds:
    -- whether that Tenant may spend the deployment's Platform Keys.
    platform_keys    boolean NOT NULL DEFAULT false,
    -- discord_user_id, when set, pins the link to one Discord User: anyone else
    -- who opens it is refused.
    discord_user_id  text NOT NULL DEFAULT '',
    -- max_uses NULL = unlimited (until it expires or is revoked).
    max_uses         integer CHECK (max_uses > 0),
    uses             integer NOT NULL DEFAULT 0,
    expires_at       timestamptz,
    created_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    revoked_at       timestamptz,
    CONSTRAINT invitation_kind_campaign CHECK ((kind = 'campaign') = (campaign_id IS NOT NULL))
);

CREATE INDEX invitation_campaign_idx ON invitation (campaign_id) WHERE campaign_id IS NOT NULL;

-- access_grant admits one Discord User to an `allowlist` deployment. Keyed by
-- snowflake, not users.id: a Platform Admin grants access BEFORE the person
-- has ever signed in. Revoking is a row delete (plus that user's sessions).
CREATE TABLE access_grant (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    discord_user_id  text NOT NULL UNIQUE,
    note             text NOT NULL DEFAULT '',
    platform_keys    boolean NOT NULL DEFAULT false,
    granted_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    invitation_id    uuid REFERENCES invitation (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now()
);

-- tenant.platform_keys gates the env-key fallback per Tenant on an `allowlist`
-- deployment (ADR-0054 seam (a)). DEFAULT true keeps every existing Tenant
-- exactly as it was: before this column, allowlist mode granted every Tenant
-- the fallback. Tenants founded through an Access Grant take the grant's value.
ALTER TABLE tenant ADD COLUMN platform_keys boolean NOT NULL DEFAULT true;

-- +goose Down

ALTER TABLE tenant DROP COLUMN IF EXISTS platform_keys;
DROP TABLE IF EXISTS access_grant;
DROP TABLE IF EXISTS invitation;
