---
nav_order: 11
---

# Multi-Tenant Architecture & Gateway

Glyphoxa supports multi-tenant SaaS deployments through a gateway/worker architecture. This document covers the tenant model, admin API, session orchestration, usage tracking, and campaign export/import.

For single-process self-hosted deployments, see [Deployment](deployment.md) — `--mode=full` requires no tenant configuration.

---

## Binary Modes

The Glyphoxa binary supports four modes via the `--mode` flag:

| Mode | Description | Use case |
|------|-------------|----------|
| `full` (default) | Single-process. Gateway and worker in one binary. No admin API. Config from YAML. | Self-hosted, open-source |
| `gateway` | Session orchestrator. Manages Discord bots, owns Discord voice connections, streams audio to workers via gRPC AudioBridge. Serves admin API. Dispatches K8s Jobs for workers. | SaaS control plane |
| `worker` | Voice pipeline executor. Receives audio from gateway via gRPC AudioBridge, runs the full voice pipeline (VAD→STT→LLM→TTS→Mixer), streams NPC audio back. Never connects to Discord directly. | SaaS data plane |
| `mcp-gateway` | Shared MCP tool server. Hosts stateless tools (dice, rules) and proxies external MCP servers over HTTP. | SaaS tool layer |

In `--mode=full`, the gateway and worker contracts are satisfied by in-process function calls (`internal/gateway/local/`), so the full pipeline runs identically to distributed mode without network overhead.

---

## Tenant Model

A **tenant** represents a paying customer (one license). Each tenant owns:

- A **license tier** (`shared` or `dedicated`)
- A **Discord bot token** (one bot per tenant)
- A set of **guild IDs** the bot is allowed to join
- A **monthly session hour quota**
- One or more **campaigns** (game worlds with NPCs, entities, and session history)

### Data Isolation

| Tier | Database | Infrastructure |
|------|----------|---------------|
| **Shared** | Schema-per-tenant in a shared PostgreSQL instance. Each tenant gets its own schema with full table set. `DROP SCHEMA CASCADE` for clean offboarding. | Shared gateway + shared worker node pool |
| **Dedicated** | Dedicated PostgreSQL instance | Dedicated gateway + dedicated worker node pool |

Schema names are validated at construction (`^[a-z][a-z0-9_]{0,62}$`) and sanitized with `pgx.Identifier` — SQL injection through schema names is impossible by construction.

### TenantContext

Every request in the system carries a `TenantContext` containing `tenant_id` and `campaign_id`. This context:

- Scopes all database queries to the correct schema
- Labels all metrics and traces with `tenant_id` and `campaign_id`
- Determines which bot token to use for Discord connections
- Enforces quota limits on session start

---

## Admin API

The gateway exposes an internal HTTP API on a separate port (default `:8081`), protected by API key authentication and NetworkPolicy.

### Authentication

