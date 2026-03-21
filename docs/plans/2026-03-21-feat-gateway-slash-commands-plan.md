---
title: "feat: Gateway-Mode Discord Slash Commands"
type: feat
status: draft
date: 2026-03-21
---

# feat: Gateway-Mode Discord Slash Commands

## Overview

Implement full Discord slash command support in gateway mode (`--mode=gateway`).
Currently, gateway mode connects per-tenant Discord bots (via `BotConnector` /
`BotManager`) and runs session orchestration, but registers **zero** slash
commands because there is no gateway-aware session manager. This plan bridges
the gap so all commands that work in full mode also work in gateway mode:
`/session start|stop`, `/session recap`, `/session voice-recap`, `/npc`,
`/entity`, `/campaign`, `/feedback`.

## Problem Statement / Motivation

Gateway mode is the production deployment path for multi-tenant Glyphoxa. Bots
connect to Discord but users cannot interact with them — no slash commands are
registered, so the bot is effectively silent. All session lifecycle management
must go through the internal admin API, which is not user-facing. DMs need
the same `/session start` workflow they have in full mode.

## Architectural Gap Analysis

### What full mode has

In `runFull()` (`cmd/glyphoxa/main.go:137`):

1. **`discord.Bot`** — wraps disgo `bot.Client` with `CommandRouter`,
   `PermissionChecker`, event listeners, and guild-scoped command registration.
2. **`app.SessionManager`** — manages the full voice pipeline lifecycle
   (connect → VAD → STT → LLM → TTS → Mixer → voice channel). Provides:
   - `Start(ctx, channelID, dmUserID) error`
   - `Stop(ctx) error`
   - `IsActive() bool`
   - `Info() SessionInfo`
   - `Orchestrator() *orchestrator.Orchestrator` (NPC agent access)
   - `Mixer() audio.Mixer` (for voice recap playback)
   - `PropagateEntity(ctx, def) (EntityDefinition, error)`
3. **Command handlers** wired to the bot's router with `SessionManager` +
   stores as dependencies.

### What gateway mode has

In `runGateway()` (`cmd/glyphoxa/main.go:278`):

1. **`BotManager`** — stores raw `*bot.Client` per tenant (no router, no
   event listeners, no command registration).
2. **`BotConnector`** — creates bare disgo clients and registers them with
   `BotManager`. No slash command infrastructure.
3. **`sessionorch.Orchestrator`** — manages session **metadata** (lifecycle
   state, license constraints, heartbeats, zombie cleanup). Has NO concept of
   NPC agents, audio pipeline, mixer, or voice connection — those live on the
   worker.
4. **`WorkerClient`** (gRPC) — sends `StartSession`/`StopSession`/`GetStatus`
   to workers. No NPC management or audio control RPCs.
5. **No per-tenant config** — the `Tenant` record has `ID`, `LicenseTier`,
   `BotToken`, `GuildIDs`, and `MonthlySessionHours`. Missing: `DMRoleID`,
   campaign config, NPC definitions.

### Gap summary

| Capability | Full Mode | Gateway Mode | Gap |
|---|---|---|---|
| Discord bot with command router | `discord.Bot` | Raw `bot.Client` | No event listeners or router |
| Session start/stop | `SessionManager.Start/Stop` | `sessionorch.ValidateAndCreate` + `WorkerClient.StartSession` | Need orchestration layer |
| Session query (IsActive/Info) | `SessionManager.IsActive/Info` | `sessionorch.ActiveSessions/GetSession` | Need adapter |
| NPC management (list/mute/speak) | `orchestrator.Orchestrator` | Not available on gateway | Need worker-proxy gRPC RPCs |
| Audio mixer (voice recap) | `SessionManager.Mixer` | Not available on gateway | Need worker-proxy gRPC RPC |
| Permissions (DM role check) | `PermissionChecker` | Not configured | Need per-tenant `DMRoleID` |
| Entity/Campaign stores | Available locally | Shared Postgres | Already accessible |
| Per-tenant campaign config | Single `config.Config` | Not stored | Need tenant config extension |

## Proposed Solution

### Strategy: Interface extraction + gateway adapter + gRPC extensions

Rather than trying to make `SessionManager` work in both modes, we:

1. **Extract a `SessionController` interface** that captures what command
   handlers actually need from session management.
2. **Implement `GatewaySessionController`** that wraps `sessionorch.Orchestrator`
   + `WorkerClient` behind that interface.
