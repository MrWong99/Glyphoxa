# Distributed Mode (Gateway + Worker) Production Readiness Audit

**Date:** 2026-03-21
**Auditor:** Claude
**Scope:** All code paths exercised in `--mode=gateway` and `--mode=worker`
**Verdict:** **Not production-ready.** Multiple critical gaps block end-to-end session dispatch.

---

## Summary

The distributed architecture is well-designed on paper: the gRPC contract, protobuf definitions, circuit breaker, session orchestrator (with both memory and PostgreSQL backends), usage tracking, Helm charts with RBAC and NetworkPolicies, and the in-process `local/` fallback for `--mode=full` are all solid foundations.

However, the **glue code that connects these pieces in gateway mode is incomplete**. The gateway cannot dispatch sessions to workers, workers cannot run voice pipelines, tenant state doesn't survive restarts, and gateway bots have no slash command handlers. The system is architecturally sound but not wired end-to-end.

### Already fixed (not covered here)

1. BotManager not wired to AdminAPI (commit 162a1f0)
2. zhi workspace wrong store provider name (commit 8399caa)

### Being worked on by other agents

3. Gateway bot slash command registration (plan written)
4. Docker workflow version tagging (in progress)

---

## Critical Gaps (Blocks Production Use)

### C1. No worker dispatcher — gateway cannot create or reach workers

**Severity:** Critical (complete blocker)
**Affected files:** `cmd/glyphoxa/main.go:278-389` (`runGateway`)

The gateway's `runGateway()` function never instantiates a `WorkerClient` (the `grpctransport.Client` or any implementation). It sets up the gRPC server to *receive* callbacks from workers (`ReportState`, `Heartbeat`), but has no client to *call* workers (`StartSession`, `StopSession`, `GetStatus`).

Additionally, the Helm chart defines a `worker-job-template` ConfigMap (`deploy/helm/glyphoxa/templates/worker-job.yaml`) with the comment "The gateway creates Jobs dynamically at session start using the Kubernetes API," but **no Go code exists** that reads this template or calls the Kubernetes API. There is no `client-go` import anywhere in the `internal/gateway/` package.

The full dispatch chain is missing:

1. Receive `/session start` slash command (gap C5)
2. Validate constraints via orchestrator
3. Create K8s Job from template (gap C1 — **no code**)
4. Wait for worker pod to be ready
5. Call `WorkerClient.StartSession()` (gap C1 — **no client**)
6. Track worker address for future calls

**Suggested fix:** Implement a `WorkerProvisioner` that:
- Reads the job template from the ConfigMap (or embeds it)
- Creates a K8s Job via `client-go`
- Waits for the worker pod's gRPC to become reachable
- Returns a connected `grpctransport.Client`
- Stores the session→worker mapping for `StopSession`/`GetStatus`

---

### C2. Worker RuntimeFactory is a placeholder — workers cannot run voice sessions

**Severity:** Critical (complete blocker)
**Affected files:** `cmd/glyphoxa/main.go:427-436`

The worker's RuntimeFactory creates an empty `session.Runtime` with only `SessionID` set:

```go
func(_ context.Context, req gw.StartSessionRequest) (*session.Runtime, error) {
    _ = providers // unused
    return session.NewRuntime(session.RuntimeConfig{
        SessionID: req.SessionID,
    }), nil
}
```

