# Production Scaling & Multi-Tenant Deployment

Brainstorm: what architectural changes are needed to take Glyphoxa from a
single-guild alpha to a scalable, multi-tenant production deployment?

**Status:** Draft brainstorm -- not a design doc. Everything here is open for
discussion.

**Date:** 2026-03-04

---

## Table of Contents

1. [Current State Summary](#1-current-state-summary)
2. [Tenant Model & License Tiers](#2-tenant-model--license-tiers)
3. [Deployment Topology](#3-deployment-topology)
4. [Container Orchestration (Kubernetes)](#4-container-orchestration-kubernetes)
5. [Database Scaling & Tenant Isolation](#5-database-scaling--tenant-isolation)
6. [Secrets Management](#6-secrets-management)
7. [MCP Services: Shared vs Per-Tenant](#7-mcp-services-shared-vs-per-tenant)
8. [Discord Gateway Sharding](#8-discord-gateway-sharding)
9. [Observability at Scale](#9-observability-at-scale)
10. [Session Lifecycle & State](#10-session-lifecycle--state)
11. [Resource Planning & Autoscaling](#11-resource-planning--autoscaling)
12. [Provider Cost Management](#12-provider-cost-management)
13. [Control Plane](#13-control-plane)
14. [Networking & Traffic](#14-networking--traffic)
15. [CI/CD & Rollouts](#15-cicd--rollouts)
16. [Open Questions](#16-open-questions)

---

## 1. Current State Summary

Glyphoxa today is a **single-process, single-guild** application:

| Aspect | Current State | Production Gap |
|--------|---------------|----------------|
| **Tenant model** | No tenant concept | Need Tenant = Customer = License, deeply integrated into core |
| **Campaign isolation** | Single campaign, no `campaign_id` in data | Need `campaign_id` on all data tables, per-campaign query scoping |
| **Guild support** | One `guild_id` per process | Need multi-guild per tenant, license-controlled parallel sessions |
| **Sessions** | One active session per process (`SessionManager.active` bool) | Need license-aware session constraints (per-tier limits) |
| **Discord bot** | Single bot token, single gateway connection | Need multi-bot gateway (shared) and per-tenant gateway (dedicated) |
| **Database** | Shared PostgreSQL, no tenant/campaign columns | Need per-tenant schema (shared) or per-tenant instance (dedicated) |
| **Config** | Flat YAML, no tenant concept | Need per-tenant config stored in control plane |
| **Deployment** | Docker Compose, single container | Need Kubernetes with gateway + worker split |
| **Secrets** | API keys in YAML or env vars | Need Vault with per-tenant paths, BYOK support |
| **MCP tools** | All tools in-process or per-instance stdio subprocesses | Need shared stateless tools + tenant-scoped DB tools |

The roadmap (Phase 8) mentions Helm charts and managed cloud as future items
but no concrete design exists.

---

## 2. Tenant Model & License Tiers

### Core Definitions

**Tenant = Customer = License.** A tenant is a single paying customer who
holds one Glyphoxa license. This is the central billing entity, the primary
data isolation boundary, and the unit that owns Discord guilds, campaigns,
NPCs, and memory.

A tenant can connect Glyphoxa to **multiple Discord guilds** under a single
license. Campaign data is shared across those guilds (the same campaign can be
played in different guilds). What the license tier controls is how many
sessions and campaigns can run concurrently.

**Campaign** is the data boundary within a tenant. Each campaign has its own
NPCs, entities, knowledge graph, session transcripts, and memory. Campaigns
within a tenant share nothing except the tenant-level config (provider keys,
MCP servers, license metadata).

### License Tiers

| | Starter | Standard | Pro | Business |
|---|---|---|---|---|
| **Campaigns** | 1 (new campaign replaces old) | Multiple (parallel, DB-separated) | Multiple (parallel, DB-separated) | Multiple (parallel, DB-separated) |
| **Sessions** | 1 at a time, any guild | 1 at a time, any guild | 1 per guild, parallel across guilds | 1+ per guild (multi-bot) |
| **Campaign data on delete** | Wiped (export first) | Retained alongside new campaigns | Retained alongside new campaigns | Retained alongside new campaigns |
| **Discord guilds** | Unlimited (but 1 session) | Unlimited (but 1 session) | Unlimited | Unlimited |
| **Voice isolation** | Logical (shared process) | Logical (shared process) | Physical (dedicated instance) | Physical (dedicated instance) |
| **Gateway isolation** | Logical (shared gateway) | Logical (shared gateway) | Physical (dedicated gateway per bot) | Physical (dedicated gateway per bot) |
| **DB isolation** | Shared instance, per-tenant schema | Shared instance, per-tenant schema | Dedicated DB instance | Dedicated DB instance |
| **MCP services** | Shared | Shared | Shared (stateless) + dedicated (DB-access) | Shared (stateless) + dedicated (DB-access) |

### Session Constraints in Detail

Sessions are **always scoped to a single campaign**. Parallel sessions (Pro
and above) must be for **different campaigns** to avoid transcript merge
conflicts and knowledge graph inconsistencies. The control plane enforces
this:

```
Tenant (Starter): campaign A, guild 1 -> session OK
                   campaign A, guild 2 -> REJECTED (session already active)

Tenant (Standard): campaign A, guild 1 -> session OK
                   campaign B, guild 2 -> REJECTED (only 1 session allowed)

Tenant (Pro):      campaign A, guild 1 -> session OK
                   campaign B, guild 2 -> session OK (different campaign, different guild)
                   campaign A, guild 3 -> REJECTED (campaign A already has active session)
                   campaign C, guild 1 -> REJECTED (guild 1 already has active session)

Tenant (Business): campaign A, guild 1, bot 1 -> session OK
                   campaign B, guild 1, bot 2 -> session OK (multi-bot)
                   campaign A, guild 1, bot 2 -> REJECTED (campaign A already active)
```

### Isolation Philosophy

Discord voice transcripts are **highly sensitive data** -- real people's
voices, conversations, and role-play content. Cross-tenant data leakage is
unacceptable at any tier. The question is not *whether* to isolate, but *how
strongly*:

| Isolation Layer | Starter / Standard | Pro / Business |
|-----------------|--------------------|--------------------|
| **Voice processing** | Logical -- one Glyphoxa process may handle sessions from different tenants. Tenant context is threaded through the entire pipeline. Audio buffers, STT results, and LLM prompts are never mixed. | Physical -- dedicated Glyphoxa instance(s) per tenant. Process boundary guarantees isolation. |
| **Discord gateway** | Logical -- one gateway process serves multiple Discord bots (from different tenants). Events are routed by bot token / guild ID. | Physical -- dedicated gateway per Discord bot. No other tenant's events touch the same process. |
| **Database** | Shared PostgreSQL instance, but **per-tenant schema** or per-tenant partition. No shared tables. RLS as defense-in-depth. | Dedicated PostgreSQL instance per tenant. Contains all campaigns for that tenant. |
| **MCP tools** | Shared stateless services (dice, rules, web search). Memory tools run in the worker and inherit the worker's tenant-scoped DB connection. | Same as Starter/Standard for stateless tools. DB-accessing MCP tools get a **dedicated instance** pointing at the tenant's DB. |
| **Secrets** | Vault paths scoped by tenant ID. Shared Vault instance. | Same Vault instance, but tenant's secrets may include dedicated DB credentials, dedicated bot tokens, etc. |

### Why Tenant Must Be a Core Concept

The tenant ID cannot be a bolted-on afterthought. It must be integrated
deeply into the Glyphoxa core application because:

1. **Every database query** must be scoped to a tenant (schema or connection).
2. **Every log line and metric** must carry `tenant_id` for attribution.
3. **Session orchestration** must enforce per-tenant license constraints
   (max sessions, max campaigns, parallel rules).
4. **Memory tools** must never return data from another tenant's campaigns.
5. **Provider cost attribution** must be per-tenant for billing.
6. **Config hot-reload** must respect tenant boundaries (changing tenant A's
   NPC config must not affect tenant B).

**Implementation:** Add a `TenantContext` that flows through the application:

```go
type TenantContext struct {
    TenantID    string
    LicenseTier LicenseTier  // Starter, Standard, Pro, Business
    CampaignID  string       // active campaign for this session
    GuildID     string       // Discord guild for this session
}
```

This context is set at session creation time and propagated via `context.Value`
to every subsystem. The `SessionStore`, `KnowledgeGraph`, and `SemanticIndex`
interfaces gain a `TenantContext` parameter (or are constructed with one).

### Campaign Lifecycle by Tier

| Tier | Create Campaign | Delete Campaign | Data Retention |
|------|----------------|----------------|----------------|
| **Starter** | Creates new, **wipes previous** campaign data | Automatic on new campaign creation | Export before delete (JSON/YAML dump) |
| **Standard+** | Creates alongside existing campaigns | Manual via dashboard/API | Retained until explicit deletion or tenant offboarding |

The Starter tier's "wipe on new campaign" behaviour requires a **campaign
export** feature before any data is deleted. The export should include:
- All session transcripts (L1)
- Knowledge graph entities and relationships (L3)
- NPC definitions and personalities
- Campaign metadata

Export format: JSON archive or structured YAML, importable into a higher tier.

---

## 3. Deployment Topology

The deployment model is **not one-size-fits-all** -- it varies by license tier.
The core architecture is always **gateway + session worker**, but the degree
of sharing changes.

### Architecture: Gateway + Session Workers

All tiers use a split architecture:

- **Gateway:** Handles Discord gateway connections, slash command routing, and
  session orchestration. Always running (Discord bots must be "online").
- **Session Worker:** Runs the voice pipeline (VAD, STT, LLM, TTS, mixer).
  Created on `/session start`, destroyed on `/session stop`.

What differs per tier is whether gateways and workers are **shared** across
tenants or **dedicated**.

### Starter / Standard Tier: Shared Infrastructure

```
Shared Gateway Pool (N replicas, sharded by guild range):
    --> manages Discord bots for ALL Starter/Standard tenants
    --> receives slash commands, routes /npc, /entity, /campaign
    --> on /session start: enforces license limits, creates worker pod

Shared Worker Pool (ephemeral, 1 per active session):
    --> may run sessions from different tenants on the same node
    --> tenant isolation via TenantContext in application code
    --> connects to shared PostgreSQL (per-tenant schema)
    --> terminates on /session stop
```

```
┌─────────────────────────────────────────────────┐
│         Shared Gateway (N replicas)              │
│  Bot A (tenant 1) ──┐                           │
│  Bot B (tenant 2) ──┼── Discord Gateway Conn    │
│  Bot C (tenant 3) ──┘                           │
│                                                  │
│  Slash command router ── tenant lookup ──┐       │
│                                          v       │
│                              Session Scheduler   │
└──────────────────────────────────┬───────────────┘
                                   │ creates
                    ┌──────────────┼──────────────┐
                    v              v              v
              Worker (T1)    Worker (T2)    Worker (T3)
              campaign-a     campaign-x     campaign-m
              guild-101      guild-202      guild-303
```

- **Gateway resource:** ~10-20 MB per bot (Discord WebSocket + command state).
  A single gateway replica can manage hundreds of bots.
- **Worker resource:** ~200-300 MB per session (VAD + audio + goroutines).
  Workers from different tenants may be co-located on the same node.
- **Isolation:** Application-level via `TenantContext`. Shared PostgreSQL
  with per-tenant schemas. Shared MCP tools.

### Pro / Business Tier: Dedicated Infrastructure

```
Dedicated Gateway (per tenant, 1+ replicas):
    --> manages only this tenant's Discord bot(s)
    --> one gateway per bot token (Business: multiple bots per guild)
    --> on /session start: creates dedicated worker pod

Dedicated Workers (per tenant, 1 per active session):
    --> only this tenant's sessions
    --> connects to dedicated PostgreSQL instance
    --> dedicated DB-accessing MCP tool instances
```

```
┌───────────────────────────────┐
│  Dedicated Gateway (Tenant X) │
│  Bot X ── Discord Gateway     │
│  Slash command router         │
│  Session Scheduler            │
└──────────────┬────────────────┘
               │ creates
               v
         Worker (Tenant X)
         campaign-a, guild-501
               │
               v
     Dedicated PostgreSQL (Tenant X)
     ├── campaign_a schema
     └── campaign_b schema
```

- **Pro tier:** One bot per tenant, one gateway Deployment per tenant,
  parallel sessions across guilds (one per guild, different campaigns).
- **Business tier:** Multiple bots per tenant (multi-bot per guild), one
  gateway Deployment per bot, truly parallel voice streams.

### Why the Split?

The shared model keeps costs low for Starter/Standard tenants (TTRPG sessions
are typically weekly, 2-4 hours -- most tenants are idle most of the time).
The dedicated model gives Pro/Business tenants the strongest isolation
guarantees and dedicated resources they're paying for.

Both models use the **same Glyphoxa binary**. The difference is purely in
orchestration: the control plane decides whether to schedule a worker onto
a shared or dedicated node pool, and which database to connect it to.

### Cold Start & Slash Command Responsiveness

Since gateways are always running (shared or dedicated), slash commands always
respond instantly. Only session workers have a cold start path:

| Phase | Duration | Mitigation |
|-------|----------|------------|
| Pod scheduling | 1-3s | Pre-pull images, warm node pools |
| Glyphoxa boot + config load | <1s | Binary startup is fast (Go) |
| Discord voice channel join | 2-5s | Gateway pre-negotiates voice state |
| Provider initialization | 1-2s | Lazy init (connect on first use) |
| **Total cold start** | **4-10s** | User sees "Starting session..." in Discord |

This is acceptable for TTRPG sessions (the DM types `/session start` and
waits a few seconds). The gateway responds to the interaction immediately
with a deferred message, then edits it when the worker is ready.

### Business Tier: Multi-Bot Per Guild

Discord limits one outbound audio stream per bot per guild. Business tenants
that need multiple simultaneous NPC voices (true polyphony, not the priority
queue) require multiple bot accounts in the same guild:

```
Guild 123:
    Bot X (NPC: Greymantle) ── Worker A ── voice stream 1
    Bot Y (NPC: Bartok)     ── Worker B ── voice stream 2
```

This requires:
- Multiple bot tokens per tenant (registered separately with Discord)
- One gateway Deployment per bot token
- Coordination between workers for turn-taking and scene management
  (orchestrator must span workers -- shared state via PostgreSQL or gRPC)
- Each worker joins the same voice channel with a different bot

This is a **rare, custom-contract** scenario. The architecture supports it
(each bot is independent), but the orchestrator coordination adds complexity.

---

## 4. Container Orchestration (Kubernetes)

### Why Kubernetes?

- Native support for the gateway + worker topology across license tiers
- Pod autoscaling, resource limits, health probes (Glyphoxa already has
  `/healthz` and `/readyz`)
- Namespaces and node pools for tier-based physical isolation
- Secret injection via Vault CSI or external-secrets-operator
- Multi-cluster / multi-region for latency
- Well-understood by platform teams

### Kubernetes Resource Model by Tier

The license tier maps to Kubernetes resource boundaries:

```
Namespace: glyphoxa-shared
├── Deployment: glyphoxa-gateway-shared     (Starter/Standard bot gateway)
├── Job: session-tenant-abc-20260304        (Starter worker)
├── Job: session-tenant-def-20260304        (Standard worker)
├── Deployment: mcp-gateway-shared          (shared stateless MCP tools)
└── PgBouncer: pgbouncer-shared             (connection pooler)

Namespace: glyphoxa-tenant-xyz              (Pro tenant)
├── Deployment: glyphoxa-gateway-xyz        (dedicated gateway)
├── Job: session-xyz-campaign-a-20260304    (dedicated worker)
└── (PostgreSQL provisioned externally or via Crossplane)

Namespace: glyphoxa-tenant-megacorp         (Business tenant)
├── Deployment: glyphoxa-gateway-megacorp-bot1
├── Deployment: glyphoxa-gateway-megacorp-bot2
├── Job: session-megacorp-campaign-a-bot1
├── Job: session-megacorp-campaign-b-bot2
└── Deployment: mcp-memory-megacorp         (dedicated DB-access MCP)
```

### Deployment Models

#### Model 1: Plain Deployments + Jobs

```yaml
# Shared gateway for Starter/Standard tenants
kind: Deployment
metadata:
  name: glyphoxa-gateway-shared
  namespace: glyphoxa-shared
spec:
  replicas: 3  # sharded by guild range
  ...

# Session worker: created by gateway, one per active session
kind: Job
metadata:
  name: session-tenant-abc-20260304
  namespace: glyphoxa-shared
  labels:
    glyphoxa.io/tenant: abc
    glyphoxa.io/tier: starter
    glyphoxa.io/campaign: curse-of-strahd
spec:
  ttlSecondsAfterFinished: 300
  template:
    spec:
      containers:
      - name: glyphoxa-worker
        resources:
          requests: { cpu: "500m", memory: "256Mi" }
          limits:   { cpu: "2",    memory: "1Gi"   }
        env:
        - name: GLYPHOXA_TENANT_ID
          value: "abc"
        - name: GLYPHOXA_CAMPAIGN_ID
          value: "curse-of-strahd"
      restartPolicy: Never
```

- **Pros:** Simple, standard Kubernetes primitives, TTL controller handles
  cleanup.
- **Cons:** Gateway must programmatically create/delete Jobs via the Kubernetes
  API. License constraint enforcement lives in gateway code.

#### Model 2: Custom Operator (CRD: GlyphoxaSession)

A custom operator watches a `GlyphoxaSession` CRD and manages worker pods.
The operator is aware of license tiers and enforces constraints declaratively.

```yaml
apiVersion: glyphoxa.io/v1alpha1
kind: GlyphoxaSession
metadata:
  name: session-guild-123
  namespace: glyphoxa-shared        # or glyphoxa-tenant-xyz for Pro
spec:
  tenantID: abc
  licenseTier: standard             # operator enforces constraints
  campaignID: curse-of-strahd
  guildID: "123456789"
  channelID: "987654321"
  configRef:
    name: tenant-abc-config
  secretRef:
    name: tenant-abc-secrets
status:
  phase: Active                     # Provisioning -> Connecting -> Active -> Draining -> Completed
  workerPod: session-abc-20260304-xxxxx
  startedAt: "2026-03-04T19:00:00Z"
  sessionDuration: "2h15m"
```

The operator:
- **Enforces license constraints** before creating the worker:
  - Starter: reject if any session active for this tenant
  - Standard: reject if any session active for this tenant
  - Pro: reject if this campaign already has a session OR this guild already
    has a session
  - Business: reject only if this campaign already has a session
- Creates the worker pod in the correct namespace (shared or dedicated)
- Injects the right DB connection (shared instance + schema, or dedicated)
- Monitors health (restarts on crash, tracks session duration)
- Cleans up on session end or timeout
- Reports status back via CRD `.status`

- **Pros:** Declarative, Kubernetes-native lifecycle, license enforcement is
  auditable via CRD events, operator can manage both shared and dedicated
  topologies.
- **Cons:** Writing and maintaining an operator is significant effort.
  Use `kubebuilder` or `operator-sdk` to scaffold.

#### Model 3: Knative / Cloud Run (Serverless)

Use Knative Serving for session workers with scale-to-zero.

- **Verdict:** Poor fit. Glyphoxa sessions are long-running (hours), not
  request/response. Knative's concurrency model and cold-start behaviour work
  against the voice pipeline's requirements. **Not recommended.**

**Recommendation:** Start with **Model 1** (Deployments + Jobs) for the
initial launch. Invest in **Model 2** (custom operator) as soon as the
complexity of license constraint enforcement and multi-tier scheduling
justifies it -- likely around 50+ tenants or when the first Pro tier customer
onboards.

---

## 5. Database Scaling & Tenant Isolation

### The Problem

All tenants share one PostgreSQL instance today with no isolation. Voice
transcripts are highly sensitive data -- cross-tenant leakage is unacceptable.
The isolation strategy must match the license tier: shared-instance for
cost-efficient lower tiers, dedicated-instance for premium tiers.

### Two-Tier Database Architecture

The license tier determines the database topology:

| Tier | DB Topology | Campaign Isolation | Rationale |
|------|-------------|-------------------|-----------|
| **Starter / Standard** | Shared PostgreSQL instance, **per-tenant schema** | Per-campaign tables within the tenant schema | Cost-efficient, strong logical isolation |
| **Pro / Business** | **Dedicated PostgreSQL instance** per tenant | Per-campaign tables within the instance | Strongest isolation, independent scaling, no noisy neighbours |

### Starter / Standard: Schema-Per-Tenant on Shared Instance

Each tenant gets its own PostgreSQL schema within a shared database. Campaigns
are separated by a `campaign_id` column within the tenant's schema.

```sql
-- Tenant abc's schema
CREATE SCHEMA tenant_abc;

CREATE TABLE tenant_abc.session_entries (
    id           BIGSERIAL    PRIMARY KEY,
    campaign_id  TEXT         NOT NULL,
    session_id   TEXT         NOT NULL,
    speaker_id   TEXT         NOT NULL DEFAULT '',
    speaker_name TEXT         NOT NULL DEFAULT '',
    text         TEXT         NOT NULL,
    raw_text     TEXT         NOT NULL DEFAULT '',
    npc_id       TEXT         NOT NULL DEFAULT '',
    timestamp    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    duration_ns  BIGINT       NOT NULL DEFAULT 0
);

CREATE TABLE tenant_abc.chunks (
    id          TEXT         PRIMARY KEY,
    campaign_id TEXT         NOT NULL,
    session_id  TEXT         NOT NULL,
    content     TEXT         NOT NULL,
    embedding   vector(1536),
    ...
);

CREATE TABLE tenant_abc.entities (
    id          TEXT         PRIMARY KEY,
    campaign_id TEXT         NOT NULL,
    type        TEXT         NOT NULL,
    name        TEXT         NOT NULL,
    attributes  JSONB        NOT NULL DEFAULT '{}',
    ...
);

CREATE TABLE tenant_abc.relationships (
    source_id   TEXT  NOT NULL REFERENCES tenant_abc.entities (id) ON DELETE CASCADE,
    target_id   TEXT  NOT NULL REFERENCES tenant_abc.entities (id) ON DELETE CASCADE,
    campaign_id TEXT  NOT NULL,
    rel_type    TEXT  NOT NULL,
    ...
);

-- Indexes include campaign_id for query filtering
CREATE INDEX idx_session_entries_campaign ON tenant_abc.session_entries (campaign_id);
CREATE INDEX idx_chunks_campaign ON tenant_abc.chunks (campaign_id);
CREATE INDEX idx_entities_campaign ON tenant_abc.entities (campaign_id);
```

**Defense-in-depth with RLS:**

Even though schemas provide isolation, apply RLS as a safety net. Each worker
sets `app.tenant_id` on its connection:

```sql
-- Applied to the shared database role used by workers
ALTER TABLE tenant_abc.session_entries ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_abc.session_entries
    USING (current_setting('app.tenant_schema') = 'tenant_abc');
```

**Pros:**
- Strong logical isolation (schema = namespace boundary)
- PostgreSQL RBAC can grant per-schema access to dedicated DB roles
- `pg_dump --schema=tenant_abc` for per-tenant backup/export
- Dropping a schema cleanly removes all tenant data (offboarding)
- Indexes are per-schema, so tenant A's 100k vectors don't bloat tenant B's
  HNSW index

**Cons:**
- Schema creation/migration must iterate all tenants (automatable)
- Connection pool management: `SET search_path = tenant_xxx` per query or
  use a pool-per-schema (prefer the former to limit connections)
- Shared instance means noisy-neighbour risk for CPU/IO (mitigated by
  resource limits on the PostgreSQL side, e.g., `pg_cgroup` or cloud quotas)

### Pro / Business: Dedicated PostgreSQL Instance

Each Pro/Business tenant gets a fully separate PostgreSQL instance (managed
service like RDS/Cloud SQL, or a dedicated pod in Kubernetes).

```
Tenant X (Pro):
    PostgreSQL instance: db-tenant-x.internal
    ├── public schema (or named schema, single tenant so no namespace needed)
    │   ├── session_entries  (campaign_id column)
    │   ├── chunks           (campaign_id column)
    │   ├── entities         (campaign_id column)
    │   └── relationships    (campaign_id column)
    └── campaign isolation via campaign_id WHERE clause
```

**Pros:**
- Strongest isolation (separate process, separate storage)
- Independent scaling (tenant X can have a larger instance without affecting
  the shared pool)
- Independent backups, PITR, and maintenance windows
- No noisy-neighbour risk
- Simpler queries (no schema qualification needed)

**Cons:**
- Higher cost (~$15-50/month per managed PostgreSQL instance)
- More infrastructure to manage (automatable via Terraform/Crossplane)
- Connection routing: control plane must maintain a tenant -> DB endpoint map

### Campaign Isolation Within a Tenant

Regardless of DB topology, **campaign_id** is a mandatory column on all data
tables. This serves two purposes:

1. **Query scoping:** Every query includes `WHERE campaign_id = $1`. A session
   worker is bound to a single campaign and never sees data from other
   campaigns.
2. **Starter tier cleanup:** When a Starter tenant creates a new campaign, the
   old campaign's data is deleted:
   ```sql
   DELETE FROM tenant_abc.session_entries WHERE campaign_id = 'old-campaign';
   DELETE FROM tenant_abc.chunks WHERE campaign_id = 'old-campaign';
   DELETE FROM tenant_abc.entities WHERE campaign_id = 'old-campaign';
   DELETE FROM tenant_abc.relationships WHERE campaign_id = 'old-campaign';
   ```
   The export feature must run **before** this deletion.

### Vector Index Scaling

pgvector HNSW performance degrades around 5-10M vectors on a single index.
The schema-per-tenant model naturally helps (each tenant's `chunks` table has
its own HNSW index). Additional strategies:

| Approach | When | Trade-off |
|----------|------|-----------|
| Schema-per-tenant (already done) | Default | Each tenant has its own HNSW index (smaller, faster) |
| Partition chunks by campaign_id | Large tenants with many campaigns | Per-campaign HNSW, prevents cross-campaign index bloat |
| Partition by time | Log-like data in long-running campaigns | Old sessions in cold partitions, recent in hot |
| Dedicated vector DB | Very large scale | Qdrant/Weaviate behind the `SemanticIndex` interface |
| Dimensionality reduction | Cost savings | Smaller embedding models (768d vs 3072d) reduce index size |

### Connection Pooling

At scale, each Glyphoxa worker opens a pgx connection pool. Connection
management differs by tier:

**Starter / Standard (shared instance):**
- Centralised **PgBouncer** in transaction mode in front of the shared
  PostgreSQL instance
- Each worker uses 3-5 connections (voice pipeline is not DB-bound)
- PgBouncer handles `SET search_path` via `server_reset_query`
- 100 concurrent workers x 3 conns = 300 PgBouncer connections, mapped to
  ~50 actual PostgreSQL connections

**Pro / Business (dedicated instance):**
- Direct pgx pool to the dedicated instance (no PgBouncer needed for a
  single-tenant DB with few workers)
- MaxConns=5 per worker is sufficient

### Database Provisioning

The control plane must automate DB lifecycle:

| Event | Starter/Standard | Pro/Business |
|-------|------------------|--------------|
| Tenant created | `CREATE SCHEMA tenant_xxx; Migrate()` on shared instance | Provision new PostgreSQL instance (Terraform/Crossplane), run `Migrate()` |
| Campaign created | Insert into `campaigns` table within tenant schema | Same |
| Campaign deleted (Starter) | `DELETE FROM ... WHERE campaign_id = X` (after export) | N/A (Standard+) |
| Tenant offboarded | `DROP SCHEMA tenant_xxx CASCADE` | Destroy PostgreSQL instance |
| Schema migration | Iterate all tenant schemas on shared instance, run DDL | Run DDL on dedicated instance |

---

## 6. Secrets Management

### The Problem

Currently API keys live in `config.yaml` or environment variables. In a
multi-tenant SaaS:
- Each tenant may bring their own API keys (BYOK) or use shared platform keys
- Keys must be rotated without downtime
- Keys must not be readable by tenant code or exposed in logs
- Operators need audit trails for key access

### Options

#### Option A: HashiCorp Vault

Central Vault instance with a KV v2 secrets engine. Each tenant's secrets are
stored at a path like `secret/data/tenants/<tenant_id>/providers`.

```
secret/data/tenants/abc/providers/
    openai_api_key: sk-...
    deepgram_api_key: dg-...
    elevenlabs_api_key: el-...
    discord_bot_token: Bot MTIz...
```

Injection methods:
- **Vault Agent sidecar:** Injects secrets as files into the pod. Glyphoxa
  reads from file paths instead of env vars. Automatic rotation.
- **Vault CSI Provider:** Mounts secrets as a CSI volume. Similar to agent but
  uses the Kubernetes CSI driver.
- **Direct API:** Glyphoxa calls the Vault API at startup to fetch secrets.
  Requires Vault token management (Kubernetes auth method is simplest).

- **Pros:** Industry standard, fine-grained ACLs, audit logging, dynamic
  secrets (can generate short-lived database credentials), automatic rotation.
- **Cons:** Operational complexity (Vault is itself a distributed system that
  needs HA), learning curve.

#### Option B: Kubernetes Secrets + External Secrets Operator

Store secrets in Kubernetes Secrets. Use External Secrets Operator (ESO) to
sync from an external provider (AWS Secrets Manager, GCP Secret Manager,
Azure Key Vault, or Vault).

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: tenant-abc-secrets
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: tenant-abc-secrets
  data:
  - secretKey: OPENAI_API_KEY
    remoteRef:
      key: tenants/abc/providers
      property: openai_api_key
```

- **Pros:** Kubernetes-native (secrets appear as env vars or volume mounts),
  works with any backend via ESO, simpler than running Vault directly.
- **Cons:** Kubernetes Secrets are base64, not encrypted at rest by default
  (need to enable etcd encryption), ESO adds another component.

#### Option C: Cloud-native secrets (AWS/GCP/Azure)

Use the cloud provider's managed secrets service directly.

- **Pros:** Zero operational overhead, integrated with IAM, automatic rotation
  for some secret types.
- **Cons:** Cloud lock-in, doesn't work for self-hosted deployments.

**Recommendation:** **Option B** (External Secrets Operator) with Vault or a
cloud secret manager as the backend. This is Kubernetes-native, supports
multiple backends (self-hosted Vault for on-prem, cloud secrets for managed
deployments), and keeps the Glyphoxa code simple (just read env vars or
files).

### BYOK (Bring Your Own Key) Flow

All tiers support BYOK for provider API keys. Pro/Business tenants are
expected to BYOK (they control their own costs). Starter/Standard can
optionally BYOK or use platform-provided keys.

1. Tenant enters keys via a web dashboard or Discord DM to the bot
2. Keys are encrypted and stored in the secrets backend under the tenant's
   path: `secret/data/tenants/<tenant_id>/providers/<provider_name>`
3. The session worker's pod spec references the tenant's secret (via ESO
   ExternalSecret or Vault CSI)
4. At startup, Glyphoxa reads keys from env vars / files
5. Keys are never logged, never stored in the config YAML

**Resolution order:** `tenant BYOK key > platform default key > env var`

### Per-Tier Secret Scope

| Secret | Starter / Standard | Pro / Business |
|--------|--------------------|--------------------|
| Discord bot token | Vault: `tenants/<id>/discord` | Vault: `tenants/<id>/discord` (+ additional bot tokens for Business) |
| LLM/STT/TTS API keys | Vault: `tenants/<id>/providers/` (BYOK or platform) | Vault: `tenants/<id>/providers/` (BYOK expected) |
| DB credentials | Shared DB role, schema-scoped | Dedicated DB credentials per tenant |
| MCP tool auth | Shared MCP gateway token | Per-tenant MCP auth tokens |

---

## 7. MCP Services: Shared vs Per-Tenant

### The Problem

MCP tools run as either in-process Go handlers (built-ins) or external
processes (stdio/HTTP). The key distinction for multi-tenancy is whether a
tool **accesses tenant data** (database, memory) or is **stateless** (dice,
rules, web search). This distinction drives the isolation model.

### Tool Classification

| Category | Examples | Tenant Data? | Shareable? |
|----------|----------|-------------|------------|
| **Stateless built-in** | `roll`, `roll_table`, `search_rules`, `get_rule` | No | Yes -- safe to share across all tenants |
| **Stateless external** | Web search, image generation | No (but may use per-tenant API keys) | Yes -- shared service, per-tenant auth |
| **DB-accessing built-in** | `search_sessions`, `query_entities`, `get_summary`, `search_facts` | Yes -- reads tenant memory | Depends on tier |
| **DB-accessing external** | Custom MCP servers that query tenant data | Yes | Depends on tier |
| **File I/O** | `read_file`, `write_file` | Yes -- tenant's sandboxed directory | No -- always per-worker |

### Architecture by Tier

#### All Tiers: Shared Stateless MCP Gateway

A centralized MCP gateway runs stateless tool servers that are safe to share.
All workers connect via streamable-http:

```
Worker A (tenant 1) ──┐
Worker B (tenant 2) ──┼──> Shared MCP Gateway ──> dice-roller
Worker C (tenant 3) ──┘                       ──> rules-lookup
                                               ──> web-search (per-tenant API key via auth header)
```

- No tenant data touches these tools, so sharing is safe.
- Web search and image gen tools use per-tenant API keys passed in the
  request auth header (the gateway routes to the right upstream key based on
  the tenant ID in the request).
- The gateway is a Deployment with 2+ replicas for HA.

#### All Tiers: In-Process Built-In Tools

Stateless built-in tools (dice, rules) also run in-process in the worker.
This is the **default** for latency-critical FAST-tier tools. The shared
gateway is a fallback for tools that are too heavy or too numerous to embed
in every worker.

Both paths coexist: in-process tools have zero network overhead, gateway tools
add ~1-5ms but save per-worker resources.

#### Starter / Standard: DB-Accessing Tools In-Process (Shared DB)

Memory tools (`search_sessions`, `query_entities`, etc.) run **in-process** in
the session worker. They inherit the worker's database connection, which is
scoped to the tenant's schema:

```go
// Worker startup for Starter/Standard tenant
pool := connectToSharedDB()
pool.Exec(ctx, "SET search_path = tenant_abc")

memoryTools := memorytool.NewTools(store.L1(), store.L2(), store)
// These tools automatically query within tenant_abc schema
```

Since Starter/Standard workers already connect to the shared PostgreSQL
instance with a tenant-scoped schema, the memory tools are isolated by
construction. No separate MCP service needed.

#### Pro / Business: Dedicated DB-Accessing MCP Instances

Pro/Business tenants have a **dedicated PostgreSQL instance**. DB-accessing
MCP tools must connect to this dedicated instance, not the shared one.

Two options:

**Option A: In-process (same as lower tiers)**

Memory tools still run in-process in the worker. The worker's DB connection
points to the dedicated instance. Simple, no extra infrastructure.

```
Worker (Tenant X) ──> dedicated PostgreSQL (Tenant X)
    └── in-process memory tools query dedicated DB
```

**Option B: Dedicated MCP service per tenant**

A separate MCP server pod per tenant runs the DB-accessing tools. This is
useful when the tenant has custom MCP tools that need DB access, or when
multiple workers (parallel sessions) should share a tool cache:

```
Worker A (Tenant X, campaign 1) ──┐
Worker B (Tenant X, campaign 2) ──┼──> MCP-Memory-TenantX ──> dedicated PostgreSQL
```

```yaml
# Dedicated MCP service for tenant X
kind: Deployment
metadata:
  name: mcp-memory-tenant-x
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: mcp-memory
        image: ghcr.io/mrwong99/glyphoxa-mcp-memory:latest
        env:
        - name: POSTGRES_DSN
          valueFrom:
            secretKeyRef:
              name: tenant-x-db-credentials
              key: dsn
```

**Recommendation:** Start with **Option A** (in-process) for all tiers. Only
move to **Option B** (dedicated MCP service) for Business tenants with
custom tool requirements or multiple parallel sessions that benefit from
shared tool state/caching.

### Per-Tenant Custom MCP Servers

Business tenants may bring their own MCP tool servers (custom game
integrations, VTT connectors, etc.). These run as **sidecar containers** in
the worker pod or as **tenant-managed external HTTP endpoints**:

```yaml
# Business tenant's custom MCP tool as sidecar
spec:
  containers:
  - name: glyphoxa-worker
    ...
  - name: mcp-custom-vtt
    image: tenant-x/foundry-connector:latest
    env:
    - name: FOUNDRY_API_KEY
      valueFrom:
        secretKeyRef: ...
```

Alternatively, the tenant hosts the MCP server externally and provides the
streamable-http URL in their Glyphoxa config. The worker connects to it via
the standard MCP HTTP transport with tenant-provided auth.

### Summary: MCP Isolation Matrix

| Tool Type | Starter / Standard | Pro / Business |
|-----------|--------------------|--------------------|
| Stateless (dice, rules) | In-process + shared gateway | In-process + shared gateway |
| Web search, image gen | Shared gateway (per-tenant API key) | Shared gateway (per-tenant API key) |
| Memory tools (DB-access) | In-process, shared DB (tenant schema) | In-process, dedicated DB |
| File I/O | In-process, per-worker sandbox | In-process, per-worker sandbox |
| Custom tenant tools | Not supported | Sidecar or external HTTP |

---

## 8. Discord Gateway & Bot Management

### The Problem

Each tenant brings their own Discord bot token (or Glyphoxa provides one).
The gateway must manage potentially thousands of bot tokens, each maintaining
a Discord gateway WebSocket connection. At scale, this intersects with
Discord's sharding requirements.

### Bot Token Ownership Model

Each tenant has **one Discord bot application** (Starter through Pro). The
tenant registers the bot on Discord's developer portal and provides the token
to Glyphoxa. Business tenants may have multiple bots (one per NPC voice
stream).

| Tier | Bot Tokens | Registered By |
|------|-----------|---------------|
| **Starter** | 1 | Tenant (or Glyphoxa-managed) |
| **Standard** | 1 | Tenant (or Glyphoxa-managed) |
| **Pro** | 1 | Tenant |
| **Business** | N (one per concurrent voice stream) | Tenant |

**Open question:** Should Glyphoxa offer a "managed bot" option where we
provide the bot token (simpler onboarding) vs always requiring tenants to
bring their own bot (BYOB)? Managed bots are simpler for users but create
a SPOF (Glyphoxa's bot application gets rate-limited or banned, all tenants
are affected). BYOB isolates blast radius.

### Shared Gateway: Multi-Bot Process

The shared gateway (Starter/Standard) manages **multiple bot tokens** in a
single process. Each bot token opens its own Discord gateway WebSocket
connection. disgo supports multiple client instances in the same process:

```
Shared Gateway Pod:
    Bot Client (tenant abc, token A) ── Discord WS ── guilds [101, 102]
    Bot Client (tenant def, token B) ── Discord WS ── guilds [201]
    Bot Client (tenant ghi, token C) ── Discord WS ── guilds [301, 302, 303]
```

Events are demultiplexed by bot client -> tenant lookup -> command routing.

**Scaling the shared gateway:**
- Each bot WebSocket uses ~5-10 MB memory + minimal CPU (event-driven).
- A single gateway pod can handle ~200-500 bot connections.
- Scale horizontally by assigning bot token ranges to gateway replicas.
- Use a consistent hash or control plane assignment to map tokens to pods.

### Dedicated Gateway: Per-Bot Process (Pro/Business)

Pro/Business tenants get one gateway Deployment per bot token. This is
the same Glyphoxa gateway binary, just configured with a single bot token:

```yaml
kind: Deployment
metadata:
  name: glyphoxa-gateway-tenant-xyz
  namespace: glyphoxa-tenant-xyz
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: glyphoxa-gateway
        env:
        - name: GLYPHOXA_MODE
          value: gateway
        - name: GLYPHOXA_TENANT_ID
          value: xyz
        - name: DISCORD_BOT_TOKEN
          valueFrom:
            secretKeyRef:
              name: tenant-xyz-secrets
              key: discord_bot_token
```

### Discord Sharding (Per-Bot)

Discord sharding applies **per bot token**, not per Glyphoxa instance. A
single bot token needs sharding when it's in 2500+ guilds. This is unlikely
for individual tenants (one customer rarely has 2500 Discord servers), but
could apply to a Glyphoxa-managed shared bot.

If sharding is needed:

| Approach | Description | When |
|----------|-------------|------|
| **disgo built-in sharding** | Single process, multiple shards internally | < 10 shards, simple |
| **One shard per pod** | Deploy N pods, each with a shard ID range | 10-50 shards |
| **External shard orchestrator** | Dedicated coordinator + message queue | 50+ shards |

**Recommendation:** Don't build sharding support until a single bot token
approaches 2500 guilds. For Starter/Standard, the shared gateway's multi-bot
model means each bot token only serves one tenant's guilds (typically 1-5).
Sharding is a non-issue until very large scale.

### Gateway Responsibilities

The gateway handles everything that does **not** require the voice pipeline:

| Function | In Gateway | In Worker |
|----------|-----------|-----------|
| Discord gateway WebSocket | Yes | No |
| Slash command handling (`/session`, `/npc`, `/entity`, `/campaign`) | Yes | No |
| Session lifecycle (start, stop, status) | Yes (orchestrates) | Yes (executes) |
| Voice channel join/leave | No | Yes |
| VAD, STT, LLM, TTS pipeline | No | Yes |
| NPC CRUD (create, update, delete) | Yes | No |
| Campaign management | Yes | No |
| License constraint enforcement | Yes | No |

---

## 9. Observability at Scale

### Current State

Glyphoxa already has solid observability (OpenTelemetry traces, Prometheus
metrics, Grafana dashboards, structured logging with trace correlation). The
question is how to scale this for multi-tenant production.

### What Needs to Change

#### Tenant-scoped metrics

Add `tenant_id` and `license_tier` labels to key metrics. Add `campaign_id`
where relevant (session and provider metrics):

```
glyphoxa_active_sessions{tenant_id="abc", license_tier="standard", campaign_id="cos"}
glyphoxa_stt_duration_seconds{tenant_id="abc", campaign_id="cos"}
glyphoxa_provider_requests_total{tenant_id="abc", provider="openai", kind="llm"}
glyphoxa_session_duration_seconds{tenant_id="abc", license_tier="standard"}
```

**Cardinality concern:** At 1000 tenants x 3 campaigns avg x 6 metric
families = ~18k time series. Manageable for Prometheus, but keep `campaign_id`
off high-frequency metrics (e.g., per-frame VAD) to avoid explosion.

#### Centralized log aggregation

- **Option A:** Loki + Promtail (Grafana ecosystem, label-based, integrates
  with existing Grafana dashboards)
- **Option B:** Elasticsearch + Fluentd/Filebeat (full-text search,
  more powerful queries, higher resource usage)
- **Option C:** Cloud-native (CloudWatch, Cloud Logging, Datadog)

**Recommendation:** Loki. It integrates with the existing Grafana stack, uses
labels for tenant filtering (`{tenant_id="abc"}`), and is significantly
cheaper than Elasticsearch for log storage.

#### Distributed tracing

Add `tenant_id` as a span attribute. Use trace sampling (10% in production)
to control costs. Export to Tempo (Grafana ecosystem) or Jaeger.

#### Per-tenant dashboards

Either:
- Grafana with tenant variable filter (single dashboard, `$tenant_id` template
  variable)
- Grafana Organizations (one per tenant, full isolation, but heavy at scale)

#### Alerting

Per-tenant alerts for:
- Session failures (crash, disconnect without reconnect)
- Provider error spikes (tenant's API keys may be exhausted/revoked)
- Latency degradation (mouth-to-ear > 2s threshold)
- Quota approaching limit (session hours, token budget)
- License expiry warning

Global alerts for:
- Cluster resource exhaustion
- Shared PostgreSQL connection pool saturation
- Shared gateway bot connection failures
- Dedicated infrastructure health (Pro/Business PostgreSQL instances)
- Control plane API latency

---

## 10. Session Lifecycle & State

### The Problem

Today `SessionManager` is an in-process singleton with a boolean `active`
flag. In a distributed, multi-tenant deployment, session state must be:
- Managed across gateway and worker processes
- Queryable for license constraint enforcement (e.g., "does this tenant
  already have an active session?")
- Scoped by tenant, campaign, and guild

### Session State Machine

```
[Idle] --/session start--> [Validating] --license OK--> [Provisioning]
    --pod ready--> [Connecting] --voice joined--> [Active]
    --/session stop--> [Draining] --drained--> [Completed]
                           |
                           +--crash--> [Recovering] --reconnect--> [Active]
                           +--idle timeout--> [Draining]
                           +--max duration--> [Draining]
```

The **Validating** state is new -- this is where the gateway checks license
constraints before provisioning a worker:

```
/session start (tenant=abc, campaign=X, guild=101)
    --> Query: active sessions WHERE tenant_id = 'abc'
    --> Check license tier constraints:
        Starter:  any active session? -> REJECT
        Standard: any active session? -> REJECT
        Pro:      active session for campaign X? -> REJECT
                  active session on guild 101? -> REJECT
        Business: active session for campaign X? -> REJECT
    --> All checks pass -> Provisioning
```

### Session State Table

**Recommendation:** PostgreSQL for durable session state (already a
dependency). The `sessions` table lives in a **shared control-plane database**
(not in per-tenant schemas) because the gateway needs to query across all
tenants for scheduling and monitoring.

```sql
CREATE TABLE sessions (
    id           TEXT         PRIMARY KEY,
    tenant_id    TEXT         NOT NULL,
    campaign_id  TEXT         NOT NULL,
    guild_id     TEXT         NOT NULL,
    channel_id   TEXT         NOT NULL DEFAULT '',
    license_tier TEXT         NOT NULL,
    state        TEXT         NOT NULL DEFAULT 'validating',
    worker_pod   TEXT,
    worker_node  TEXT,
    started_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    ended_at     TIMESTAMPTZ,
    last_voice   TIMESTAMPTZ,       -- last voice activity (for idle timeout)
    metadata     JSONB        DEFAULT '{}',

    -- Enforce: no two active sessions for the same campaign
    CONSTRAINT unique_active_campaign
        EXCLUDE USING gist (
            campaign_id WITH =,
            tstzrange(started_at, ended_at, '[)') WITH &&
        ) WHERE (state NOT IN ('completed', 'failed'))
);

CREATE INDEX idx_sessions_tenant ON sessions (tenant_id);
CREATE INDEX idx_sessions_state ON sessions (state) WHERE state NOT IN ('completed', 'failed');
CREATE INDEX idx_sessions_guild ON sessions (guild_id);
```

The exclusion constraint `unique_active_campaign` prevents two active sessions
for the same campaign at the database level -- a safety net beyond the
application-level check.

### State Change Notification

The gateway needs to know when a worker transitions state (e.g., Connecting ->
Active). Options:

| Option | Latency | Complexity |
|--------|---------|------------|
| PostgreSQL LISTEN/NOTIFY | ~10ms | Low (native, no extra deps) |
| Worker -> gateway gRPC callback | ~1ms | Medium (gRPC service in gateway) |
| Kubernetes CRD status (if using operator) | ~1s | Low (operator handles it) |

**Recommendation:** PostgreSQL LISTEN/NOTIFY for the initial implementation.
The worker updates the `sessions` row and issues `NOTIFY session_state_change`.
The gateway listens and reacts. If using the custom operator (Model 2), CRD
status is the natural notification mechanism.

### Session Timeouts

| Timeout | Default | Configurable? | Per-Tier Override? |
|---------|---------|--------------|-------------------|
| **Idle timeout** (no voice activity) | 15 min | Yes | Starter: 10min, Pro: 30min |
| **Max duration** | 8 hours | Yes | Starter: 4h, Standard: 8h, Pro/Business: 12h |
| **Disconnect grace period** | 5 min | Yes | Same across tiers |
| **Cold start timeout** | 60s | Yes | If worker doesn't reach Active in 60s, fail |

### Billing Integration

The `sessions` table doubles as the billing source of truth:

```sql
-- Monthly session hours per tenant
SELECT tenant_id,
       SUM(EXTRACT(EPOCH FROM (COALESCE(ended_at, now()) - started_at)) / 3600)
           AS total_hours
FROM sessions
WHERE started_at >= date_trunc('month', now())
GROUP BY tenant_id;
```

Combined with provider cost metrics (`glyphoxa_provider_requests_total`),
this gives per-tenant cost attribution for both compute and API usage.

---

## 11. Resource Planning & Autoscaling

### Per-Session Resource Profile

Based on the deployment docs and architecture:

| Component | CPU | Memory | Notes |
|-----------|-----|--------|-------|
| Glyphoxa worker (cloud providers) | 0.5-1 core | 200-300 MB | VAD + audio pipeline + goroutines |
| Glyphoxa worker (local whisper) | 2-4 cores | 500 MB-1 GB | In-process STT adds CPU load |
| Glyphoxa worker (local LLM/Ollama) | 4-8 cores | 4-16 GB | Depends on model size |
| PostgreSQL connection | - | ~10 MB per conn | pgx pool, 3-5 conns per worker |
| Discord gateway WebSocket | minimal | ~5 MB | Long-lived connection |
| MCP stdio subprocess | 0.1 core | 50-100 MB | Per external tool server |

### Autoscaling Strategies

#### Option A: Horizontal Pod Autoscaler (HPA) on custom metrics

Scale gateway pods based on active sessions or connected guilds:

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: glyphoxa-gateway
  minReplicas: 1
  maxReplicas: 10
  metrics:
  - type: Pods
    pods:
      metric:
        name: glyphoxa_active_sessions
      target:
        type: AverageValue
        averageValue: "5"
```

- Session workers don't need HPA -- they're created/destroyed per session.

#### Option B: Kubernetes Event-Driven Autoscaling (KEDA)

KEDA can scale based on external metrics (Prometheus, PostgreSQL row count,
message queue depth):

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
spec:
  scaleTargetRef:
    name: glyphoxa-gateway
  triggers:
  - type: prometheus
    metadata:
      serverAddress: http://prometheus:9090
      metricName: glyphoxa_active_sessions
      threshold: "5"
      query: sum(glyphoxa_active_sessions)
```

#### Option C: Custom autoscaler in the control plane

The control plane tracks session counts and guild assignments, creating/scaling
gateway and worker pods directly via the Kubernetes API.

- **Pros:** Full control, can implement tenant-aware scheduling (e.g., co-locate
  a tenant's gateway and worker on the same node for lower latency).
- **Cons:** Reinventing autoscaler logic.

**Recommendation:** **Option B** (KEDA) for shared gateway scaling. Session
workers don't need autoscaling -- they're created on-demand and cleaned up on
session end. Dedicated gateways (Pro/Business) are static Deployments with
replicas=1 (or 2 for HA on Business tier).

### Tier-Aware Node Scheduling

Use Kubernetes node pools and scheduling constraints to separate shared and
dedicated workloads:

```yaml
# Shared node pool: Starter/Standard workers + shared gateway
nodePool: shared-workers
  taints:
  - key: glyphoxa.io/tier
    value: shared
    effect: NoSchedule

# Dedicated node pool: Pro/Business workers
nodePool: dedicated-workers
  taints:
  - key: glyphoxa.io/tier
    value: dedicated
    effect: NoSchedule
```

Worker pods include the matching tolerations and node affinity. This ensures
Pro/Business tenants' workers never share nodes with Starter/Standard tenants,
providing physical isolation at the compute layer.

### GPU Node Scheduling

For tenants using local inference (whisper.cpp, Ollama, Coqui):
- Use Kubernetes node affinity or taints to schedule GPU-requiring workers on
  GPU nodes
- NVIDIA GPU Operator for GPU resource management
- Consider time-sharing GPUs (MIG on A100, or MPS) for smaller models
- GPU workers are likely Pro/Business only (local inference is expensive)

```yaml
resources:
  limits:
    nvidia.com/gpu: 1
nodeSelector:
  gpu-type: "t4"
tolerations:
- key: glyphoxa.io/tier
  value: dedicated
  effect: NoSchedule
```

---

## 12. Provider Cost Management

### The Problem

In a multi-tenant SaaS, provider API costs (LLM, STT, TTS, embeddings) are
the primary operational expense. Need per-tenant tracking and controls.

### Cost Attribution

Each provider call must be tagged with `tenant_id` for cost allocation:

```go
m.RecordProviderRequest(ctx, "openai", "llm", "ok")
// --> glyphoxa_provider_requests_total{tenant_id="abc", provider="openai", kind="llm"} +1
```

Use PromQL to compute per-tenant costs:

```
# Approximate OpenAI cost per tenant per hour
sum(rate(glyphoxa_provider_requests_total{provider="openai"}[1h])) by (tenant_id)
  * $cost_per_request
```

### Rate Limiting & Quotas

| Control | Starter | Standard | Pro | Business |
|---------|---------|----------|-----|----------|
| **Max concurrent sessions** | 1 | 1 | 1 per guild | Custom |
| **Max session duration** | 4h | 8h | 12h | Custom |
| **Max campaigns** | 1 (replace) | Unlimited | Unlimited | Unlimited |
| **Monthly session hours** | 20h | 60h | Unlimited | Custom |
| **LLM token budget** | Per-session cap | Monthly cap | Unlimited (BYOK) | Custom |
| **Provider rate limit** | Shared pool | Shared pool | Per-tenant | Per-tenant |
| **Overage handling** | Block new sessions | Warn, then degrade to cheaper model | N/A (BYOK) | Custom SLA |

### BYOK vs Platform Keys

| Model | Description | Who Pays |
|-------|-------------|----------|
| **Platform keys** | Glyphoxa provides API keys, costs are baked into subscription | Glyphoxa |
| **BYOK** | Tenant provides their own API keys | Tenant |
| **Hybrid** | Platform provides defaults, tenant can override with own keys | Split |

**Recommendation:** Support both. Platform keys for managed tier, BYOK for
self-hosted and enterprise. The config resolution order:
`tenant BYOK key > platform default key > env var fallback`.

---

## 13. Control Plane

### What It Does

The control plane is the management layer that sits between customers and the
Glyphoxa runtime:

```
Customer Dashboard / API
        |
        v
Control Plane (stateless API service)
    |           |           |
    v           v           v
Kubernetes   PostgreSQL    Vault
(workers)    (state/config) (secrets)
```

### Responsibilities

| Function | Description |
|----------|-------------|
| **Tenant & license management** | CRUD tenants, map Discord guilds, manage licenses (tier, limits, expiry) |
| **Config management** | Store per-tenant Glyphoxa configs (NPCs, campaigns, providers) |
| **Campaign lifecycle** | Create/delete campaigns, enforce Starter single-campaign policy (export then wipe), manage campaign-level NPC definitions |
| **Session orchestration** | Validate license constraints, create/monitor/destroy session worker pods, enforce concurrent-session limits |
| **Infrastructure provisioning** | Provision dedicated resources for Pro/Business: gateway Deployments, PostgreSQL instances, MCP services |
| **Discord interaction proxy** | Receive Discord webhooks, route to appropriate gateway/worker |
| **Billing integration** | Track session hours + provider API usage per tenant, enforce quotas, integrate with payment provider |
| **Secret management** | Store/rotate API keys in Vault, inject into worker pods, manage BYOK key submission |
| **Health monitoring** | Track worker/gateway health, auto-recover crashed sessions, report per-tenant SLA metrics |
| **Data lifecycle** | Tenant offboarding (data export + deletion), campaign export, GDPR deletion requests |

### Build vs Buy

| Component | Build | Buy/Use |
|-----------|-------|---------|
| Tenant management | Custom API | Auth0/Clerk for user auth, custom for tenant mapping |
| Config storage | PostgreSQL table | - |
| Session orchestration | Custom controller or Kubernetes operator | - |
| Discord proxy | Custom gateway service | - |
| Billing | Custom usage tracking | Stripe for payments, custom for usage metering |
| Secret management | Vault integration | HashiCorp Vault or cloud KMS |
| Monitoring | Grafana stack | - |

---

## 14. Networking & Traffic

### Ingress & Load Balancing

```
Internet
    |
    v
Cloud LB / Ingress Controller (nginx, Traefik, Envoy)
    |
    +---> /api/*        --> Control Plane service
    +---> /metrics      --> Prometheus (internal only)
    +---> /healthz      --> Health checks
    +---> /ws/session/* --> Session workers (if WebRTC)
```

Discord bot traffic does **not** go through ingress -- it's outbound
WebSocket from the gateway pods to Discord's gateway.

### Network Policies

- Workers can reach: PostgreSQL, Vault, shared MCP gateway, external APIs
  (LLM/STT/TTS)
- Workers cannot reach: other workers, control plane admin endpoints
- Gateway pods can reach: Kubernetes API (to create workers), PostgreSQL,
  Discord API
- MCP gateway can reach: external tool APIs
- PostgreSQL: only reachable from workers, gateways, and control plane

### DNS & Service Discovery

- Kubernetes Services for internal routing (gateway, MCP gateway, PostgreSQL)
- ExternalDNS for public endpoints (control plane API, web dashboard)
- CoreDNS for intra-cluster resolution

### WebRTC Considerations

If using the WebRTC audio platform (browser-based sessions without Discord):
- TURN/STUN servers needed (self-hosted coturn or managed like Twilio)
- Workers need a publicly reachable UDP port (or use TURN relay)
- LoadBalancer service per worker, or use a shared TURN relay

---

## 15. CI/CD & Rollouts

### Deployment Strategy

| Strategy | Description | Risk | Downtime |
|----------|-------------|------|----------|
| **Rolling update** | Kubernetes default, pods replaced one at a time | Low | Zero (for gateway). Workers are ephemeral. |
| **Blue-green** | Two full deployments, traffic switched atomically | Very low | Zero |
| **Canary** | New version serves a percentage of tenants first | Very low | Zero |

**Recommendation:** **Rolling update** for gateways (stateless). Session
workers are ephemeral Jobs -- new sessions use the new image, existing
sessions keep running on the old image until they end.

### Image Build Pipeline

```
Push to main
    --> CI builds multi-arch image (amd64 + arm64)
    --> Push to GHCR
    --> Update Helm chart values (image tag)
    --> ArgoCD / Flux syncs to cluster
    --> Rolling update of gateway pods
    --> New sessions use new worker image
```

### Database Migrations

Glyphoxa uses idempotent DDL (`CREATE TABLE IF NOT EXISTS`). For additive
changes (new columns, new tables), migrations run automatically on worker
startup. For breaking changes (column renames, type changes):

- Use a migration tool (golang-migrate, Atlas, goose)
- Run migrations as a Kubernetes Job before the deployment rolls out
- Schema-per-tenant requires iterating all schemas

---

## 16. Open Questions

### Architecture

- [ ] Should the gateway be a new binary (`cmd/glyphoxa-gateway/`) or a mode
  of the existing binary (`glyphoxa --mode=gateway`)? Mode flag is simpler to
  build and ship; separate binary is cleaner long-term.
- [ ] How does the gateway communicate with session workers? gRPC? REST?
  PostgreSQL LISTEN/NOTIFY? (Recommendation: start with LISTEN/NOTIFY, move
  to gRPC if latency matters.)
- [ ] Should session workers pull their config from the control plane API or
  from mounted ConfigMaps/Secrets? ConfigMaps are simpler but less dynamic.
- [ ] How should the shared gateway handle graceful restart without dropping
  all bot connections? (Rolling update with connection draining? Blue-green?)
- [ ] How does the multi-bot Business tier coordinate NPC orchestration across
  workers? Shared state in PostgreSQL? Direct worker-to-worker gRPC?

### Tenant & Campaign Data

- [ ] What does the campaign export format look like? JSON archive? YAML?
  Should it be importable into a fresh campaign on a higher tier?
- [ ] How do we handle tenant offboarding? Data retention period before
  permanent deletion? GDPR right-to-deletion timelines?
- [ ] How do we migrate existing single-tenant alpha data into the multi-tenant
  schema? One-time migration script? Treat existing data as tenant #1?
- [ ] Should campaign deletion (Starter tier) be synchronous (blocking) or
  async (background job with progress)?
- [ ] How granular is the campaign export? Full L1+L2+L3 dump, or just the
  knowledge graph and NPC definitions (transcripts are large)?

### Discord

- [ ] Can we use Discord's interaction endpoint (HTTP webhook) instead of the
  gateway WebSocket for slash commands? This eliminates always-on gateway
  pods but adds latency and loses gateway event subscriptions (presence,
  voice state updates). Likely need both.
- [ ] Should Glyphoxa offer managed bot tokens (simpler onboarding) or
  require BYOB (bring your own bot)? Managed bots create a shared-fate
  SPOF; BYOB is more resilient but harder to onboard.
- [ ] How do we handle a tenant's bot getting rate-limited or banned by
  Discord? Auto-notify tenant? Auto-disable sessions?

### Business / Licensing

- [ ] What's the pricing model? Monthly subscription + session-hour overage?
  Flat per-tier? Usage-based?
- [ ] Is self-hosted (open-core) a priority? If yes, the tenant model must
  degrade gracefully to "single tenant, no control plane" for self-hosters.
- [ ] What SLA per tier? Starter: best-effort? Standard: 99.5%? Pro: 99.9%?
  Business: 99.95% with SLA credits?
- [ ] How do we handle license upgrades mid-session? (e.g., tenant upgrades
  from Standard to Pro -- do active sessions pick up the new limits
  immediately, or only new sessions?)
- [ ] Should there be a free/trial tier below Starter?

### Operations

- [ ] Who is on-call? What's the incident response process?
- [ ] What's the disaster recovery plan for shared PostgreSQL? For dedicated
  instances?
- [ ] Multi-region? Discord voice servers are regional -- should Glyphoxa
  workers be deployed close to Discord's voice servers for latency?
- [ ] Backup strategy: shared instance (pg_dump per-schema on schedule),
  dedicated instances (managed PITR)?
- [ ] Do we need SOC 2 or similar compliance for handling voice data?

---

## Summary: Recommended First Steps

Prioritised implementation order, informed by the tenant/license model:

### Phase 1: Core Tenant Model (prerequisite for everything else)

1. **Define `TenantContext` and `LicenseTier` in core** -- add to
   `internal/config/`, thread through `context.Context`. Every subsystem
   receives the tenant context: memory, MCP, providers, metrics.

2. **Add `campaign_id` to the data model** -- new column on `session_entries`,
   `chunks`, `entities`, `relationships`. All queries gain a `campaign_id`
   filter. This is the data isolation boundary within a tenant.

3. **Schema-per-tenant database migration** -- modify `postgres.Migrate()` to
   create tables within a tenant schema (`SET search_path`). Add RLS policies
   as defense-in-depth on shared instances.

4. **Campaign export feature** -- JSON/YAML dump of L1 transcripts, L3
   knowledge graph, and NPC definitions. Required before Starter tier can
   replace campaigns.

### Phase 2: Gateway / Worker Split

5. **Split gateway and worker** -- refactor `cmd/glyphoxa/` into two modes
   (`--mode=gateway` and `--mode=worker`). Gateway handles Discord
   interactions and session orchestration. Worker handles the voice pipeline.
   Same binary, different startup paths.

6. **Session state in PostgreSQL** -- replace in-process `SessionManager`
   with the `sessions` table. License constraint enforcement in the gateway's
   `/session start` handler.

7. **Shared gateway multi-bot support** -- gateway manages multiple Discord
   bot tokens (one per tenant). Events are demuxed by bot client. This is the
   Starter/Standard deployment model.

### Phase 3: Kubernetes & Infrastructure

8. **Helm chart** -- shared gateway Deployment, session worker Jobs,
   PgBouncer, shared MCP gateway. Configurable for shared (Starter/Standard)
   and dedicated (Pro/Business) topologies.

9. **External Secrets Operator + Vault** -- move API keys and bot tokens out
   of config files. Per-tenant Vault paths. Support BYOK submission flow.

10. **Dedicated infrastructure provisioning** -- Terraform/Crossplane modules
    for Pro/Business: dedicated PostgreSQL instance, dedicated gateway
    Deployment, dedicated namespace.

### Phase 4: Observability & Billing

11. **Per-tenant metrics and logging** -- add `tenant_id`, `license_tier`,
    `campaign_id` labels to Prometheus metrics and structured log fields.
    Grafana dashboards with tenant filter.

12. **Usage tracking and quotas** -- session hours, provider API calls per
    tenant. Enforce limits based on license tier. Integrate with Stripe or
    equivalent for billing.

### Phase 5: Polish & Scale

13. **Custom operator (CRD: GlyphoxaSession)** -- declarative session
    lifecycle with license constraint enforcement at the Kubernetes level.
    Implement when manual Job management becomes unwieldy.

14. **KEDA autoscaling** -- scale shared gateway based on connected bot count.

15. **Tier-aware node scheduling** -- separate node pools for shared and
    dedicated workloads. GPU scheduling for local inference tenants.

16. **Discord interaction endpoint (webhook)** -- supplement the gateway
    WebSocket with HTTP interactions for lower-latency slash command
    responses and reduced gateway load.