3. **Enhance `BotConnector`** to produce fully wired `discord.Bot`-like
   instances with command routers and event listeners.
4. **Extend the gRPC worker contract** with NPC management and audio playback
   RPCs for commands that need worker-side resources.
5. **Extend the tenant model** with per-tenant config fields (`DMRoleID`,
   campaign ID) needed by command handlers.

### Architecture

```
                        Gateway Process
┌──────────────────────────────────────────────────────┐
│                                                      │
│  Admin API                                           │
│  ┌──────────┐     ┌─────────────────┐                │
│  │ Tenant   │────▶│ BotConnector    │                │
│  │ CRUD     │     │ (enhanced)      │                │
│  └──────────┘     └────────┬────────┘                │
│                            │ creates                 │
│                            ▼                         │
│  ┌──────────────────────────────────────┐            │
│  │ Per-Tenant GatewayBot                │            │
│  │  ┌─────────────┐ ┌───────────────┐  │            │
│  │  │CommandRouter│ │PermissionChkr │  │            │
│  │  └──────┬──────┘ └───────────────┘  │            │
│  │         │ dispatches to              │            │
│  │  ┌──────▼───────────────────────┐   │            │
│  │  │ Command Handlers             │   │            │
│  │  │  /session → GwSessionCtrl    │   │            │
│  │  │  /npc     → gRPC proxy       │   │            │
│  │  │  /entity  → Postgres store   │   │            │
│  │  │  /campaign→ tenant config    │   │            │
│  │  │  /feedback→ feedback store   │   │            │
│  │  └──────┬───────────────────────┘   │            │
│  └─────────┼───────────────────────────┘            │
│            │                                         │
│  ┌─────────▼──────────────────────┐                  │
│  │ GatewaySessionController       │                  │
│  │  sessionorch.Orchestrator      │                  │
│  │  WorkerClient (gRPC)           │                  │
│  └─────────┬──────────────────────┘                  │
│            │ gRPC                                     │
└────────────┼─────────────────────────────────────────┘
             │
             ▼
┌──────────────────────────────────────────────────────┐
│                     Worker Process                    │
│  ┌────────────────────────────────────┐              │
│  │ WorkerHandler (enhanced)           │              │
│  │  StartSession / StopSession        │              │
│  │  ListNPCs / MuteNPC / SpeakNPC    │  ◀── new     │
│  │  PlayAudio                         │  ◀── new     │
│  └────────────────────────────────────┘              │
│                    │                                  │
│                    ▼                                  │
│  ┌────────────────────────────────────┐              │
│  │ session.Runtime (voice pipeline)   │              │
│  │  orchestrator.Orchestrator         │              │
│  │  audio.Mixer                       │              │
│  └────────────────────────────────────┘              │
└──────────────────────────────────────────────────────┘
```

## Technical Approach

### Command-by-command analysis

| Command | Dependencies | Gateway approach | Reuse? |
|---|---|---|---|
| `/session start` | SessionController.Start, VoiceState cache, permissions | GatewaySessionController → orch + worker gRPC | Handler logic reusable via interface |
| `/session stop` | SessionController.Stop/IsActive/Info, permissions | GatewaySessionController → orch + worker gRPC | Handler logic reusable via interface |
| `/session recap` | SessionController.IsActive/Info, SessionStore, Orchestrator (NPC list) | SessionStore on gateway (Postgres); NPC list via gRPC or omit | Mostly reusable; NPC list degraded |
| `/session voice-recap` | SessionController.IsActive/Info, Mixer, RecapStore, Generator | Mixer + generator on worker; gateway sends gRPC PlayRecap | New gateway handler; worker RPC |
| `/npc list\|mute\|unmute\|speak\|muteall\|unmuteall` | orchestrator.Orchestrator | All proxied to worker via new gRPC RPCs | New gateway handlers + new RPCs |
| `/entity add\|list\|remove\|import` | entity.Store | Postgres entity store on gateway | **Fully reusable as-is** |
| `/campaign info\|load\|switch` | entity.Store, CampaignConfig, isActive | entity.Store shared; config needs tenant extension | Mostly reusable; config source changes |
| `/feedback` | FeedbackStore, sessionID | Store on gateway; sessionID from orch | **Fully reusable** with adapted sessionID fn |

### Implementation Phases

---

#### Phase 1: Tenant Model Extension [Foundation]