All requests must include the `Authorization: Bearer <api-key>` header. The API key is set via the `GLYPHOXA_ADMIN_KEY` environment variable.

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/tenants` | Create a new tenant |
| `GET` | `/tenants` | List all tenants |
| `GET` | `/tenants/{id}` | Get tenant details |
| `PUT` | `/tenants/{id}` | Update tenant (tier, bot token, guilds) |
| `DELETE` | `/tenants/{id}` | Delete tenant and all data |

### Create Tenant

```json
POST /tenants
{
  "id": "acme-corp",
  "license_tier": "shared",
  "bot_token": "MTIzNDU2Nzg5..."
}
```

### Update Tenant

```json
PUT /tenants/acme-corp
{
  "license_tier": "dedicated",
  "guild_ids": ["123456789", "987654321"]
}
```

---

## Session Orchestration

The `sessionorch.Orchestrator` manages distributed session lifecycle with three states:

```
pending → active → ended
```

### Constraint Enforcement

On `ValidateAndCreate`, the orchestrator checks:

1. **Concurrent session limit** — enforced by the license tier (database-level unique constraints prevent races)
2. **Quota guard** — `usage.QuotaGuard` wraps the orchestrator and rejects sessions when the tenant's monthly session hours are exhausted

### Heartbeat & Zombie Cleanup

Workers send periodic heartbeats to the gateway via gRPC. If a worker dies:

1. The heartbeat stops arriving
2. `CleanupZombies(timeout)` transitions stale sessions (no heartbeat for >90s) to `ended`
3. A new session can then be started

### Implementations

| Implementation | Backend | Used by |
|---------------|---------|---------|
| `PostgresOrchestrator` | PostgreSQL `sessions` table with exclusion constraints | `--mode=gateway` |
| `MemoryOrchestrator` | In-memory map | `--mode=full` |

---

## Usage & Quota Tracking

The `usage.Store` tracks per-tenant resource consumption per billing period (monthly):

| Metric | Description |
|--------|-------------|
| `session_hours` | Total hours of active voice sessions |
| `llm_tokens` | LLM tokens consumed |
| `stt_seconds` | Speech-to-text audio seconds processed |
| `tts_chars` | Text-to-speech characters synthesised |

### Quota Enforcement

`QuotaGuard` wraps the session orchestrator. Before creating a session, it calls `Store.CheckQuota()`. If the tenant has reached their `monthly_session_hours` limit, the session is rejected with `ErrQuotaExceeded`.

---

## gRPC Contract

Gateway and worker communicate via three gRPC services defined in `proto/glyphoxa/v1/session.proto`:

### SessionWorkerService (worker exposes, gateway calls)

| RPC | Direction | Purpose |
|-----|-----------|---------|
| `StartSession` | gateway → worker | Launch voice pipeline for a session |
| `StopSession` | gateway → worker | Tear down a running session |
| `GetStatus` | gateway → worker | Query active session statuses |
| `ListNPCs` | gateway → worker | List NPCs in a running session |
| `MuteNPC` / `UnmuteNPC` | gateway → worker | Mute/unmute a specific NPC |
| `MuteAllNPCs` / `UnmuteAllNPCs` | gateway → worker | Mute/unmute all NPCs in a session |
| `SpeakNPC` | gateway → worker | Force an NPC to speak pre-written text |

### AudioBridgeService (gateway exposes, worker calls)

| RPC | Direction | Purpose |
|-----|-----------|---------|
| `StreamAudio` | bidirectional | Opus audio frames between gateway's Discord voice connection and worker's audio pipeline |

The gateway joins Discord voice via disgo's `VoiceManager` and creates a per-session `SessionBridge` (`internal/gateway/audiobridge/`). When a worker starts, it opens a bidirectional gRPC stream to `StreamAudio`, sends a handshake frame with the `session_id`, and then receives Discord audio (gateway→worker) and sends NPC audio (worker→gateway) over the same stream. The worker uses `grpcbridge.Connection` (`pkg/audio/grpcbridge/`) which implements `audio.Connection`, so the rest of the voice pipeline is identical to direct Discord mode.

Audio frames include `flush` signals for barge-in: when a player interrupts an NPC, the worker sends a flush frame and the gateway discards all buffered outgoing audio.

### SessionGatewayService (gateway exposes, worker calls)

| RPC | Direction | Purpose |
|-----|-----------|---------|
| `ReportState` | worker → gateway | Report session state transitions |
| `Heartbeat` | worker → gateway | Periodic liveness signal |

The gRPC client (`grpctransport.Client`) wraps all calls with a circuit breaker to prevent cascading failures when a worker becomes unreachable.

In `--mode=full`, the `SessionWorkerService` and `SessionGatewayService` contracts are satisfied by `local.Client` and `local.Callback` which make direct function calls with no serialisation overhead. The AudioBridge is not used in full mode — the worker opens its own Discord voice connection directly.

---

## Bot Management

`BotManager` manages per-tenant Discord bot connections:

- Each tenant has exactly one bot (one token)
- `AddBot` / `RemoveBot` manage bot lifecycle
- `RouteEvent` dispatches Discord events to the correct tenant's bot
- Thread-safe: all operations are guarded by `sync.Mutex`

The gateway starts a bot for each tenant on startup and adds/removes bots when tenants are created or deleted via the admin API.

---

## Campaign Export & Import

Campaigns can be exported as `.tar.gz` archives and imported into another tenant or environment.

### Archive Structure

```
campaign-export.tar.gz
├── metadata.json          # CampaignID, TenantID, LicenseTier, ExportedAt, Version
├── npcs/
│   ├── grimjaw.yaml       # NPC definition
│   └── elara.yaml
├── knowledge-graph.json   # Entities and relationships with provenance
└── sessions/
    ├── session-001.txt    # Session transcript
    └── session-002.txt
```

### Usage

Export and import are available through the `pkg/memory/export` package:

- `WriteTarGz(w io.Writer, data ExportData) error` — creates archive
- `ReadTarGz(r io.Reader) (*ImportData, error)` — reads and validates archive

L2 semantic chunks (vector embeddings) are included only for Dedicated tier exports, as they are large and can be regenerated.

---

## Key Source Files

| File | Description |
|------|-------------|
| `cmd/glyphoxa/main.go` | Mode flag parsing and dispatch |
| `cmd/glyphoxa/worker_factory.go` | RuntimeFactory — builds voice pipeline, connects AudioBridge or direct Discord |
| `internal/gateway/admin.go` | Admin API HTTP handlers |
| `internal/gateway/botmanager.go` | Per-tenant bot lifecycle |
| `internal/gateway/contract.go` | WorkerClient, NPCController, and GatewayCallback interfaces |
| `internal/gateway/audiobridge/bridge.go` | Per-session audio bridge server (gateway-side, Discord↔gRPC) |
| `internal/gateway/dispatch/dispatcher.go` | K8s Job dispatcher for worker pods |
| `internal/gateway/sessionorch/orchestrator.go` | Session lifecycle and constraints |
| `internal/gateway/usage/quota_guard.go` | Quota enforcement wrapper |
| `internal/gateway/grpctransport/client.go` | gRPC WorkerClient with circuit breaker |
| `internal/gateway/local/local.go` | In-process WorkerClient for full mode |
| `internal/session/runtime.go` | Voice pipeline lifecycle |
| `internal/session/worker_handler.go` | gRPC handler managing Runtime instances |
| `pkg/audio/grpcbridge/connection.go` | Worker-side gRPC audio connection (implements audio.Connection) |
| `pkg/memory/export/` | Campaign archive read/write |
| `proto/glyphoxa/v1/session.proto` | gRPC service and message definitions |

---

**See also:** [Architecture](architecture.md) · [Distributed Mode](distributed-mode.md) · [Deployment](deployment.md) · [Observability](observability.md) · [Configuration](configuration.md)
