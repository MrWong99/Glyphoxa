-- Web Management Service: initial schema
-- Creates mgmt schema with users and campaigns tables.

CREATE SCHEMA IF NOT EXISTS mgmt;

-- Users: social auth users (Discord OAuth2)
CREATE TABLE IF NOT EXISTS mgmt.users (
    id              TEXT        PRIMARY KEY,
    tenant_id       TEXT        NOT NULL,
    discord_id      TEXT        UNIQUE,
    email           TEXT,
    display_name    TEXT        NOT NULL,
    avatar_url      TEXT,
    role            TEXT        NOT NULL DEFAULT 'viewer',
    last_login_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_mgmt_users_tenant
    ON mgmt.users(tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_mgmt_users_discord
    ON mgmt.users(discord_id) WHERE discord_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_mgmt_users_role
    ON mgmt.users(tenant_id, role) WHERE deleted_at IS NULL;

-- Campaigns: groups NPCs and sessions under a named campaign.
CREATE TABLE IF NOT EXISTS mgmt.campaigns (
    id              TEXT        PRIMARY KEY,
    tenant_id       TEXT        NOT NULL,
    name            TEXT        NOT NULL,
    system          TEXT        NOT NULL DEFAULT '',
    description     TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_mgmt_campaigns_tenant
    ON mgmt.campaigns(tenant_id) WHERE deleted_at IS NULL;
