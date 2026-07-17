-- +goose Up

-- SaaS billing foundation (ADR-0054): a configurable Plan catalog, per-Tenant
-- Subscriptions with price snapshots, and a durable per-Tenant Usage Ledger.
--
-- Everything here is deployment-optional: a pure-BYOK self-host never syncs a
-- plan, never subscribes a tenant, and the ledger simply accumulates rows the
-- operator can ignore. Nothing in the voice path reads these tables.

-- Where a Plan's provider keys come from (ADR-0004 / ADR-0054):
--   'byok'     — the Tenant supplies its own provider credentials (v1.0 default).
--   'platform' — the deployment's own provider keys (the env-fallback path) serve
--                the Tenant's usage; the subscription price covers it.
CREATE TYPE plan_key_source AS ENUM ('byok', 'platform');

-- The Plan catalog: one row per subscription tier. Rows are synced from the
-- operator's declarative catalog file (`glyphoxa billing plans-sync`, ADR-0054),
-- never hand-edited by the app. `slug` is the stable configuration handle;
-- everything else may change between syncs. Removed tiers are ARCHIVED, never
-- deleted — subscriptions reference them and revenue history must keep resolving.
CREATE TABLE plan (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL UNIQUE,
    display_name text NOT NULL,
    description text NOT NULL DEFAULT '',
    -- The tier's monthly list price. double precision holds a currency estimate
    -- consistent with the spend-cap columns (ADR-0046); authoritative invoicing
    -- (a payment processor) is a later layer (ADR-0054).
    monthly_price_usd double precision NOT NULL DEFAULT 0
        CHECK (monthly_price_usd >= 0),
    key_source plan_key_source NOT NULL DEFAULT 'byok',
    -- Monthly estimated-USD provider-usage allowance included in a 'platform'
    -- plan. NULL = no configured allowance (unbounded, or BYOK where it is moot).
    included_usage_usd double precision
        CHECK (included_usage_usd IS NULL OR included_usage_usd >= 0),
    -- Extensible per-tier knobs (max campaigns, max concurrent sessions, feature
    -- flags, …) without schema churn: consumers read the keys they know.
    limits jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- Archived plans accept no NEW subscriptions; existing ones keep working.
    archived boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- A Tenant's binding to a Plan. plan_slug and monthly_price_usd are SNAPSHOTS
-- taken at subscribe time: editing a plan's price in the catalog never rewrites
-- what running subscriptions are worth, so revenue stays measurable from these
-- rows alone. At most one ACTIVE (ended_at IS NULL) subscription per tenant;
-- ended rows are the subscription history.
CREATE TABLE tenant_subscription (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenant (id) ON DELETE CASCADE,
    plan_id uuid NOT NULL REFERENCES plan (id),
    plan_slug text NOT NULL,
    monthly_price_usd double precision NOT NULL,
    started_at timestamptz NOT NULL DEFAULT now(),
    ended_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX tenant_subscription_active_uq
    ON tenant_subscription (tenant_id)
    WHERE ended_at IS NULL;
CREATE INDEX tenant_subscription_tenant_idx
    ON tenant_subscription (tenant_id);

-- The durable per-Tenant Usage Ledger (ADR-0054): daily-bucketed accumulation of
-- the same metered usage the in-memory spend meter reads (ADR-0045/0046), keyed
-- (tenant, day, component, provider, model) and upsert-accumulated. estimated_usd
-- is priced at write time from the static price map — a persisted ESTIMATE for
-- cost attribution, never billing truth (ADR-0046 posture). Quantities are kept
-- alongside so a price-map change never rewrites history.
CREATE TABLE usage_ledger (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenant (id) ON DELETE CASCADE,
    day date NOT NULL,
    component provider_component NOT NULL,
    provider text NOT NULL,
    model text NOT NULL DEFAULT '',
    llm_input_tokens bigint NOT NULL DEFAULT 0,
    llm_output_tokens bigint NOT NULL DEFAULT 0,
    tts_characters bigint NOT NULL DEFAULT 0,
    stt_audio_seconds double precision NOT NULL DEFAULT 0,
    estimated_usd double precision NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, day, component, provider, model)
);

CREATE INDEX usage_ledger_tenant_day_idx ON usage_ledger (tenant_id, day);

-- +goose Down

DROP TABLE usage_ledger;
DROP TABLE tenant_subscription;
DROP TABLE plan;
DROP TYPE plan_key_source;
