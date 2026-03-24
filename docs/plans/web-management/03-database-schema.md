# Database Schema Design — Web Management Service

**Date:** 2026-03-24
**Status:** Draft
**Depends on:** [Pricing Models](pricing-models.md), [Admin Web UI Plan](../2026-03-23-admin-web-ui-plan.md)

---

## Design Decisions

### 1. Same PostgreSQL Instance, Separate Schema

The web management tables live in the **same PostgreSQL instance** as the gateway
but in a dedicated `mgmt` schema. Reasons:

- **Foreign keys work.** `mgmt.subscriptions` references `public.tenants(id)` —
  cross-schema FKs work natively in PostgreSQL within the same database.
- **Shared connection pool.** The gateway already manages a `*sql.DB` pool.
  Adding a schema avoids a second pool and second set of credentials.
- **Operational simplicity.** One backup, one migration pipeline, one monitoring target.
  At >1000 users this is still a small database (<10GB).
- **Clean separation.** The `mgmt` schema keeps billing/user tables out of the
  `public` schema where runtime engine tables live. A `SET search_path` or
  schema-qualified names prevent accidental coupling.

When to revisit: if the management service moves to a separate process (not
embedded in the gateway), extract the `mgmt` schema to its own database.

### 2. Multi-Tenancy: Shared Tables with `tenant_id`

Matches the existing pattern (sessions, usage_records, npc_definitions all use
`tenant_id`). Row-level security (RLS) is added in Phase 2 when user auth lands:

```sql
ALTER TABLE mgmt.campaigns ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON mgmt.campaigns
    USING (tenant_id = current_setting('app.current_tenant_id'));
```

Schema-per-tenant is overkill at this scale and makes cross-tenant admin queries
(super_admin dashboard) painful.

### 3. Soft Delete Where It Matters

Tables with user-visible data use `deleted_at TIMESTAMPTZ` — users, campaigns,
NPCs, voice profiles, support tickets. This enables undo, audit, and prevents
FK breakage in historical records.

Event/log tables (payment_events, audit_log, usage_metering) are append-only
and never deleted.

### 4. IDs

- User-facing tables: `TEXT` primary keys holding UUIDs (matches existing
  Glyphoxa convention — tenants, sessions, NPCs all use `TEXT` PKs).
- Event/log tables: `BIGSERIAL` for compact storage and fast sequential writes.
- Stripe IDs stored as `TEXT` (e.g., `cus_xxx`, `sub_xxx`, `pi_xxx`).

### 5. Timestamps

All tables include `created_at TIMESTAMPTZ NOT NULL DEFAULT now()`.
Mutable tables add `updated_at TIMESTAMPTZ NOT NULL DEFAULT now()`.
Soft-deletable tables add `deleted_at TIMESTAMPTZ`.

---

## Schema Overview

```
mgmt schema
├── users                  ← social auth users (Discord, Google)
├── user_sessions          ← JWT session tracking
├── user_preferences       ← per-user settings (JSONB)
├── subscription_plans     ← tier definitions (Apprentice, Adventurer, DM, Guild)
├── subscriptions          ← user/tenant → plan binding
├── payment_events         ← Stripe webhook log (append-only)
├── invoices               ← generated invoices
├── usage_metering         ← per-session cost breakdown
├── campaigns              ← promoted from string field
├── campaign_lore          ← markdown world-building docs
├── voice_samples          ← uploaded audio for custom voices
├── voice_profiles         ← created custom voices with provider IDs
├── support_tickets        ← support requests
└── audit_log              ← all write operations (append-only)

public schema (existing, unchanged)
├── tenants
├── sessions
├── usage_records
├── npc_definitions
├── session_entries
├── chunks
├── entities
├── relationships
├── sessions (memory)
└── recaps
```

---

## Schema Creation

```sql
CREATE SCHEMA IF NOT EXISTS mgmt;
```

---

## User Management

### `mgmt.users`

Primary user table. Supports multiple OAuth providers per user via the
`discord_id` and `google_id` fields. A user is always bound to exactly one
tenant (the DM-pays model — the DM is the tenant owner, players are viewers).