Missing from the factory:
- Agent creation (NPC definitions, router, orchestrator)
- Engine creation (cascade/s2s pipeline)
- Audio connection (Discord voice via the worker's own bot token or forwarded connection)
- Mixer and transport setup
- Session store for transcript recording
- TenantContext propagation
- MCP host and tools

The `StartSessionRequest` protobuf carries `tenant_id`, `campaign_id`, `guild_id`, `channel_id`, `bot_token`, and `config_yaml`, but none of these are used.

**Suggested fix:** Wire the factory to build a full `app.Application` subset per session, similar to how `--mode=full` works in `cmd/glyphoxa/main.go:164`. The worker needs to establish its own Discord voice connection (audio flows directly worker↔Discord, not through the gateway).

---

### C3. Gateway hardcodes MemoryOrchestrator — session state lost on restart

**Severity:** Critical
**Affected files:** `cmd/glyphoxa/main.go:322`

```go
orch := sessionorch.NewMemoryOrchestrator()
```

The `PostgresOrchestrator` exists and is fully implemented (`internal/gateway/sessionorch/postgres.go`), complete with migrations, constraint enforcement, and zombie cleanup. But `runGateway()` hardcodes the in-memory version.

On gateway restart:
- All active session records are lost
- Zombie cleanup can't find stale sessions
- License constraints can't detect pre-existing active sessions
- Workers with active sessions become orphaned (their heartbeats will fail with "session not found")

**Suggested fix:** Wire `PostgresOrchestrator` when a database DSN is available (via `GLYPHOXA_DATABASE_DSN` env var). Fall back to `MemoryOrchestrator` only for development.

---

### C4. No PostgresAdminStore — tenant data lost on restart

**Severity:** Critical
**Affected files:** `cmd/glyphoxa/main.go:295`, `internal/gateway/adminstore_mem.go`

Only `MemAdminStore` exists. There is no `PostgresAdminStore` implementation. The `AdminStore` interface is defined (`internal/gateway/admin.go:55-61`) but only the in-memory variant is implemented.

On gateway restart:
- All tenant records are lost (IDs, license tiers, bot tokens, guild IDs, quotas)
- All bot connections must be manually re-created via the admin API
- No way to know which tenants existed or what their configuration was

**Suggested fix:** Implement `PostgresAdminStore` with a `tenants` table in the gateway database. Wire it in `runGateway()` when a DSN is available.

---

### C5. Gateway bots have no slash command handlers or event routing

**Severity:** Critical (being addressed by another agent — plan written)
**Affected files:** `internal/gateway/botconnector.go:31-43`

Gateway bots are created with `disgo.New()` with no event handlers:

```go
client, err := disgo.New(botToken,
    bot.WithDefaultGateway(),
    bot.WithCacheConfigOpts(...),
    bot.WithGatewayConfigOpts(...),
)
```

Compare this to the full-mode `Bot` (`internal/discord/bot.go:56+`) which registers an `InteractionCreateHandler` and routes commands via `CommandRouter`.

Gateway bots:
- Cannot receive or respond to slash commands
- Cannot handle voice state updates
- Don't register slash commands with Discord (no `SyncCommands` call)
- Have no way to trigger session start/stop

**Note:** A plan for this is already being written by another agent.

---

## Important Gaps (Should Fix Before Production)

### I1. No bot reconnection on gateway startup

**Severity:** Important
**Affected files:** `cmd/glyphoxa/main.go:278-389`, `internal/gateway/botmanager.go`

`BotManager` is purely in-memory. When the gateway restarts (even if `PostgresAdminStore` existed), there is no startup code that:

1. Reads all tenants from the admin store
2. Re-creates bot connections for tenants with bot tokens
3. Re-registers slash commands

This means every gateway restart requires manual re-registration of all tenants via the admin API.

**Suggested fix:** Add a startup reconciliation loop: `store.ListTenants() → for each with token → bots.ConnectBot()`.

---

### I2. BotConnector ignores guild IDs — no multi-guild filtering

**Severity:** Important
**Affected files:** `internal/gateway/botconnector.go:27-62`

The `ConnectBot` function accepts `guildIDs []string` but only logs them. The disgo client connects to the global Discord gateway without guild filtering. The bot will:

- Receive events from ALL guilds it's been added to, not just the configured ones
- Allow sessions to be started in unauthorized guilds
- Potentially cross tenant boundaries if the same bot token is used (unlikely but possible in dev)

The full-mode `Bot` also only supports a single `GuildID` (`internal/discord/bot.go:49`), not multiple.

**Suggested fix:** Filter incoming interactions by guild ID. Either reject commands from non-configured guilds, or use guild-specific command registration (register slash commands per guild, not globally).

---

### I3. No usage/quota tracking wired in gateway mode

**Severity:** Important
**Affected files:** `cmd/glyphoxa/main.go:278-389`

The `usage.QuotaGuard` wrapper and `usage.PostgresStore` exist and are tested, but `runGateway()` doesn't instantiate either. The orchestrator runs without quota enforcement, meaning tenants can run unlimited sessions regardless of their `monthly_session_hours` setting.

**Suggested fix:** Wrap the orchestrator with `usage.NewQuotaGuard(orch, usageStore, quotaLookup)` in `runGateway()`.

---

### I4. Gateway doesn't register readiness checks

**Severity:** Important
**Affected files:** `cmd/glyphoxa/main.go:319`

```go
observeSrv := startObserveServer(cfg)
```

No readiness checkers are passed. In full mode, `app.ReadinessChecks()` registers a database probe. In gateway mode, `/readyz` always returns 200 even if:

- PostgreSQL is unreachable (when `PostgresOrchestrator` is wired)
- The admin store is unhealthy
- No bot connections are active

Kubernetes will route traffic to an unready gateway.

**Suggested fix:** Register readiness checkers for database connectivity and critical subsystem health.

---

### I5. gRPC uses insecure credentials — no TLS or authentication

**Severity:** Important
**Affected files:**
- `cmd/glyphoxa/main.go:414` (worker → gateway)
- `internal/gateway/grpctransport/client.go:34` (gateway → worker)
- `cmd/glyphoxa/main.go:330,445` (gRPC servers)

All gRPC connections use `insecure.NewCredentials()`. gRPC servers are created with `grpc.NewServer()` (no options). This means:

- Traffic between gateway and workers is unencrypted
- Any pod in the namespace can impersonate a worker and send fake heartbeats/state reports
- Any pod can impersonate the gateway and send StartSession commands to workers

The Helm chart's NetworkPolicies provide some mitigation (only gateway↔worker gRPC traffic is allowed), but this is defense-in-depth, not a substitute for transport security.

**Suggested fix:** Add mTLS using K8s cert-manager or a service mesh (Istio/Linkerd). At minimum, add a shared secret as a gRPC metadata interceptor.

---

### I6. gRPC servers have no observability interceptors

**Severity:** Important
**Affected files:**
- `cmd/glyphoxa/main.go:330` (`grpc.NewServer()` — gateway)
- `cmd/glyphoxa/main.go:445` (`grpc.NewServer()` — worker)

Neither gRPC server has logging, metrics, or tracing interceptors. This means:

- Distributed traces don't propagate across gRPC boundaries (W3C Trace Context headers aren't extracted/injected)
- gRPC call latency/error rate isn't captured in Prometheus metrics
- gRPC calls aren't logged with structured context

