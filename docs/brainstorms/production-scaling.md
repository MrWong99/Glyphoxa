# Production Scaling & Multi-Tenant Deployment

Brainstorm: what architectural changes are needed to take Glyphoxa from a
single-guild alpha to a scalable, multi-tenant production deployment?

**Status:** Draft brainstorm -- not a design doc. Everything here is open for
discussion.

**Date:** 2026-03-04

---

## Table of Contents

1. [Current State Summary](#1-current-state-summary)
2. [Multi-Tenancy & Guild Isolation](#2-multi-tenancy--guild-isolation)
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
| Guild support | One `guild_id` per process | No multi-guild in one instance |
| Sessions | One active session per process (`SessionManager.active` bool) | No concurrent sessions |
| Discord bot | Single bot token, single gateway connection | No sharding |
| Database | Shared PostgreSQL, no tenant columns | No data isolation |
| Config | Flat YAML, no tenant concept | No per-tenant config |
| Deployment | Docker Compose, single container | No orchestration |
| Secrets | API keys in YAML or env vars | No vault integration |
| MCP tools | All tools in-process or per-instance stdio subprocesses | No shared tool services |

The roadmap (Phase 8) mentions Helm charts and managed cloud as future items
but no concrete design exists.

---

## 2. Multi-Tenancy & Guild Isolation

### The Problem

A production Glyphoxa SaaS must serve many customers (tenants), each with
their own Discord guild(s), API keys, NPC configs, campaigns, and memory. Data
from tenant A must never leak to tenant B.

### What is a "tenant"?

Needs definition. Candidates:

| Model | Description | Pros | Cons |
|-------|-------------|------|------|
| **Tenant = Guild** | Each Discord guild is a tenant | Simple, 1:1 mapping with Discord | A customer with multiple guilds gets multiple tenants |
| **Tenant = Customer** | A customer (person/org) owns N guilds | Flexible, natural for billing | Requires an account system beyond Discord |
| **Tenant = Campaign** | Each campaign is isolated | Finer-grained, good for shared guilds | Over-isolation -- same guild NPCs can't interact cross-campaign |

**Recommendation:** Start with **Tenant = Customer** (an account that owns
1+ guilds). This allows a single billing entity while supporting multiple
guilds per subscription. A lightweight account service (OAuth with Discord
login) maps Discord user IDs to tenant IDs.

### Isolation Approaches

#### Option A: Process-level isolation (one container per tenant)

Each tenant gets their own Glyphoxa container(s). No code changes for tenant
isolation -- data isolation is guaranteed by process boundary.

```
Tenant A pod: [glyphoxa-a] ---> shared PostgreSQL (schema: tenant_a)
Tenant B pod: [glyphoxa-b] ---> shared PostgreSQL (schema: tenant_b)
```

- **Pros:** Strongest isolation, simplest code changes, blast radius per
  tenant, independent scaling.
- **Cons:** Higher resource baseline (one container per tenant even when idle),
  more complex orchestration.

#### Option B: Shared process, application-level isolation

A single Glyphoxa process serves multiple guilds. Tenant context is threaded
through every database query and memory operation.

```
Shared pod: [glyphoxa] ---> shared PostgreSQL (tenant_id column on every table)
```

- **Pros:** Lower resource usage, simpler deployment.
- **Cons:** Deep code changes (tenant context everywhere), risk of
  cross-tenant data leaks, noisy-neighbor latency, harder to reason about.

#### Option C: Hybrid -- shared process per shard, process-per-tenant for premium

Small tenants share a Glyphoxa instance (with application-level isolation).
Premium/large tenants get dedicated instances. The control plane decides
placement.

- **Pros:** Cost-efficient for small tenants, strong isolation for large ones.
- **Cons:** Two code paths, complex control plane.

**Initial recommendation:** **Option A** (process-per-tenant). Glyphoxa's
architecture is already single-guild. Running one container per guild avoids
deep refactoring and provides the strongest isolation. Kubernetes makes this
manageable.

---

## 3. Deployment Topology

### Option A: One container per active session

A container is created on `/session start` and torn down on `/session stop`.
Between sessions, no Glyphoxa container runs for that tenant.

```
Discord command: /session start
    --> Control plane receives webhook
    --> Kubernetes Job/Deployment created
    --> Glyphoxa boots, joins voice, runs session
    --> /session stop --> container terminates
```

- **Pros:** Zero cost when idle (scale to zero), strong isolation, resource
  usage exactly matches active sessions.
- **Cons:** Cold start latency (container pull + boot + Discord gateway connect
  + provider init = 5-15s), need a persistent control plane to receive Discord
  interactions before the session container is up.
- **Cold start mitigation:** Pre-pull images on nodes, keep a warm pool of
  standby containers, use `initContainers` for model downloads.

### Option B: One long-running container per tenant

Each tenant has a persistent Glyphoxa deployment. The container stays up even
between sessions, handling Discord interactions (slash commands for NPC CRUD,
entity management, etc.).

```
Tenant A: always-on Deployment (1 replica)
    --> handles slash commands + sessions
    --> scales from idle (low CPU) to active (voice session)
```

- **Pros:** No cold start, slash commands always work, simpler lifecycle.
- **Cons:** Baseline resource cost per tenant (even idle containers consume
  memory for Discord gateway + PostgreSQL connection).
- **Idle footprint:** ~50-80 MB RAM (Go binary + Discord gateway WebSocket +
  pgx pool). At 1000 tenants that's ~50-80 GB cluster memory just for idle.

### Option C: Shared gateway + session workers

A shared "gateway" service handles all Discord interactions (slash commands,
interaction routing). When a session starts, it spawns a dedicated "session
worker" container.

```
Shared gateway (1-N replicas, sharded by guild):
    --> receives all Discord interactions
    --> routes /npc, /entity, /campaign commands itself
    --> on /session start: creates session worker pod

Session worker (ephemeral, 1 per active session):
    --> joins voice channel
    --> runs the full voice pipeline
    --> terminates on /session stop
```

- **Pros:** Minimal idle cost (gateway is lightweight), session workers scale
  to zero, slash commands always respond instantly.
- **Cons:** More complex architecture (two service types), need RPC between
  gateway and worker, worker needs access to tenant config and secrets.
- **Gateway resource:** ~10-20 MB per guild (just Discord gateway + command
  routing). 1000 guilds in a single gateway instance is feasible.

**Recommendation:** **Option C** is the sweet spot for a SaaS. The gateway
handles Discord presence (always online, slash commands respond instantly)
while session workers scale purely with active voice usage. This matches the
usage pattern: most tenants are idle most of the time (TTRPG sessions are
weekly, 2-4 hours).

---

## 4. Container Orchestration (Kubernetes)

### Why Kubernetes?

- Native support for the gateway + worker topology
- Pod autoscaling, resource limits, health probes (Glyphoxa already has
  `/healthz` and `/readyz`)
- Secret injection via Vault CSI or external-secrets-operator
- Multi-cluster / multi-region for latency
- Well-understood by platform teams

### Deployment Models

#### Model 1: Plain Deployments + Jobs

```yaml
# Gateway: long-running, handles Discord interactions
kind: Deployment
metadata:
  name: glyphoxa-gateway
spec:
  replicas: 3  # sharded by guild range
  ...

# Session worker: created by gateway, one per active session
kind: Job
metadata:
  name: session-tenant-abc-20260304
spec:
  template:
    spec:
      containers:
      - name: glyphoxa-worker
        resources:
          requests: { cpu: "500m", memory: "256Mi" }
          limits:   { cpu: "2",    memory: "1Gi"   }
      restartPolicy: Never
```

- **Pros:** Simple, standard Kubernetes primitives.
- **Cons:** Gateway must programmatically create/delete Jobs via the Kubernetes
  API. Job cleanup needs TTL controller or manual GC.

#### Model 2: Custom Operator (CRD: GlyphoxaSession)

A custom operator watches a `GlyphoxaSession` CRD and manages worker pods.

```yaml
apiVersion: glyphoxa.io/v1alpha1
kind: GlyphoxaSession
metadata:
  name: session-guild-123
spec:
  tenantID: abc
  guildID: "123456789"
  configRef:
    name: tenant-abc-config
  secretRef:
    name: tenant-abc-secrets
```

The operator:
- Creates the worker pod with injected config and secrets
- Monitors health (restarts on crash, tracks session duration)
- Cleans up on session end or timeout
- Reports status back to the gateway
- Enforces per-tenant resource quotas

- **Pros:** Declarative, Kubernetes-native lifecycle, audit trail via CRD
  status, can enforce policies (max sessions per tenant, resource limits).
- **Cons:** Writing and maintaining an operator is significant effort.
  Consider operator frameworks: `kubebuilder` or `operator-sdk`.

#### Model 3: Knative / Cloud Run (Serverless)

Use Knative Serving or a managed equivalent (Cloud Run, AWS App Runner) for
session workers. Scale to zero when no sessions are active.

```yaml
apiVersion: serving.knative.dev/v1
kind: Service
metadata:
  name: glyphoxa-session
spec:
  template:
    metadata:
      annotations:
        autoscaling.knative.dev/min-scale: "0"
        autoscaling.knative.dev/max-scale: "100"
    spec:
      containers:
      - image: ghcr.io/mrwong99/glyphoxa:latest
        resources:
          limits: { cpu: "2", memory: "1Gi" }
```

- **Pros:** Managed scaling, zero idle cost, built-in revision management.
- **Cons:** Knative is HTTP-oriented (Glyphoxa's session lifecycle is
  long-running WebSocket/voice, not request/response). Serverless cold starts
  (5-15s) may be problematic. May need to use Knative in "always allocated"
  mode which defeats the purpose.

**Recommendation:** Start with **Model 1** (Deployments + Jobs) for simplicity.
If the number of tenants grows past ~100 and operational burden increases,
invest in **Model 2** (custom operator) for declarative lifecycle management.
Knative is a poor fit for long-running voice sessions.

---

## 5. Database Scaling & Tenant Isolation

### The Problem

All tenants share one PostgreSQL instance. Need:
- Data isolation (tenant A can't read tenant B's memories)
- Performance isolation (tenant A's heavy queries don't slow tenant B)
- Scalable vector search (pgvector HNSW scales poorly past ~10M vectors on a
  single instance)

### Isolation Strategies

#### Option A: Schema-per-tenant

Each tenant gets a PostgreSQL schema. Same database, separate namespaces.

```sql
CREATE SCHEMA tenant_abc;
CREATE TABLE tenant_abc.session_entries (...);
CREATE TABLE tenant_abc.chunks (...);
CREATE TABLE tenant_abc.entities (...);
CREATE TABLE tenant_abc.relationships (...);
```

Connection string includes `search_path=tenant_xxx` or the application
qualifies table names.

- **Pros:** Strong logical isolation, PostgreSQL RBAC can enforce access,
  independent schema migrations possible, `pg_dump` per-schema for backups.
- **Cons:** Schema explosion at scale (10k tenants = 10k schemas), connection
  pool management (one pool per schema or SET search_path per query), DDL
  migrations must iterate all schemas.

#### Option B: Shared tables with `tenant_id` column

Add a `tenant_id TEXT NOT NULL` column to every table. All queries include
`WHERE tenant_id = $1`.

```sql
CREATE TABLE session_entries (
    id         BIGSERIAL PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    session_id TEXT NOT NULL,
    ...
);
CREATE INDEX idx_session_entries_tenant ON session_entries (tenant_id);
```

Use PostgreSQL Row-Level Security (RLS) as a safety net:

```sql
ALTER TABLE session_entries ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON session_entries
    USING (tenant_id = current_setting('app.tenant_id'));
```

- **Pros:** Single schema, simple migrations, works at any tenant count,
  RLS provides defense-in-depth.
- **Cons:** Every query must filter by tenant_id (easy to forget = data leak),
  index bloat (tenant_id in every index), noisy-neighbor on shared tables.

#### Option C: Database-per-tenant

Each tenant gets a fully separate PostgreSQL database (or even a separate
instance via managed services like RDS).

- **Pros:** Strongest isolation, independent scaling, per-tenant backups and
  restores, can use different PostgreSQL versions/extensions.
- **Cons:** Highest operational complexity, connection overhead, no cross-tenant
  queries (fine for Glyphoxa), expensive at scale with managed databases.

#### Option D: Shared instance with partitioning

Use PostgreSQL declarative partitioning to split tables by tenant:

```sql
CREATE TABLE session_entries (
    id         BIGSERIAL,
    tenant_id  TEXT NOT NULL,
    ...
) PARTITION BY LIST (tenant_id);

CREATE TABLE session_entries_tenant_abc PARTITION OF session_entries
    FOR VALUES IN ('abc');
```

- **Pros:** Partition pruning gives per-tenant query performance, can
  attach/detach partitions for data lifecycle, single schema.
- **Cons:** Partition management overhead, DDL complexity, pgvector HNSW
  indexes are per-partition (good for isolation, but each partition needs its
  own index).

**Recommendation:** **Option B** (shared tables + `tenant_id` + RLS) for the
initial launch (simplest, fewest moving parts). Migrate to **Option A**
(schema-per-tenant) or **Option D** (partitioning) if specific tenants need
performance isolation or if the total data volume makes shared indexes
problematic.

### Vector Index Scaling

pgvector HNSW performance degrades around 5-10M vectors on a single index.
Strategies:

| Approach | When | Trade-off |
|----------|------|-----------|
| Partition by tenant_id | Moderate scale | Each partition has its own HNSW index (smaller, faster) |
| Partition by time | Log-like data | Old sessions in cold partitions, recent in hot |
| Separate pgvector instance | Large scale | Dedicated vector DB (e.g., Qdrant, Weaviate) behind the `SemanticIndex` interface |
| Dimensionality reduction | Cost savings | Use smaller embedding models (768d vs 3072d) to reduce index size |

### Connection Pooling

At scale, each Glyphoxa worker pod opens a pgx connection pool. 100 workers
with 10 connections each = 1000 PostgreSQL connections. Solutions:

- **PgBouncer** in transaction mode (sidecar or centralized)
- **Supavisor** for Supabase-style pooling
- **pgx pool with MaxConns=3** per worker (voice pipeline is not DB-bound)

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

For tenants who want to use their own API keys:

1. Tenant enters keys via a web dashboard or Discord DM to the bot
2. Keys are encrypted and stored in the secrets backend under the tenant's path
3. The session worker's pod spec references the tenant's secret
4. At startup, Glyphoxa reads keys from env vars / files
5. Keys are never logged, never stored in the config YAML

---

## 7. MCP Services: Shared vs Per-Tenant

### The Problem

MCP tools run as either in-process Go handlers (built-ins) or external
processes (stdio/HTTP). In a multi-tenant deployment:
- Built-in tools (dice roller, rules lookup) are stateless -- can be shared
- Memory tools need tenant-scoped database access
- External tools (image gen, web search) may have per-tenant API keys
- Custom per-tenant MCP servers need lifecycle management

### Architecture Options

#### Option A: All tools in-process (current model, per worker)

Each session worker runs all built-in tools in-process and spawns stdio MCP
servers as subprocesses. No shared state.

- **Pros:** Simple, no network hop for built-in tools, tools die with the
  session.
- **Cons:** Duplicated processes (every worker spawns its own rules lookup,
  dice roller), stdio MCP servers can't be shared.

#### Option B: Shared MCP gateway service

A centralized MCP gateway runs shared tool servers (rules lookup, dice roller,
web search). Workers connect via streamable-http.

```
Worker A ---> MCP Gateway (shared) ---> rules-lookup
Worker B -/                         ---> dice-roller
                                    ---> web-search
```

Per-tenant tools still run in-process or as sidecar containers.

- **Pros:** Resource-efficient (one rules lookup process serves all workers),
  centralized tool health monitoring, can add new shared tools without
  redeploying workers.
- **Cons:** Network hop adds latency (~1-5ms), MCP gateway becomes a SPOF
  (needs HA), auth between worker and gateway.

#### Option C: Sidecar MCP containers

Each session worker pod includes sidecar containers for MCP tools that need
isolation (per-tenant image gen, custom tool servers).

```yaml
spec:
  containers:
  - name: glyphoxa-worker
    ...
  - name: mcp-image-gen      # sidecar, per-tenant API key
    image: mcp-image-gen:latest
    env:
    - name: API_KEY
      valueFrom:
        secretKeyRef: ...
  - name: mcp-custom-tools   # tenant's custom MCP server
    image: tenant-abc/custom-tools:latest
```

- **Pros:** Per-tenant isolation for sensitive tools, lifecycle tied to session,
  localhost networking (fast).
- **Cons:** Pod spec complexity, sidecar resource overhead.

**Recommendation:** **Hybrid.** Built-in tools (dice, rules, memory) stay
in-process in the worker. Shared stateless tools (web search) run as a shared
MCP gateway service. Per-tenant custom tools run as sidecars or
tenant-managed external HTTP servers.

### Memory Tool Scoping

Memory tools (`search_sessions`, `query_entities`, etc.) currently query the
global database. In a multi-tenant setup:
- In-process memory tools receive the tenant-scoped database connection (via
  the worker's config)
- If using shared tables with `tenant_id`, the memory tools must filter by
  tenant -- enforced at the `SessionStore` / `KnowledgeGraph` interface level
- RLS provides defense-in-depth

---

## 8. Discord Gateway Sharding

### The Problem

At 2500 guilds, Discord mandates gateway sharding. Even below that, a single
gateway connection handling 100+ guilds may hit rate limits or event
processing bottlenecks.

### Sharding Models

#### Option A: One shard per gateway pod (manual sharding)

Deploy N gateway pods, each configured with a shard ID range. Discord's
sharding protocol assigns guilds to shards deterministically
(`guild_id >> 22 % num_shards`).

```
Gateway Pod 0: shard 0 (guilds 0-832)
Gateway Pod 1: shard 1 (guilds 833-1665)
Gateway Pod 2: shard 2 (guilds 1666-2499)
```

- **Pros:** Simple, well-understood, each pod is independent.
- **Cons:** Manual shard assignment, rebalancing on scale-up requires
  coordination.

#### Option B: Shard manager (disgo's built-in sharding)

disgo (the Discord library Glyphoxa uses) has built-in shard management. A
single process can run multiple shards internally.

- **Pros:** Less operational complexity, disgo handles shard lifecycle.
- **Cons:** All shards in one process (single point of failure, resource
  contention), doesn't scale past one node easily.

#### Option C: External shard orchestrator

A dedicated shard coordinator (like [Twilight
Gateway](https://github.com/twilight-rs/twilight) or a custom one) handles
Discord gateway connections. It forwards events to the gateway service via
AMQP/NATS/Redis Streams.

```
Discord API <--> Shard Orchestrator <--> Message Queue <--> Gateway Pods
```

- **Pros:** Separates Discord protocol handling from application logic,
  horizontal scaling, shard rebalancing without app restarts.
- **Cons:** Significant infrastructure (message queue, orchestrator),
  added latency for event delivery.

**Recommendation:** Start with **Option A** (one shard per pod) using disgo's
shard ID configuration. This works well up to ~50 shards (~125k guilds). If
Glyphoxa needs to scale beyond that, move to **Option C** with a message
queue for event distribution.

### Bot Token Strategy

| Approach | Description | When |
|----------|-------------|------|
| **Single bot token** | All shards share one bot token (standard Discord bot) | < 2500 guilds, one bot application |
| **Multiple bot tokens** | Separate bot applications for shard groups | Hitting per-token rate limits |
| **Per-tenant bot tokens** | Each tenant registers their own bot (BYOB) | Self-hosted / enterprise deployments |

---

## 9. Observability at Scale

### Current State

Glyphoxa already has solid observability (OpenTelemetry traces, Prometheus
metrics, Grafana dashboards, structured logging with trace correlation). The
question is how to scale this for multi-tenant production.

### What Needs to Change

#### Tenant-scoped metrics

Add a `tenant_id` label to key metrics:

```
glyphoxa_active_sessions{tenant_id="abc"}
glyphoxa_stt_duration_seconds{tenant_id="abc"}
glyphoxa_provider_requests_total{tenant_id="abc", provider="openai", kind="llm"}
```

**Cardinality concern:** At 1000 tenants with 6 metric families, this adds
~6000 time series. Manageable for Prometheus, but monitor cardinality growth.

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
- Latency degradation

Global alerts for:
- Cluster resource exhaustion
- Database connection pool saturation
- Shard disconnections

---

## 10. Session Lifecycle & State

### The Problem

Today `SessionManager` is an in-process singleton. In a distributed
deployment, session state must be managed across gateway and worker
processes.

### Session State Machine

```
[Idle] --/session start--> [Provisioning] --pod ready--> [Connecting]
    --voice joined--> [Active] --/session stop--> [Draining] --drained--> [Idle]
                           |
                           +--crash--> [Recovering] --reconnect--> [Active]
                           +--timeout--> [Draining]
```

### State Storage Options

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| **PostgreSQL** | `sessions` table with state, tenant, timestamps | Already have PostgreSQL, ACID, queryable | Polling for state changes, slightly higher latency |
| **Redis** | Key-value with TTL, pub/sub for state changes | Fast, pub/sub for real-time updates | Another dependency |
| **etcd / Kubernetes API** | CRD status field or etcd keys | Kubernetes-native, watches for changes | Coupling to Kubernetes, not suitable for high-frequency updates |
| **In-memory (gateway)** | Gateway holds session state, workers report via gRPC | Lowest latency, simple | State lost on gateway restart, needs persistence backup |

**Recommendation:** **PostgreSQL** for durable session state (it's already a
dependency). Use a `sessions` table:

```sql
CREATE TABLE sessions (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL,
    guild_id    TEXT NOT NULL,
    state       TEXT NOT NULL DEFAULT 'provisioning',
    worker_pod  TEXT,
    channel_id  TEXT,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at    TIMESTAMPTZ,
    metadata    JSONB DEFAULT '{}'
);
```

The gateway polls or uses PostgreSQL LISTEN/NOTIFY for state transitions.

### Session Timeouts

Glyphoxa sessions can run for 4+ hours. Need:
- **Idle timeout:** No voice activity for N minutes -> auto-stop (save
  resources)
- **Max duration:** Hard limit (e.g., 8 hours) to prevent runaway sessions
- **Grace period on disconnect:** If voice drops, wait M minutes before
  teardown (Discord voice disconnects are common)
- **Billing integration:** Track session duration for usage-based pricing

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

**Recommendation:** **Option B** (KEDA) for gateway scaling. Session workers
don't need autoscaling -- they're created on-demand and cleaned up on session
end.

### GPU Node Scheduling

For tenants using local inference (whisper.cpp, Ollama, Coqui):
- Use Kubernetes node affinity or taints to schedule GPU-requiring workers on
  GPU nodes
- NVIDIA GPU Operator for GPU resource management
- Consider time-sharing GPUs (MIG on A100, or MPS) for smaller models

```yaml
resources:
  limits:
    nvidia.com/gpu: 1
nodeSelector:
  gpu-type: "t4"
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

| Control | Description | Granularity |
|---------|-------------|-------------|
| **Session quota** | Max concurrent sessions per tenant | Tier-based (free: 1, pro: 3, enterprise: unlimited) |
| **Session duration limit** | Max session length | Tier-based (free: 1h, pro: 4h, enterprise: 8h) |
| **LLM token budget** | Max tokens per session or per month | Per-tenant, enforced at LLM provider wrapper |
| **Provider rate limit** | Max API calls per minute | Per-tenant, prevents abuse |
| **Overage handling** | What happens when quota is exhausted | Degrade (switch to cheaper model), throttle, or block |

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
| **Tenant management** | CRUD tenants, map Discord guilds, manage subscriptions |
| **Config management** | Store per-tenant Glyphoxa configs (NPCs, campaigns, providers) |
| **Session orchestration** | Create/monitor/destroy session worker pods |
| **Discord interaction proxy** | Receive Discord webhooks, route to appropriate gateway/worker |
| **Billing integration** | Track usage, enforce quotas, generate invoices |
| **Secret management** | Store/rotate API keys in Vault, inject into worker pods |
| **Health monitoring** | Track worker/gateway health, auto-recover crashed sessions |

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
  of the existing binary (`glyphoxa --mode=gateway`)?
- [ ] How does the gateway communicate with session workers? gRPC? REST?
  PostgreSQL LISTEN/NOTIFY?
- [ ] Should session workers pull their config from the control plane API or
  from mounted ConfigMaps/Secrets?
- [ ] Is one session per guild sufficient, or do we need multiple concurrent
  sessions per guild (e.g., different voice channels)?

### Data

- [ ] How do we handle tenant offboarding? Data retention policy? GDPR
  deletion requests?
- [ ] Should tenant data be exportable (campaign export, memory dump)?
- [ ] How do we migrate existing single-tenant data to the multi-tenant
  schema?

### Discord

- [ ] Can we use Discord's interaction endpoint (webhook-based) instead of
  gateway for slash commands? This would eliminate the need for always-on
  gateway pods for slash command handling.
- [ ] How do we handle the 2500-guild sharding requirement? Do we need it
  before launch?

### Business

- [ ] What's the pricing model? Per-session? Per-hour? Per-month? Tiered?
- [ ] Is self-hosted (open-core) a priority alongside the managed SaaS?
- [ ] What SLA do we offer? 99.9% (8.7h downtime/year)? 99.5%?

### Operations

- [ ] Who is on-call? What's the incident response process?
- [ ] What's the disaster recovery plan? Multi-region?
- [ ] What's the backup strategy for tenant data?
- [ ] Do we need PCI compliance (if handling payment data)?

---

## Summary: Recommended First Steps

If I had to prioritize, here's the order I'd tackle these in:

1. **Add `tenant_id` to the data model** -- column on all tables, RLS
   policies, filter in all queries. This is the foundation for everything else.

2. **Split gateway and worker** -- refactor `cmd/glyphoxa/` into two modes.
   Gateway handles Discord interactions; worker handles voice sessions. They
   share the same binary but different startup paths.

3. **Kubernetes deployment with Helm** -- gateway as a Deployment, workers as
   Jobs created by the gateway. Basic health probes are already done.

4. **External Secrets Operator + Vault** -- move API keys out of config files.
   Support both platform keys and tenant BYOK.

5. **Control plane API** -- tenant CRUD, config management, session
   orchestration. Start minimal (just what's needed for the gateway to create
   workers).

6. **Per-tenant metrics and logging** -- add `tenant_id` label to Prometheus
   metrics, structured log fields, and Grafana dashboards.

7. **Session state in PostgreSQL** -- replace in-process `SessionManager`
   with a database-backed state machine.

8. **Discord sharding** -- implement when approaching 2500 guilds.

9. **Custom operator** -- implement when manual Job management becomes
   unwieldy (100+ tenants).

10. **Cost management** -- per-tenant usage tracking, quotas, billing
    integration.
