-- +goose Up

-- Self-signup admission state (ADR-0055): the deployment-persistent Admission
-- Mode posture and the open-mode suspension column.

-- deployment_settings: deployment-scoped (not per-Tenant) posture the boot
-- records and reads. A single row — env-only admission posture is a rollback
-- trap (an env var silently lost on a config change would flip the deployment
-- back to allowlist posture and mass-revoke every signup's session at the boot
-- sweep), so the posture lives here, versioned and visible, with the env var
-- as the operator-facing switch (ADR-0055).
CREATE TABLE deployment_settings (
    -- Singleton guard: TRUE is the only legal PK value, so at most one row.
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    -- 'allowlist' | 'open' (ADR-0055). Text, not an enum: the app validates the
    -- vocabulary, and an older binary reading an unknown future value must not
    -- fail at the type layer.
    admission_mode text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Open-mode revocation is suspension-based, not sweep-based (ADR-0055): the
-- allowlist boot sweep must not run in open mode (it would log out every
-- signup each restart), so lock-out becomes a per-user runtime decision that
-- the per-request session re-check enforces. Nullable and non-destructive —
-- unsuspending is clearing the timestamp; sessions are never deleted.
ALTER TABLE users ADD COLUMN suspended_at timestamptz;

-- +goose Down

ALTER TABLE users DROP COLUMN suspended_at;
DROP TABLE deployment_settings;