The HTTP middleware (`internal/observe/middleware.go`) does all this for HTTP, but gRPC is uninstrumented.

**Suggested fix:** Add `go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc` interceptors to both servers and clients.

---

### I7. Worker crash detection relies only on heartbeat timeout

**Severity:** Important
**Affected files:** `internal/gateway/sessionorch/` (zombie cleanup), `cmd/glyphoxa/main.go:346-361`

When a worker crashes mid-session:

1. Heartbeats stop arriving
2. Gateway detects stale heartbeat after 90 seconds (`CleanupZombies` timeout)
3. Session is transitioned to `ended` with error "heartbeat timeout"

This is a 90-second detection window. During this time:
- The session appears active to the gateway
- No new session can be started for that campaign (constraint violation)
- Users see no feedback

Additionally, if `MemoryOrchestrator` is used (current state), a gateway restart during this window loses the stale session entirely — it can never be cleaned up, and the K8s Job (once implemented) would run until its 4-hour `activeDeadlineSeconds`.

**Suggested fix:**
- Reduce heartbeat interval from 30s to 10s and timeout from 90s to 30s for faster detection
- When C1 is implemented, also watch the K8s Job status (pod terminated → immediate cleanup)
- Send a user-facing Discord message when a session is cleaned up as a zombie

---