Extend the `Tenant` record with fields needed by command handlers. Without
these, permissions and campaign context cannot work per-tenant.

**Tasks:**

- [ ] Add `DMRoleID string` to `gateway.Tenant` and `TenantCreateRequest` /
  `TenantUpdateRequest`
  - `internal/gateway/admin.go`
- [ ] Add `CampaignID string` to `gateway.Tenant` (references which campaign
  config the tenant is using)
  - `internal/gateway/admin.go`
- [ ] Update `MemAdminStore` to persist new fields
  - `internal/gateway/adminstore_mem.go`
- [ ] Update admin API handlers to accept/return new fields
  - `internal/gateway/admin.go`
- [ ] Update `TenantCreateRequest` validation to accept optional `dm_role_id`
  and `campaign_id`

**Tests:**

- [ ] Admin API: create tenant with `dm_role_id` → stored and returned
- [ ] Admin API: update tenant `dm_role_id` → persisted
- [ ] Admin API: create tenant without `dm_role_id` → all users treated as DM

**Success criteria:** Tenant records carry the per-tenant config needed by
command handlers.

**Estimated effort:** Small — struct field additions and API plumbing.

---

#### Phase 2: SessionController Interface [Core]

Extract an interface from `SessionManager` that command handlers can depend on.
Both full mode and gateway mode implement it.

**Tasks:**

- [ ] Define `SessionController` interface in a shared location
  (`internal/app/controller.go` or `internal/discord/commands/controller.go`):
  ```go
  // SessionController abstracts session lifecycle for command handlers.
  // Implemented by app.SessionManager (full mode) and
  // gateway.SessionController (gateway mode).
  type SessionController interface {
      Start(ctx context.Context, channelID, dmUserID string) error
      Stop(ctx context.Context) error
      IsActive() bool
      Info() SessionInfo
  }
  ```
  - `SessionInfo` already exists in `internal/app/session_manager.go:33` —
    move to the interface file or keep and re-export.
- [ ] Add compile-time assertion: `var _ SessionController = (*SessionManager)(nil)`
  - `internal/app/session_manager.go`
- [ ] Refactor `SessionCommands` to depend on `SessionController` instead of
  `*app.SessionManager`
  - `internal/discord/commands/session.go`
- [ ] Refactor `RecapCommands` to depend on `SessionController` for
  `IsActive()`/`Info()` (keep `SessionStore` as separate dependency)
  - `internal/discord/commands/recap.go`
- [ ] Refactor `VoiceRecapCommands` to depend on `SessionController` for
  `IsActive()`/`Info()`
  - `internal/discord/commands/voice_recap.go`
- [ ] Refactor `CampaignCommands` to accept `func() bool` for `isActive` (already
  does — no change needed)
- [ ] Update `runFull()` in `cmd/glyphoxa/main.go` to pass `SessionManager`
  as `SessionController` to command constructors — should be source-compatible
  since `SessionManager` satisfies the interface

**Design decision — interface location:**

Place `SessionController` in `internal/discord/commands/` (consumer-side). This
follows the Go convention of defining interfaces at the point of use rather
than the point of implementation. The `app` package should not depend on
`commands`, and the `gateway` package should not either.

**Tests:**

- [ ] Compile-time assertion for `*app.SessionManager`
- [ ] Existing `session_test.go` still passes (refactored to use interface)

**Success criteria:** Command handlers depend on `SessionController` interface;
full mode still works identically.

**Estimated effort:** Medium — interface extraction, refactor 3 command files,
update wiring in main.go.

---

#### Phase 3: GatewaySessionController [Core]

Implement `SessionController` for gateway mode by composing
`sessionorch.Orchestrator` and `gateway.WorkerClient`.

**Tasks:**

- [ ] Create `internal/gateway/sessionctrl.go` with `GatewaySessionController`:
  ```go
  type GatewaySessionController struct {
      orch       sessionorch.Orchestrator
      worker     WorkerClient
      tenantID   string
      campaignID string
      guildID    string
      tier       config.LicenseTier

      mu     sync.Mutex
      active string // session ID of the active session, empty if none
  }
  ```
- [ ] Implement `Start(ctx, channelID, dmUserID) error`:
  1. Call `orch.ValidateAndCreate()` with tenant/campaign/guild/channel/tier
  2. Call `worker.StartSession()` with the resulting session ID
  3. On success, store session ID in `active` field
  4. On worker failure, call `orch.Transition(sessionID, SessionEnded, err)`