```sql
CREATE TABLE IF NOT EXISTS mgmt.users (
    id              TEXT        PRIMARY KEY,  -- UUID
    tenant_id       TEXT        NOT NULL REFERENCES public.tenants(id),
    discord_id      TEXT        UNIQUE,       -- Discord snowflake
    google_id       TEXT        UNIQUE,       -- Google sub claim
    email           TEXT,                     -- from OAuth profile, not unique (Google + Discord can share)
    display_name    TEXT        NOT NULL,
    avatar_url      TEXT,                     -- OAuth profile picture
    role            TEXT        NOT NULL DEFAULT 'viewer',
                                             -- super_admin, tenant_admin, dm, viewer
    email_verified  BOOLEAN     NOT NULL DEFAULT false,
    last_login_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ              -- soft delete
);

-- Queries: "list users in tenant", "lookup by OAuth ID"
CREATE INDEX idx_users_tenant    ON mgmt.users(tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_users_discord   ON mgmt.users(discord_id) WHERE discord_id IS NOT NULL;
CREATE INDEX idx_users_google    ON mgmt.users(google_id)  WHERE google_id IS NOT NULL;
CREATE INDEX idx_users_email     ON mgmt.users(email)      WHERE email IS NOT NULL;
CREATE INDEX idx_users_role      ON mgmt.users(tenant_id, role) WHERE deleted_at IS NULL;
```

**Constraint notes:**
- `discord_id` and `google_id` are independently UNIQUE — a user can link both
  but no two users can claim the same provider account.
- `email` is intentionally not unique: the same email can appear on both a
  Discord and Google account that haven't been linked yet.
- `role` is a text enum validated at the application layer (not a PG enum) to
  avoid migration pain when adding roles.

### `mgmt.user_sessions`

JWT session tracking for revocation and audit. Each row represents an active
or expired JWT.

```sql
CREATE TABLE IF NOT EXISTS mgmt.user_sessions (
    id              TEXT        PRIMARY KEY,  -- JWT jti claim (UUID)
    user_id         TEXT        NOT NULL REFERENCES mgmt.users(id) ON DELETE CASCADE,
    ip_address      INET,
    user_agent      TEXT,
    issued_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    revoked_at      TIMESTAMPTZ              -- NULL = active
);

-- "Is this JWT revoked?" — hot path, must be fast
CREATE INDEX idx_user_sessions_user     ON mgmt.user_sessions(user_id);
CREATE INDEX idx_user_sessions_active   ON mgmt.user_sessions(user_id, expires_at)
    WHERE revoked_at IS NULL;

-- Periodic cleanup of expired sessions
CREATE INDEX idx_user_sessions_expired  ON mgmt.user_sessions(expires_at)
    WHERE revoked_at IS NULL;
```

### `mgmt.user_preferences`

Per-user settings stored as JSONB. Thin table — one row per user, schemaless
value. Application code defines the shape; the DB just stores it.