### I8. CallbackBridge loses worker error messages

**Severity:** Important
**Affected files:** `internal/gateway/sessionorch/callback.go:28-31`

```go
func (cb *CallbackBridge) ReportState(ctx context.Context, sessionID string, state gateway.SessionState) error {
    var errMsg string
    if state == gateway.SessionEnded {
        errMsg = "worker reported ended"
    }
    // ...
}
```

The actual error message from the worker is not propagated. The `GatewayCallback.ReportState` interface only takes `sessionID` and `state`, not an error message. When a worker reports `SessionEnded` (e.g., due to a provider error, OOM, or user disconnect), the gateway records a generic "worker reported ended" instead of the real reason.

**Suggested fix:** Extend `GatewayCallback.ReportState` to accept an optional error message. Update the protobuf `ReportStateRequest` to include an `error` field (it already has one: `string error = 4`), and wire it through.

---

## Nice-to-Have Improvements

### N1. No worker auto-scaling mechanism

**Severity:** Nice-to-have (workers are ephemeral Jobs, so this is inherently elastic)
**Affected files:** `deploy/helm/glyphoxa/templates/hpa.yaml`

HPA exists for the gateway but not for workers (which are Jobs, not Deployments). The current design of one Job per session is inherently elastic, but there's no:

- Session queuing when the node pool is full
- Feedback to users about worker provisioning time
- Cluster Autoscaler integration awareness (scaling nodes takes minutes)

**Suggested fix:** Add a pending session queue in the gateway. If Job creation fails due to resource pressure, queue the session and retry. Report estimated wait time to the user.

---

### N2. Migration strategy has no version tracking

**Severity:** Nice-to-have
**Affected files:** `internal/gateway/sessionorch/postgres.go:50-65`

`runMigrations()` executes all SQL files on every startup with no version tracking:

```go
for _, f := range migrationFiles {
    _, err = pool.Exec(ctx, string(upSQL))
}
```

The SQL uses `IF NOT EXISTS` / `CREATE TABLE IF NOT EXISTS` to be idempotent, which works for initial creation but won't handle schema evolution (column adds, type changes, constraint modifications).

**Suggested fix:** Integrate `golang-migrate` or similar for versioned migrations with rollback support.

---

### N3. Config YAML in ConfigMap may contain provider API keys

**Severity:** Nice-to-have (mitigated by NetworkPolicy)
**Affected files:** `deploy/helm/glyphoxa/templates/configmap.yaml`

The application config is mounted as a ConfigMap. If the config YAML contains provider API keys (OpenAI, Anthropic, ElevenLabs, etc.), these are stored as plaintext in etcd and visible to anyone with ConfigMap read access in the namespace.

The admin API key is already handled correctly — it's in a K8s Secret (`deploy/helm/glyphoxa/templates/secrets.yaml`).

**Suggested fix:** Move sensitive config values to K8s Secrets or use an external secret manager (Vault, SealedSecrets, External Secrets Operator). Reference them as environment variables from Secrets rather than embedding in the config YAML.

---

### N4. No graceful session draining on gateway shutdown

**Severity:** Nice-to-have
**Affected files:** `cmd/glyphoxa/main.go:371-388`

Gateway shutdown calls `gwGRPCServer.GracefulStop()` and `botMgr.Close()`, but doesn't:

- Notify active workers to stop their sessions
- Wait for in-progress session starts to complete
- Drain the orchestrator's pending sessions

