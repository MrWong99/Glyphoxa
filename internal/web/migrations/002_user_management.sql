-- User management: preferences column + invites table.
ALTER TABLE mgmt.users ADD COLUMN IF NOT EXISTS preferences JSONB NOT NULL DEFAULT '{}';

CREATE TABLE IF NOT EXISTS mgmt.invites (
    id          TEXT        PRIMARY KEY,
    tenant_id   TEXT        NOT NULL,
    role        TEXT        NOT NULL DEFAULT 'viewer',
    created_by  TEXT        NOT NULL REFERENCES mgmt.users(id),
    token       TEXT        NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_by     TEXT        REFERENCES mgmt.users(id),
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_mgmt_invites_token ON mgmt.invites(token) WHERE used_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_mgmt_invites_tenant ON mgmt.invites(tenant_id);