```sql
CREATE TABLE IF NOT EXISTS mgmt.user_preferences (
    user_id         TEXT        PRIMARY KEY REFERENCES mgmt.users(id) ON DELETE CASCADE,
    preferences     JSONB       NOT NULL DEFAULT '{}',
    -- Example shape:
    -- {
    --   "theme": "dark",
    --   "language": "de",
    --   "notifications": { "email": true, "discord": false },
    --   "dashboard": { "default_campaign_id": "rabenheim" }
    -- }
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

No extra indexes needed — PK lookup only.

---

## Billing & Subscriptions

### `mgmt.subscription_plans`

Static tier definitions. Seeded at deployment, rarely changed. Matches the
pricing model (Apprentice, Adventurer, Dungeon Master, Guild).

```sql
CREATE TABLE IF NOT EXISTS mgmt.subscription_plans (
    id                  TEXT        PRIMARY KEY,  -- e.g., "apprentice", "adventurer", "dm", "guild"
    name                TEXT        NOT NULL,      -- display name: "Dungeon Master"
    tier_order          INT         NOT NULL,      -- 0, 1, 2, 3 for sorting
    monthly_price_cents INT         NOT NULL,      -- price in cents (0 for free tier)
    annual_price_cents  INT,                       -- NULL = no annual option
    currency            TEXT        NOT NULL DEFAULT 'usd',
    max_sessions_month  INT,                       -- NULL = unlimited
    max_npcs            INT,                       -- NULL = unlimited
    max_campaigns       INT         NOT NULL DEFAULT 1,
    max_player_seats    INT         NOT NULL DEFAULT 1,
    features            JSONB       NOT NULL DEFAULT '{}',
    -- Example features:
    -- {
    --   "voice_quality": "basic|standard|premium",
    --   "llm_tier": "flash|standard|premium",
    --   "custom_voices": false,
    --   "knowledge_graph": false,
    --   "priority_support": false
    -- }
    stripe_price_id_monthly TEXT,                  -- Stripe Price object ID (monthly)
    stripe_price_id_annual  TEXT,                  -- Stripe Price object ID (annual)
    active              BOOLEAN     NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_subscription_plans_active ON mgmt.subscription_plans(active, tier_order);
```

**Seed data:**

```sql
INSERT INTO mgmt.subscription_plans (id, name, tier_order, monthly_price_cents, annual_price_cents, max_sessions_month, max_npcs, max_campaigns, max_player_seats, features) VALUES
('apprentice',  'Apprentice',      0,    0,    NULL, 2,    2,    1, 1, '{"voice_quality":"basic","llm_tier":"flash","custom_voices":false,"knowledge_graph":false,"priority_support":false}'),
('adventurer',  'Adventurer',      1,  900,   9000, 8,   10,    2, 1, '{"voice_quality":"standard","llm_tier":"standard","custom_voices":false,"knowledge_graph":false,"priority_support":false}'),
('dm',          'Dungeon Master',  2, 1900,  19000, NULL, NULL,  5, 1, '{"voice_quality":"premium","llm_tier":"premium","custom_voices":true,"knowledge_graph":true,"priority_support":false}'),
('guild',       'Guild',           3, 2900,  29000, NULL, NULL, 10, 5, '{"voice_quality":"premium","llm_tier":"premium","custom_voices":true,"knowledge_graph":true,"priority_support":true}');
```

### `mgmt.subscriptions`

Binds a tenant to a plan. One active subscription per tenant at a time.
The `status` field tracks the Stripe subscription lifecycle.

```sql
CREATE TABLE IF NOT EXISTS mgmt.subscriptions (
    id                  TEXT        PRIMARY KEY,  -- UUID
    tenant_id           TEXT        NOT NULL REFERENCES public.tenants(id),
    plan_id             TEXT        NOT NULL REFERENCES mgmt.subscription_plans(id),
    status              TEXT        NOT NULL DEFAULT 'active',
                                   -- active, past_due, canceled, trialing, paused, incomplete
    billing_cycle       TEXT        NOT NULL DEFAULT 'monthly',  -- monthly, annual
    stripe_customer_id  TEXT,       -- Stripe Customer ID (cus_xxx)
    stripe_subscription_id TEXT,    -- Stripe Subscription ID (sub_xxx)
    current_period_start TIMESTAMPTZ NOT NULL,
    current_period_end   TIMESTAMPTZ NOT NULL,
    cancel_at            TIMESTAMPTZ,              -- scheduled cancellation
    canceled_at          TIMESTAMPTZ,              -- when user clicked cancel
    trial_end            TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One active subscription per tenant (allow canceled/expired to coexist)
CREATE UNIQUE INDEX idx_subscriptions_tenant_active
    ON mgmt.subscriptions(tenant_id)
    WHERE status IN ('active', 'trialing', 'past_due');

CREATE INDEX idx_subscriptions_stripe_customer
    ON mgmt.subscriptions(stripe_customer_id)
    WHERE stripe_customer_id IS NOT NULL;
CREATE INDEX idx_subscriptions_stripe_sub
    ON mgmt.subscriptions(stripe_subscription_id)
    WHERE stripe_subscription_id IS NOT NULL;
CREATE INDEX idx_subscriptions_status
    ON mgmt.subscriptions(status);
CREATE INDEX idx_subscriptions_period_end
    ON mgmt.subscriptions(current_period_end)
    WHERE status IN ('active', 'trialing');
```

### `mgmt.payment_events`

Append-only Stripe webhook log. Every webhook delivery is recorded verbatim.
Used for reconciliation, debugging, and audit.

```sql
CREATE TABLE IF NOT EXISTS mgmt.payment_events (
    id                  BIGSERIAL   PRIMARY KEY,
    stripe_event_id     TEXT        NOT NULL UNIQUE,  -- evt_xxx (idempotency key)
    event_type          TEXT        NOT NULL,          -- e.g., "invoice.paid", "customer.subscription.updated"
    tenant_id           TEXT,                          -- resolved from Stripe metadata, NULL if unknown
    subscription_id     TEXT        REFERENCES mgmt.subscriptions(id),
    payload             JSONB       NOT NULL,          -- full Stripe event JSON
    processed           BOOLEAN     NOT NULL DEFAULT false,
    error               TEXT,                          -- processing error message, if any
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_payment_events_type
    ON mgmt.payment_events(event_type);
CREATE INDEX idx_payment_events_tenant
    ON mgmt.payment_events(tenant_id)
    WHERE tenant_id IS NOT NULL;
CREATE INDEX idx_payment_events_unprocessed
    ON mgmt.payment_events(created_at)
    WHERE processed = false;
CREATE INDEX idx_payment_events_created
    ON mgmt.payment_events(created_at);
```

### `mgmt.invoices`

Tracks invoices generated for each billing period. Linked to Stripe Invoice
objects but also stores enough data locally for display without Stripe API calls.

```sql
CREATE TABLE IF NOT EXISTS mgmt.invoices (
    id                  TEXT        PRIMARY KEY,  -- UUID
    tenant_id           TEXT        NOT NULL REFERENCES public.tenants(id),
    subscription_id     TEXT        NOT NULL REFERENCES mgmt.subscriptions(id),
    stripe_invoice_id   TEXT        UNIQUE,       -- in_xxx
    status              TEXT        NOT NULL DEFAULT 'draft',
                                   -- draft, open, paid, void, uncollectible
    currency            TEXT        NOT NULL DEFAULT 'usd',
    subtotal_cents      INT         NOT NULL DEFAULT 0,
    tax_cents           INT         NOT NULL DEFAULT 0,
    total_cents         INT         NOT NULL DEFAULT 0,
    amount_paid_cents   INT         NOT NULL DEFAULT 0,
    period_start        TIMESTAMPTZ NOT NULL,
    period_end          TIMESTAMPTZ NOT NULL,
    due_date            TIMESTAMPTZ,
    paid_at             TIMESTAMPTZ,
    hosted_invoice_url  TEXT,       -- Stripe hosted invoice page
    pdf_url             TEXT,       -- Stripe invoice PDF
    line_items          JSONB       NOT NULL DEFAULT '[]',
    -- Example line_items:
    -- [
    --   {"description": "Adventurer plan (monthly)", "amount_cents": 900, "quantity": 1},
    --   {"description": "Overage: 2 extra sessions", "amount_cents": 200, "quantity": 1}
    -- ]
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_invoices_tenant
    ON mgmt.invoices(tenant_id, created_at DESC);
CREATE INDEX idx_invoices_subscription
    ON mgmt.invoices(subscription_id);
CREATE INDEX idx_invoices_status
    ON mgmt.invoices(status)
    WHERE status IN ('open', 'draft');
CREATE INDEX idx_invoices_stripe
    ON mgmt.invoices(stripe_invoice_id)
    WHERE stripe_invoice_id IS NOT NULL;
```

### `mgmt.usage_metering`

Fine-grained per-session cost breakdown. Complements the existing
`public.usage_records` (monthly aggregates) with session-level detail for
billing transparency and the usage dashboard.

```sql
CREATE TABLE IF NOT EXISTS mgmt.usage_metering (
    id                  BIGSERIAL   PRIMARY KEY,
    tenant_id           TEXT        NOT NULL REFERENCES public.tenants(id),
    session_id          TEXT        NOT NULL,     -- references public.sessions(id)
    campaign_id         TEXT        NOT NULL,
    period              DATE        NOT NULL,     -- first of month (matches usage_records.period)

    -- Duration
    session_duration_s  INT         NOT NULL DEFAULT 0,

    -- Component breakdown
    llm_tokens_in       BIGINT      NOT NULL DEFAULT 0,
    llm_tokens_out      BIGINT      NOT NULL DEFAULT 0,
    stt_seconds         NUMERIC(10,2) NOT NULL DEFAULT 0,
    tts_characters      BIGINT      NOT NULL DEFAULT 0,

    -- Estimated cost (in cents, computed at session end)
    llm_cost_cents      INT         NOT NULL DEFAULT 0,
    stt_cost_cents      INT         NOT NULL DEFAULT 0,
    tts_cost_cents      INT         NOT NULL DEFAULT 0,
    total_cost_cents    INT         NOT NULL DEFAULT 0,

    -- Provider metadata
    llm_provider        TEXT,       -- e.g., "gemini", "openai"
    llm_model           TEXT,       -- e.g., "gemini-2.0-flash"
    stt_provider        TEXT,       -- e.g., "elevenlabs", "deepgram"
    tts_provider        TEXT,       -- e.g., "elevenlabs"

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One metering record per session
CREATE UNIQUE INDEX idx_usage_metering_session
    ON mgmt.usage_metering(session_id);

CREATE INDEX idx_usage_metering_tenant_period
    ON mgmt.usage_metering(tenant_id, period);
CREATE INDEX idx_usage_metering_campaign
    ON mgmt.usage_metering(campaign_id, period);
CREATE INDEX idx_usage_metering_period
    ON mgmt.usage_metering(period);
```

---

## Campaign Management

### `mgmt.campaigns`

Promotes `campaign_id` from a bare string to a first-class entity. The existing
`public.tenants.campaign_id`, `public.sessions.campaign_id`, and
`npc_definitions.campaign_id` fields continue to reference campaign IDs by
string — no FK constraint is added to those existing tables (they predate this
table and may contain legacy IDs). New code should insert the campaign here
first, then use the ID downstream.

```sql
CREATE TABLE IF NOT EXISTS mgmt.campaigns (
    id              TEXT        PRIMARY KEY,  -- short slug, e.g., "rabenheim"
    tenant_id       TEXT        NOT NULL REFERENCES public.tenants(id),
    name            TEXT        NOT NULL,     -- display name: "Die Chroniken von Rabenheim"
    system          TEXT        NOT NULL DEFAULT '',  -- dnd5e, pf2e, fate, custom, etc.
    description     TEXT        NOT NULL DEFAULT '',  -- short blurb
    language        TEXT        NOT NULL DEFAULT 'en', -- ISO 639-1 (de, en, fr, ...)
    settings        JSONB       NOT NULL DEFAULT '{}',
    -- Example settings:
    -- {
    --   "tone": "dark-fantasy",
    --   "era": "medieval",
    --   "homebrew_rules": true
    -- }
    image_url       TEXT,       -- campaign banner/art
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ              -- soft delete
);

CREATE INDEX idx_campaigns_tenant
    ON mgmt.campaigns(tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_campaigns_system
    ON mgmt.campaigns(system) WHERE deleted_at IS NULL;
```

### `mgmt.campaign_lore`

Markdown documents for world-building content. Each document is a separate row
— supports long-form lore, NPC backstories, location descriptions, etc. These
feed into the knowledge graph and hot context assembly.

```sql
CREATE TABLE IF NOT EXISTS mgmt.campaign_lore (
    id              TEXT        PRIMARY KEY,  -- UUID
    campaign_id     TEXT        NOT NULL REFERENCES mgmt.campaigns(id) ON DELETE CASCADE,
    title           TEXT        NOT NULL,
    category        TEXT        NOT NULL DEFAULT 'general',
                                -- general, location, faction, history, rules, npc_backstory
    content         TEXT        NOT NULL,     -- markdown
    sort_order      INT         NOT NULL DEFAULT 0,
    metadata        JSONB       NOT NULL DEFAULT '{}',
    -- Example metadata:
    -- { "tags": ["rabenheim", "history"], "related_npcs": ["heinrich", "mathilde"] }
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX idx_campaign_lore_campaign
    ON mgmt.campaign_lore(campaign_id, sort_order) WHERE deleted_at IS NULL;
CREATE INDEX idx_campaign_lore_category
    ON mgmt.campaign_lore(campaign_id, category) WHERE deleted_at IS NULL;

-- Full-text search on lore content
CREATE INDEX idx_campaign_lore_fts
    ON mgmt.campaign_lore USING gin(to_tsvector('simple', title || ' ' || content))
    WHERE deleted_at IS NULL;
```

---

## Voice Management

### `mgmt.voice_samples`

Audio files uploaded by users for custom voice creation. Stored as references
to object storage (S3/MinIO) — the actual audio bytes live outside PostgreSQL.

```sql
CREATE TABLE IF NOT EXISTS mgmt.voice_samples (
    id              TEXT        PRIMARY KEY,  -- UUID
    tenant_id       TEXT        NOT NULL REFERENCES public.tenants(id),
    uploaded_by     TEXT        NOT NULL REFERENCES mgmt.users(id),
    filename        TEXT        NOT NULL,     -- original filename
    mime_type       TEXT        NOT NULL,     -- audio/wav, audio/mpeg, etc.
    size_bytes      BIGINT      NOT NULL,
    duration_ms     INT,                      -- audio duration, populated after analysis
    storage_path    TEXT        NOT NULL,     -- object storage key (e.g., "voices/samples/{tenant_id}/{id}.wav")
    status          TEXT        NOT NULL DEFAULT 'uploaded',
                                -- uploaded, processing, ready, failed
    error           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX idx_voice_samples_tenant
    ON mgmt.voice_samples(tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_voice_samples_uploaded_by
    ON mgmt.voice_samples(uploaded_by) WHERE deleted_at IS NULL;
```

### `mgmt.voice_profiles`

Custom voices created from uploaded samples via a TTS provider's voice cloning
API (e.g., ElevenLabs Instant Voice Cloning). Links back to the source samples
and stores the provider's voice ID for use in NPC definitions.

```sql
CREATE TABLE IF NOT EXISTS mgmt.voice_profiles (
    id                  TEXT        PRIMARY KEY,  -- UUID
    tenant_id           TEXT        NOT NULL REFERENCES public.tenants(id),
    created_by          TEXT        NOT NULL REFERENCES mgmt.users(id),
    name                TEXT        NOT NULL,     -- user-given name: "Gravelly Dwarf"
    description         TEXT        NOT NULL DEFAULT '',
    provider            TEXT        NOT NULL,     -- tts provider: "elevenlabs", "playht", etc.
    provider_voice_id   TEXT,                     -- provider's voice ID (set after creation succeeds)
    sample_ids          TEXT[]      NOT NULL DEFAULT '{}',  -- references voice_samples(id)
    status              TEXT        NOT NULL DEFAULT 'pending',
                                   -- pending, creating, ready, failed, deleted_remote
    provider_metadata   JSONB       NOT NULL DEFAULT '{}',
    -- Example: { "model_id": "eleven_multilingual_v2", "labels": {"accent": "german"} }
    error               TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at          TIMESTAMPTZ
);

CREATE INDEX idx_voice_profiles_tenant
    ON mgmt.voice_profiles(tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_voice_profiles_provider
    ON mgmt.voice_profiles(provider, provider_voice_id)
    WHERE provider_voice_id IS NOT NULL AND deleted_at IS NULL;
```

---

## Support

### `mgmt.support_tickets`

Lightweight support ticket system. Can serve as the primary system at launch
and later act as a sync target for a third-party tool (Zendesk, Linear, etc.)
via the `external_ref` field.

```sql
CREATE TABLE IF NOT EXISTS mgmt.support_tickets (
    id              TEXT        PRIMARY KEY,  -- UUID
    tenant_id       TEXT        NOT NULL REFERENCES public.tenants(id),
    user_id         TEXT        NOT NULL REFERENCES mgmt.users(id),
    subject         TEXT        NOT NULL,
    description     TEXT        NOT NULL,     -- markdown
    category        TEXT        NOT NULL DEFAULT 'general',
                                -- general, billing, bug, feature_request, voice, account
    priority        TEXT        NOT NULL DEFAULT 'normal',
                                -- low, normal, high, urgent
    status          TEXT        NOT NULL DEFAULT 'open',
                                -- open, in_progress, waiting_on_user, resolved, closed
    assigned_to     TEXT        REFERENCES mgmt.users(id),  -- admin/support user
    external_ref    TEXT,       -- third-party ticket ID (e.g., Zendesk, Linear)
    tags            TEXT[]      NOT NULL DEFAULT '{}',
    resolved_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX idx_support_tickets_tenant
    ON mgmt.support_tickets(tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_support_tickets_user
    ON mgmt.support_tickets(user_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_support_tickets_status
    ON mgmt.support_tickets(status) WHERE status NOT IN ('resolved', 'closed');
CREATE INDEX idx_support_tickets_assigned
    ON mgmt.support_tickets(assigned_to)
    WHERE assigned_to IS NOT NULL AND status NOT IN ('resolved', 'closed');
CREATE INDEX idx_support_tickets_category
    ON mgmt.support_tickets(category, created_at DESC)
    WHERE deleted_at IS NULL;
```

---

## Audit

### `mgmt.audit_log`

Append-only log of all write operations across the management service.
Never updated or deleted. Retention policy applied externally (e.g., pg_cron
partition pruning or DELETE WHERE created_at < now() - interval '2 years').

```sql
CREATE TABLE IF NOT EXISTS mgmt.audit_log (
    id              BIGSERIAL   PRIMARY KEY,
    tenant_id       TEXT,                     -- NULL for super_admin cross-tenant ops
    user_id         TEXT,                     -- NULL for system/webhook-triggered actions
    action          TEXT        NOT NULL,     -- e.g., "campaign.create", "npc.update", "subscription.cancel"
    resource_type   TEXT        NOT NULL,     -- e.g., "campaign", "npc", "subscription", "user"
    resource_id     TEXT        NOT NULL,
    changes         JSONB,                    -- {"field": {"old": "...", "new": "..."}} or NULL for creates/deletes
    ip_address      INET,
    user_agent      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- "Show me what happened to this resource"
CREATE INDEX idx_audit_log_resource
    ON mgmt.audit_log(resource_type, resource_id, created_at DESC);

-- "Show me what this user did"
CREATE INDEX idx_audit_log_user
    ON mgmt.audit_log(user_id, created_at DESC)
    WHERE user_id IS NOT NULL;

-- "Show me all actions for this tenant"
CREATE INDEX idx_audit_log_tenant
    ON mgmt.audit_log(tenant_id, created_at DESC)
    WHERE tenant_id IS NOT NULL;

-- "Show me recent audit entries" (dashboard, admin review)
CREATE INDEX idx_audit_log_created
    ON mgmt.audit_log(created_at DESC);

-- Time-based partitioning candidate for large deployments:
-- ALTER TABLE mgmt.audit_log PARTITION BY RANGE (created_at);
```

---

## Entity Relationship Diagram

```
                                    public schema (existing)
                                    ┌─────────────────┐
                              ┌────→│    tenants       │←──────────────────────┐
                              │     └────────┬────────┘                        │
                              │              │                                 │
                              │              │ (existing: sessions,            │
                              │              │  usage_records,                 │
                              │              │  npc_definitions)               │
                              │              │                                 │
                    mgmt schema│              │                                 │
┌─────────────────────────────┼──────────────┼─────────────────────────────────┤
│                             │              │                                 │
│  ┌──────────────┐     ┌─────┴────────┐     │     ┌────────────────────┐      │
│  │ user_prefs   │────→│    users      │─────┼────→│  subscriptions     │──────┤
│  └──────────────┘     └──────┬───────┘     │     └────────┬───────────┘      │
│                              │             │              │                  │
│  ┌──────────────┐            │             │     ┌────────┴───────────┐      │
│  │ user_sessions│←───────────┘             │     │  subscription_plans│      │
│  └──────────────┘                          │     └────────────────────┘      │
│                                            │                                │
│  ┌──────────────┐     ┌────────────────┐   │     ┌────────────────────┐      │
│  │campaign_lore │────→│   campaigns    │───┘     │  payment_events    │      │
│  └──────────────┘     └────────────────┘         └────────────────────┘      │
│                                                                             │
│  ┌──────────────┐     ┌────────────────┐         ┌────────────────────┐      │
│  │voice_samples │     │ voice_profiles │         │    invoices        │──────┘
│  └──────────────┘     └────────────────┘         └────────────────────┘
│                                                                             │
│  ┌──────────────┐     ┌────────────────┐                                    │
│  │support_tickets│    │  audit_log     │                                    │
│  └──────────────┘     └────────────────┘                                    │
│                                                                             │
│  ┌──────────────┐                                                           │
│  │usage_metering│────────────────────────────────────────────────────────────┘
│  └──────────────┘
└─────────────────────────────────────────────────────────────────────────────┘

Arrows show FK relationships (→ = references)
```

---

## Migration Strategy

### Approach

Use the same embedded migration pattern as the rest of Glyphoxa:

```
internal/gateway/mgmt/migrations/
├── 000001_create_schema.up.sql
├── 000001_create_schema.down.sql
├── 000002_user_tables.up.sql
├── 000002_user_tables.down.sql
├── 000003_billing_tables.up.sql
├── 000003_billing_tables.down.sql
├── 000004_campaign_tables.up.sql
├── 000004_campaign_tables.down.sql
├── 000005_voice_tables.up.sql
├── 000005_voice_tables.down.sql
├── 000006_support_audit.up.sql
└── 000006_support_audit.down.sql
```

Applied at gateway startup via `embed.FS` + sequential execution, matching the
existing `runMigrations()` / `runAdminMigrations()` pattern.

### Phase Rollout

**Phase 1 (MVP):** Migrations 000001-000004 only.
- Schema, campaigns, and minimal user table (API key auth still primary).
- `mgmt.users` created but only used for admin-created records initially.
- Billing tables created but Stripe integration not wired yet.

**Phase 2 (User Auth + Billing):** Migrations 000005, plus ALTER statements
for any Phase 1 schema adjustments.
- Discord/Google OAuth2 flows populate `mgmt.users`.
- Stripe webhooks populate `mgmt.payment_events` and update `mgmt.subscriptions`.
- Voice management tables created.

**Phase 3 (Support + Audit):** Migration 000006.
- Support ticket system.
- Audit logging (can be backfilled from application logs).

### Data Migration from Current State

The existing `public.tenants` rows need corresponding records in the new tables:

```sql
-- 1. Create campaigns from existing tenant campaign_id strings
INSERT INTO mgmt.campaigns (id, tenant_id, name, system, description)
SELECT DISTINCT
    t.campaign_id,              -- reuse existing campaign_id as PK
    t.id,
    t.campaign_id,              -- name = id initially, updated manually
    '',
    ''
FROM public.tenants t
WHERE t.campaign_id IS NOT NULL AND t.campaign_id != ''
ON CONFLICT (id) DO NOTHING;

-- 2. Create free-tier subscriptions for existing tenants
INSERT INTO mgmt.subscriptions (id, tenant_id, plan_id, status, current_period_start, current_period_end)
SELECT
    gen_random_uuid()::TEXT,
    t.id,
    CASE t.license_tier
        WHEN 'dedicated' THEN 'dm'
        ELSE 'apprentice'
    END,
    'active',
    date_trunc('month', now()),
    date_trunc('month', now()) + interval '1 month'
FROM public.tenants t
ON CONFLICT DO NOTHING;
```

---

## Common Query Patterns & Index Justification

| Query | Table | Index Used |
|-------|-------|------------|
| Login: find user by Discord ID | `users` | `idx_users_discord` |
| Login: find user by Google ID | `users` | `idx_users_google` |
| Dashboard: list users in tenant | `users` | `idx_users_tenant` |
| Auth: check JWT not revoked | `user_sessions` | `idx_user_sessions_active` |
| Billing: active subscription for tenant | `subscriptions` | `idx_subscriptions_tenant_active` |
| Billing: process Stripe webhook (dedup) | `payment_events` | `UNIQUE stripe_event_id` |
| Billing: list invoices for tenant | `invoices` | `idx_invoices_tenant` |
| Campaign: list campaigns for tenant | `campaigns` | `idx_campaigns_tenant` |
| Campaign: search lore content | `campaign_lore` | `idx_campaign_lore_fts` |
| Usage: session cost breakdown | `usage_metering` | `idx_usage_metering_tenant_period` |
| Support: open tickets for tenant | `support_tickets` | `idx_support_tickets_status` |
| Audit: resource history | `audit_log` | `idx_audit_log_resource` |
| Audit: user activity | `audit_log` | `idx_audit_log_user` |
| Cleanup: expired JWT sessions | `user_sessions` | `idx_user_sessions_expired` |

---

## Sizing Estimates (>1000 Users)

| Table | Rows at 1000 users | Growth rate | Notes |
|-------|-------------------|-------------|-------|
| `users` | 1,000 | Slow (signups) | Tiny table |
| `user_sessions` | ~50,000/year | Moderate (logins) | Prune expired monthly |
| `user_preferences` | 1,000 | Static | 1:1 with users |
| `subscription_plans` | 4-10 | Static | Seed data |
| `subscriptions` | ~1,500 | Slow (churn) | Includes historical |
| `payment_events` | ~12,000/year | Steady (billing events) | Append-only |
| `invoices` | ~12,000/year | Steady (monthly billing) | |
| `usage_metering` | ~50,000/year | Per session | Main growth table |
| `campaigns` | ~1,500 | Slow | |
| `campaign_lore` | ~15,000 | Moderate (content creation) | |
| `voice_samples` | ~2,000 | Slow (premium feature) | |
| `voice_profiles` | ~500 | Slow (premium feature) | |
| `support_tickets` | ~3,000/year | Moderate | |
| `audit_log` | ~500,000/year | Fast (all writes) | Partition if needed |

Total estimated storage: <2GB/year. PostgreSQL handles this trivially.

---

## Security Considerations

### Row-Level Security (Phase 2)

When user auth lands, enable RLS on tenant-scoped tables:

```sql
-- Set current tenant from JWT in application middleware:
-- SET LOCAL app.current_tenant_id = '<tenant_id>';

ALTER TABLE mgmt.campaigns ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON mgmt.campaigns
    USING (tenant_id = current_setting('app.current_tenant_id'));

-- Repeat for: users, subscriptions, invoices, usage_metering,
-- campaign_lore, voice_samples, voice_profiles, support_tickets
```

Super-admin bypasses RLS via `SET ROLE` or a separate connection without RLS.

### Sensitive Data

| Field | Protection |
|-------|-----------|
| `tenants.bot_token` | Encrypted via Vault Transit (existing) |
| `subscriptions.stripe_*_id` | Not secret, but only exposed to tenant_admin+ |
| `payment_events.payload` | Contains Stripe event JSON — restrict to super_admin |
| `invoices.hosted_invoice_url` | Contains Stripe session — restrict to tenant owner |
| `voice_samples.storage_path` | Presigned URL generation, never expose raw path |
| `user_sessions.ip_address` | PII — prune with expired sessions |

### Backup Strategy

The `mgmt` schema is included in the existing PostgreSQL backup (pg_dump of
the full database). No additional backup infrastructure needed.

For disaster recovery, the Stripe webhook replay mechanism can reconstruct
billing state from scratch — `payment_events` is the source of truth for
reconciliation.