With `PostgresOrchestrator`, sessions survive in the database, but workers will keep heartbeating to a dead gateway until the 90s timeout.

**Suggested fix:** On shutdown, iterate active sessions and send `StopSession` to each worker. Set a drain timeout.

---

### N5. Worker doesn't report pod identity

**Severity:** Nice-to-have
**Affected files:** `internal/gateway/grpctransport/client.go`, `proto/glyphoxa/v1/session.proto`

The `HeartbeatRequest` protobuf has a `worker_pod` field, but `grpctransport.GatewayClient.Heartbeat()` doesn't populate it. The `Session` struct has `WorkerPod` and `WorkerNode` fields, but they're never set.

This makes it impossible to:
- Correlate sessions to specific pods in monitoring
- Debug issues on specific worker pods
- Track which node a session ran on

**Suggested fix:** Pass `HOSTNAME` (set by K8s downward API) as `worker_pod` in heartbeat and state reports.

---

## Dependency Graph for Fixes

```
C4 (PostgresAdminStore) ──┐
                          ├─→ I1 (Bot reconnection on startup)
C5 (Slash commands)  ─────┤
                          ├─→ C1 (Worker dispatcher)
C3 (PostgresOrchestrator)─┤       │
                          │       ├─→ C2 (RuntimeFactory)
I3 (Quota tracking)  ─────┘       │
                                  └─→ End-to-end session flow
```

Recommended order:
1. **C3** + **C4** — Wire PostgreSQL backends (small, unblocks everything)
2. **C5** — Gateway slash commands (already in progress)
3. **C1** — Worker dispatcher with K8s Job creation (largest piece)
4. **C2** — Worker RuntimeFactory (depends on C1 for testing)
5. **I1** — Bot reconnection (depends on C4)
6. **I3** — Quota tracking (depends on C3)
7. **I5** + **I6** — gRPC security and observability
8. Everything else

---

## Files Referenced

| File | Relevance |
|------|-----------|
| `cmd/glyphoxa/main.go` | Gateway and worker startup wiring |
| `internal/gateway/admin.go` | AdminAPI and AdminStore interface |
| `internal/gateway/adminstore_mem.go` | In-memory admin store (only implementation) |
| `internal/gateway/botmanager.go` | Per-tenant bot lifecycle |
| `internal/gateway/botconnector.go` | Discord bot creation (ignores guild IDs) |
| `internal/gateway/contract.go` | WorkerClient and GatewayCallback interfaces |
| `internal/gateway/grpctransport/client.go` | gRPC client (insecure credentials) |
| `internal/gateway/grpctransport/server.go` | gRPC server (no interceptors) |
| `internal/gateway/local/local.go` | In-process fallback for full mode |
| `internal/gateway/sessionorch/orchestrator.go` | Orchestrator interface |
| `internal/gateway/sessionorch/postgres.go` | PostgreSQL orchestrator (exists but unused) |
| `internal/gateway/sessionorch/memory.go` | Memory orchestrator (used in gateway mode) |
| `internal/gateway/sessionorch/callback.go` | CallbackBridge (loses error messages) |
| `internal/gateway/usage/quota_guard.go` | Quota enforcement (exists but unwired) |
| `internal/session/runtime.go` | Voice pipeline lifecycle |
| `internal/session/worker_handler.go` | gRPC handler managing Runtimes |
| `internal/discord/bot.go` | Full-mode bot (single guild, has commands) |
| `internal/discord/commands/session.go` | Session slash commands |
| `internal/health/health.go` | Health probes |
| `internal/observe/metrics.go` | Metrics with tenant labels |
| `internal/observe/trace.go` | Tracing with tenant attributes |
| `internal/resilience/circuitbreaker.go` | Circuit breaker for gRPC |
| `deploy/helm/glyphoxa/templates/` | Helm chart (gateway, worker, RBAC, NetworkPolicy) |
| `proto/glyphoxa/v1/session.proto` | gRPC service definitions |