- [ ] Implement `Stop(ctx) error`:
  1. Read `active` session ID
  2. Call `worker.StopSession(sessionID)`
  3. Call `orch.Transition(sessionID, SessionEnded, "")`
  4. Clear `active` field
- [ ] Implement `IsActive() bool`:
  - Check `active != ""` (fast path)
  - Optional: cross-check with `orch.ActiveSessions()` for consistency
- [ ] Implement `Info() SessionInfo`:
  - If `active != ""`, call `orch.GetSession(active)` and map to `SessionInfo`
  - Map `Session.StartedAt` → `SessionInfo.StartedAt`,
    `Session.ChannelID` → `SessionInfo.ChannelID`, etc.
  - `StartedBy` (DM user ID) is not in `sessionorch.Session` — add field
- [ ] Add `StartedBy string` to `sessionorch.SessionRequest` and
  `sessionorch.Session`
  - `internal/gateway/sessionorch/orchestrator.go`
  - `internal/gateway/sessionorch/memory.go`
- [ ] Add compile-time assertion:
  `var _ commands.SessionController = (*GatewaySessionController)(nil)`

**Concurrency note:** The gateway handles one session per guild (enforced by
the orchestrator's license constraints). The `mu` mutex protects the `active`
field for concurrent command handler access.

**Tests:**

- [ ] Start succeeds → `IsActive() == true`, `Info()` returns correct metadata
- [ ] Start when already active → error from orchestrator (constraint violation)
- [ ] Stop succeeds → `IsActive() == false`
- [ ] Stop when not active → error
- [ ] Start fails on worker → orchestrator session transitioned to ended
- [ ] Compile-time interface assertion

**Success criteria:** `/session start` and `/session stop` work in gateway mode
via the orchestrator + worker gRPC path.

**Estimated effort:** Medium — new struct with 4 methods, orchestrator field
additions, tests.

---

#### Phase 4: Gateway Bot Integration [Core]

Enhance `BotConnector` and `BotManager` to create fully wired Discord bots
with command routers and event listeners, rather than bare clients.

**Tasks:**

- [ ] Define `GatewayBot` struct in `internal/gateway/gatewaybot.go`:
  ```go
  type GatewayBot struct {
      client   *bot.Client
      router   *discord.CommandRouter
      perms    *discord.PermissionChecker
      guildIDs []snowflake.ID
      tenantID string
      commands []discordtypes.ApplicationCommand
  }
  ```
  With methods: `Router()`, `Permissions()`, `Client()`, `Close()`,
  `RegisterCommands(ctx)` (registers slash commands with Discord API),
  `UnregisterCommands()`.
- [ ] Update `BotManager` to store `*GatewayBot` instead of `*botEntry`:
  - Change `bots map[string]*botEntry` → `bots map[string]*GatewayBot`
  - `AddBot(tenantID, gwBot)` instead of `AddBot(tenantID, client)`
  - `GetBot(tenantID) (*GatewayBot, bool)` (replaces `Get`)
  - Keep `RouteEvent` for backward compatibility or remove if unused
  - `internal/gateway/botmanager.go`
- [ ] Update `DiscordBotConnector.ConnectBot()` to:
  1. Create `CommandRouter` and `PermissionChecker` from tenant's `DMRoleID`
  2. Create disgo client with event listeners wired to the router (same
     pattern as `discord.Bot.New()` lines 84-95)
  3. Open Discord gateway
  4. Wrap in `GatewayBot`
  5. Register with `BotManager`
  - `internal/gateway/botconnector.go`
- [ ] Add `SetupCommands` method or callback on `BotConnector` / `GatewayBot`
  that accepts a function to register command handlers after the bot is created.
  This decouples bot creation from command handler wiring.
- [ ] Update `runGateway()` in `cmd/glyphoxa/main.go` to:
  1. Pass a command registration callback to `BotConnector`
  2. In the callback, create `GatewaySessionController` and wire command handlers
  3. Call `gwBot.RegisterCommands(ctx)` to register with Discord API
- [ ] Update `AdminAPI.createTenant` and `AdminAPI.updateTenant` to pass
  `DMRoleID` through to `BotConnector.ConnectBot()`
  - Extend `ConnectBot` signature or add a `TenantConfig` struct parameter

**Design decision — per-tenant command registration:**

Each tenant's bot registers the same set of slash commands (same definitions)
but with different handler instances backed by different tenant state. The
`CommandRouter` is per-tenant, so handlers can close over tenant-specific
dependencies (session controller, stores).

**Design decision — BotConnector signature change:**

Rather than passing individual fields, introduce a `BotConfig` struct:
```go
type BotConfig struct {
    TenantID string
    BotToken string
    GuildIDs []string
    DMRoleID string
    // Future: CampaignID, NPCConfig source, etc.
}
```

This avoids repeated signature changes as per-tenant config grows.

**Tests:**

- [ ] `GatewayBot` creates router and registers event listeners
- [ ] `GatewayBot.RegisterCommands` registers commands with Discord REST API
- [ ] `GatewayBot.Close` unregisters commands and closes client
- [ ] `BotManager` stores and retrieves `GatewayBot` instances
- [ ] `BotConnector.ConnectBot` with `DMRoleID` → `PermissionChecker` configured

**Success criteria:** Creating a tenant with a bot token results in a Discord
bot with slash commands registered in the guild.

**Estimated effort:** Large — significant refactor of BotManager/BotConnector,
new GatewayBot struct, wiring changes in runGateway.

---

#### Phase 5: Wire Session + Store Commands [Integration]

Connect the session, entity, campaign, and feedback command handlers to the
gateway bot infrastructure from Phase 4.

**Tasks:**

- [ ] In `runGateway()`, create per-tenant command wiring function:
  ```go
  func wireCommands(gwBot *GatewayBot, ctrl commands.SessionController,
      stores TenantStores, tenant Tenant) {
      router := gwBot.Router()
      perms := gwBot.Permissions()

      // Session commands
      commands.NewSessionCommands(gwBot, ctrl, perms)
      commands.NewRecapCommands(commands.RecapConfig{...})

      // Entity commands (shared Postgres store)
      entityCmds := commands.NewEntityCommands(perms, stores.EntityStore)
      entityCmds.Register(router)

      // Campaign commands
      campaignCmds := commands.NewCampaignCommands(...)
      campaignCmds.Register(router)

      // Feedback commands
      feedbackCmds := commands.NewFeedbackCommands(...)
      feedbackCmds.Register(router)
  }
  ```
- [ ] Adapt `NewSessionCommands` to accept `SessionController` interface
  (from Phase 2). The constructor currently takes `*discordbot.Bot` — for
  gateway mode, either:
  - (a) Make it accept a `BotLike` interface with `GuildID()`,
    `Client()`, `Router()` methods, or
  - (b) Pass router/guildID/client as separate params
  - Option (b) is simpler and avoids a new interface for the bot itself
- [ ] Create `TenantStores` struct that holds per-tenant store accessors:
  ```go
  type TenantStores struct {
      EntityStore  func() entity.Store
      SessionStore memory.SessionStore
      RecapStore   memory.RecapStore
      Feedback     commands.FeedbackStore
  }
  ```
- [ ] For entity/campaign/feedback commands, resolve stores from shared
  Postgres with tenant schema isolation (using `config.TenantContext`)
- [ ] Wire `GatewaySessionController` per-tenant in the command registration
  callback, with the tenant's orchestrator, worker client, and config

**Note on store creation:** In full mode, stores are created once by
`app.New()`. In gateway mode, stores need to be created per-tenant with the
tenant's schema. This may require a `StoreFactory` that takes a
`TenantContext` and returns the appropriate stores. This is part of the larger
multi-tenant storage story and can use existing Postgres schema isolation.

**Tests:**

- [ ] Integration test: create tenant → bot connects → slash commands registered
- [ ] `/session start` on gateway bot → orch validates → worker starts session
- [ ] `/session stop` on gateway bot → worker stops → orch transitions
- [ ] `/entity list` on gateway bot → returns entities from tenant's schema

**Success criteria:** Session start/stop, entity, campaign, and feedback
commands work end-to-end through the gateway.

**Estimated effort:** Medium — primarily wiring and plumbing.

---

#### Phase 6: Worker gRPC Extensions for NPC Commands [Core]

Extend the gRPC contract so the gateway can proxy NPC management commands
to the worker where the `orchestrator.Orchestrator` lives.

**Tasks:**

- [ ] Add new RPCs to `proto/glyphoxa/v1/session.proto`:
  ```protobuf
  service SessionWorkerService {
      // Existing
      rpc StartSession(StartSessionRequest) returns (StartSessionResponse);
      rpc StopSession(StopSessionRequest) returns (StopSessionResponse);
      rpc GetStatus(GetStatusRequest) returns (GetStatusResponse);

      // New — NPC management
      rpc ListNPCs(ListNPCsRequest) returns (ListNPCsResponse);
      rpc MuteNPC(MuteNPCRequest) returns (MuteNPCResponse);
      rpc UnmuteNPC(UnmuteNPCRequest) returns (UnmuteNPCResponse);
      rpc SpeakNPC(SpeakNPCRequest) returns (SpeakNPCResponse);
      rpc MuteAllNPCs(MuteAllNPCsRequest) returns (MuteAllNPCsResponse);
      rpc UnmuteAllNPCs(UnmuteAllNPCsRequest) returns (UnmuteAllNPCsResponse);
  }
  ```
- [ ] Define proto messages:
  ```protobuf
  message NPCInfo {
      string id = 1;
      string name = 2;
      bool muted = 3;
  }

  message ListNPCsRequest { string session_id = 1; }
  message ListNPCsResponse { repeated NPCInfo npcs = 1; }

  message MuteNPCRequest { string session_id = 1; string name = 2; }
  message MuteNPCResponse {}
  // ... similar for unmute, speak, muteall, unmuteall
  ```
- [ ] Regenerate Go code: `make proto` or `buf generate`
- [ ] Implement new RPCs in `grpctransport.WorkerServer`
  - Delegate to `WorkerHandler` interface (extended with new methods)
  - `internal/gateway/grpctransport/server.go`
- [ ] Extend `grpctransport.WorkerHandler` interface with NPC methods
- [ ] Implement NPC methods in `session.WorkerHandler`:
  - Look up active `Runtime` for the session ID
  - Access `Runtime.Orchestrator()` to call `MuteAgent`, `UnmuteAgent`, etc.
  - `internal/session/worker_handler.go`
- [ ] Implement new RPCs in `grpctransport.Client` (gateway-side)
  - `internal/gateway/grpctransport/client.go`
- [ ] Implement new methods in `local.Client` for full mode (direct delegation)
  - `internal/gateway/local/local.go`
- [ ] Extend `gateway.WorkerClient` interface with NPC methods
  - `internal/gateway/contract.go`

**Tests:**

- [ ] gRPC roundtrip: `ListNPCs` → returns NPCs from worker runtime
- [ ] gRPC roundtrip: `MuteNPC` → agent muted on worker
- [ ] gRPC roundtrip: `SpeakNPC` → agent speaks text on worker
- [ ] Local client: NPC methods delegate directly
- [ ] Error handling: NPC not found → gRPC error code

**Success criteria:** Gateway can query and control NPCs on the worker via
gRPC.

**Estimated effort:** Large — proto changes, codegen, 6 new RPCs, worker
handler extensions.

---

#### Phase 7: Gateway NPC Command Handlers [Integration]

Create gateway-specific NPC command handlers that proxy through gRPC to the
worker.

**Tasks:**

- [ ] Create `internal/discord/commands/npc_gateway.go` with
  `GatewayNPCCommands`:
  ```go
  type GatewayNPCCommands struct {
      perms   *discord.PermissionChecker
      worker  gateway.WorkerClient
      getSessionID func() string
  }
  ```
- [ ] Implement all handlers (`list`, `mute`, `unmute`, `speak`, `muteall`,
  `unmuteall`) by calling the corresponding `WorkerClient` methods
- [ ] Reuse the same `Definition()` from `NPCCommands` (command definitions
  are identical; only the handler implementation differs)
- [ ] Register autocomplete handler — fetch NPC names from worker via
  `ListNPCs` gRPC call
- [ ] Wire `GatewayNPCCommands` in the per-tenant command registration function
  from Phase 5

**Design decision — separate handler struct vs adapter:**

Using a separate `GatewayNPCCommands` struct (rather than making `NPCCommands`
accept an interface) is cleaner because:
- The full-mode `NPCCommands` takes `func() *orchestrator.Orchestrator` which
  gives synchronous in-process access — fundamentally different from async gRPC
- Gateway handlers need error handling for gRPC failures (timeouts, worker down)
- The handlers share `Definition()` but not implementation

**Tests:**

- [ ] `/npc list` returns NPC list from worker (mocked gRPC)
- [ ] `/npc mute <name>` → worker mutes NPC
- [ ] `/npc speak <name> <text>` → worker makes NPC speak
- [ ] Autocomplete returns NPC names from worker
- [ ] Worker unavailable → graceful error message to user

**Success criteria:** All `/npc` subcommands work in gateway mode.

**Estimated effort:** Medium — handler implementations are straightforward
given the gRPC plumbing from Phase 6.

---

#### Phase 8: Recap and Voice Recap [Polish]

Make `/session recap` fully functional and `/session voice-recap` workable
in gateway mode.

**Tasks:**

- [ ] **Text recap** (`/session recap`):
  - Already works if `SessionStore` (Postgres) is accessible from the gateway
  - Refactor `RecapCommands.buildNPCList()` to handle nil orchestrator
    gracefully (display "NPC list unavailable in gateway mode" or fetch via
    `WorkerClient.ListNPCs`)
  - Alternatively, make `RecapCommands` optionally accept a
    `func() []NPCInfo` for NPC list in recap embed
- [ ] **Voice recap** (`/session voice-recap`):
  - **Option A (recommended for MVP):** Generate recap text on the gateway
    (LLM call from gateway), post as text-only embed. Skip audio playback.
    Voice recap requires `Mixer` (worker-side) and `TTS` (could be
    gateway-side). For MVP, text recap is sufficient.
  - **Option B (full):** Add `PlayRecapAudio` gRPC RPC to worker contract.
    Gateway generates recap text + audio (needs LLM + TTS providers on
    gateway), sends audio data to worker for playback via mixer. Or, send
    only text to worker and have worker generate + play.
  - For Phase 8, implement **Option A** (text-only) and leave **Option B**
    as a follow-up.
- [ ] Add `ListNPCNames(sessionID string) ([]string, error)` helper to
  `GatewaySessionController` that calls `WorkerClient.ListNPCs`
- [ ] Update `VoiceRecapCommands` to gracefully degrade when mixer is
  unavailable: post text embed with a note that voice playback requires
  full mode

**Tests:**

- [ ] `/session recap` in gateway mode → text recap with transcript
- [ ] `/session recap` NPC list gracefully handles nil orchestrator
- [ ] `/session voice-recap` in gateway mode → text-only recap posted

**Success criteria:** Recap commands produce useful output in gateway mode
even without direct access to worker-side resources.

**Estimated effort:** Small-Medium — mostly graceful degradation and optional
gRPC calls.

---

## Implementation Order and Dependencies

```
Phase 1 (Tenant Model) ──────────────────────────┐
                                                   │
Phase 2 (SessionController Interface) ────────────┤
                                                   │
Phase 3 (GatewaySessionController) ◀──────────────┤
          depends on Phase 2                       │
                                                   │
Phase 4 (Gateway Bot Integration) ◀────────────────┤
          depends on Phase 1                       │
                                                   │
Phase 5 (Wire Commands) ◀─────────────────────────┤
          depends on Phases 2, 3, 4                │
                                                   │
Phase 6 (Worker gRPC Extensions) ─────────────────┤
          independent of Phases 1-5                │
                                                   │
Phase 7 (Gateway NPC Handlers) ◀───────────────────┤
          depends on Phases 4, 5, 6                │
                                                   │
Phase 8 (Recap/Voice Recap) ◀──────────────────────┘
          depends on Phases 5, 6
```

**Parallelization opportunities:**
- Phases 1 and 2 can be done in parallel (no dependency)
- Phase 6 (gRPC extensions) can be done in parallel with Phases 1-5

**Recommended order:** 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8

## Acceptance Criteria

### Functional Requirements

- [ ] Creating a tenant with a bot token → Discord bot connects with slash
  commands registered in the guild
- [ ] `/session start` in gateway mode → orchestrator validates constraints →
  worker starts voice pipeline → user sees confirmation
- [ ] `/session stop` in gateway mode → worker stops pipeline → orchestrator
  transitions to ended → user sees confirmation
- [ ] `/session recap` in gateway mode → text recap from shared Postgres
- [ ] `/npc list|mute|unmute|speak|muteall|unmuteall` in gateway mode →
  proxied to worker via gRPC → user sees result
- [ ] `/entity add|list|remove|import` in gateway mode → works with
  tenant-scoped Postgres schema
- [ ] `/campaign info|load|switch` in gateway mode → works with tenant config
- [ ] `/feedback` in gateway mode → persists feedback
- [ ] Deleting a tenant → bot disconnects, commands unregistered
- [ ] Updating a tenant's bot token → old bot disconnected, new bot connected
  with commands

### Non-Functional Requirements

- [ ] All new code has `t.Parallel()` tests with table-driven subtests
- [ ] Race detector clean (`-race -count=1`)
- [ ] Compile-time interface assertions where applicable
- [ ] gRPC NPC RPCs have <100ms overhead on top of the orchestrator operation
- [ ] Bot connection failure for one tenant does not affect other tenants
- [ ] Worker gRPC failure → graceful error message to Discord user

## Explicitly Out of Scope

| Item | Reason | Tracked |
|---|---|---|
| Per-tenant NPC definitions in Postgres | Requires campaign config CRUD — separate feature | Future issue |
| Per-tenant campaign config hot-reload | Requires config management API | Future issue |
| Voice recap audio playback in gateway mode | Requires audio data transfer over gRPC | Phase 8 Option B (follow-up) |
| Worker pool / load balancing | Requires scheduler — separate infrastructure concern | Future issue |
| PostgreSQL admin store (replacing MemAdminStore) | Separate persistence story | Future issue |
| Tenant quota enforcement in command handlers | `usage/` package exists but not wired to commands | Future issue |
| Dashboard embed updates in gateway mode | Requires `bot.Client` access from session events | Future issue |

## Dependencies & Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Proto changes break existing gRPC clients | Build failure | New RPCs are additive; existing messages unchanged. Version workers before gateway. |
| Per-tenant store creation at scale | Memory / connection pressure | Use connection pooling; lazy store creation; consider shared pool with schema switching |
| BotManager refactor breaks existing tests | Test failures | Incremental refactor; keep backward compat during transition |
| Worker unavailable when slash command arrives | User sees error | Circuit breaker on gRPC client; clear error message ("Session service temporarily unavailable") |
| Multiple guildIDs per tenant | Command registration complexity | Phase 4 registers commands per-guild; each guild gets the same command set |
| Autocomplete latency for NPC names (gRPC round-trip) | Slow autocomplete | Cache NPC names on gateway after session start; invalidate on mute/unmute events |
| `SessionController` interface too narrow/wide | Refactoring churn | Start narrow (4 methods); extend only as command handlers actually need more |

## References & Research

### Internal References

- Full mode wiring: `cmd/glyphoxa/main.go:137` (runFull)
- Gateway mode wiring: `cmd/glyphoxa/main.go:278` (runGateway)
- Discord bot wrapper: `internal/discord/bot.go:43` (Bot struct)
- Command router: `internal/discord/router.go:32` (CommandRouter)
- Session manager: `internal/app/session_manager.go:53` (SessionManager)
- Session commands: `internal/discord/commands/session.go:17` (SessionCommands)
- NPC commands: `internal/discord/commands/npc.go:17` (NPCCommands)
- Entity commands: `internal/discord/commands/entity.go:26` (EntityCommands)
- Campaign commands: `internal/discord/commands/campaign.go:19` (CampaignCommands)
- Recap commands: `internal/discord/commands/recap.go:27` (RecapCommands)
- Voice recap: `internal/discord/commands/voice_recap.go:25` (VoiceRecapCommands)
- Feedback commands: `internal/discord/commands/feedback.go:37` (FeedbackCommands)
- Bot manager: `internal/gateway/botmanager.go:27` (BotManager)
- Bot connector: `internal/gateway/botconnector.go:15` (DiscordBotConnector)
- Session orchestrator: `internal/gateway/sessionorch/orchestrator.go:45` (Orchestrator interface)
- Memory orchestrator: `internal/gateway/sessionorch/memory.go:23` (MemoryOrchestrator)
- gRPC contract: `internal/gateway/contract.go:74` (WorkerClient interface)
- gRPC transport client: `internal/gateway/grpctransport/client.go:23` (Client)
- gRPC transport server: `internal/gateway/grpctransport/server.go:17` (WorkerServer)
- Local transport: `internal/gateway/local/local.go:30` (Client)
- Admin API: `internal/gateway/admin.go:77` (AdminAPI)
- Permissions: `internal/discord/permissions.go:20` (PermissionChecker)
- Respond helpers: `internal/discord/respond.go` (RespondEphemeral, DeferReply, etc.)
- Tenant context: `internal/config/tenant.go:53` (TenantContext)
