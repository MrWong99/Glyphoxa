CREATE TABLE IF NOT EXISTS tenants (
    id                    TEXT        PRIMARY KEY,
    license_tier          TEXT        NOT NULL DEFAULT 'shared',
    bot_token             TEXT        NOT NULL DEFAULT '',
    guild_ids             TEXT[]      DEFAULT '{}',
    monthly_session_hours NUMERIC(10,2) NOT NULL DEFAULT 0,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_tenants_tier ON tenants (license_tier);
