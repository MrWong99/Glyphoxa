-- Phase 3: Audit log + Google/GitHub OAuth support

-- Add Google and GitHub OAuth columns to users.
ALTER TABLE mgmt.users ADD COLUMN IF NOT EXISTS google_id TEXT;
ALTER TABLE mgmt.users ADD COLUMN IF NOT EXISTS github_id TEXT;

-- Create unique indexes (only if they don't exist).
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_google ON mgmt.users(google_id) WHERE google_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_github ON mgmt.users(github_id) WHERE github_id IS NOT NULL;

-- Audit log: append-only log of all write operations.
CREATE TABLE IF NOT EXISTS mgmt.audit_log (
    id              BIGSERIAL   PRIMARY KEY,
    tenant_id       TEXT,
    user_id         TEXT,
    action          TEXT        NOT NULL,
    resource_type   TEXT        NOT NULL,
    resource_id     TEXT        NOT NULL,
    changes         JSONB,
    ip_address      INET,
    user_agent      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audit_log_resource
    ON mgmt.audit_log(resource_type, resource_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_audit_log_user
    ON mgmt.audit_log(user_id, created_at DESC)
    WHERE user_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_audit_log_tenant
    ON mgmt.audit_log(tenant_id, created_at DESC)
    WHERE tenant_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_audit_log_created
    ON mgmt.audit_log(created_at DESC);
