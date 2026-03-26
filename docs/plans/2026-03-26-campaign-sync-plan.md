---
title: "Campaign Sync — Web UI & Discord Bot Unification"
type: plan
status: draft
date: 2026-03-26
depends_on:
  - docs/plans/web-management/01-architecture.md (Section 5 — Service Boundaries)
  - docs/plans/web-management/03-database-schema.md
---

# Campaign Sync Plan — Web Management UI & Discord Bot

## Problem Statement

Campaigns created in the web management UI are invisible to Discord slash commands,
and vice versa. A DM who creates a campaign at `app.glyphoxa.com` cannot select it
via `/campaign switch` in Discord. A DM who uploads a campaign YAML in Discord
doesn't see it in the web dashboard.

The root cause: the web service and the gateway/Discord bot use **completely separate
data sources** for campaigns — the web writes to `mgmt.campaigns` in PostgreSQL,
while the gateway reads from YAML config files loaded at startup.

---

## 1. Current State Analysis

### 1.1 Web Management Service

| Aspect | Detail |
|--------|--------|
| **Campaign store** | `mgmt.campaigns` table (PostgreSQL) |
| **CRUD** | Full REST API: POST/GET/PUT/DELETE `/api/v1/campaigns` |
| **Model** | id, tenant_id, name, system, language, description, timestamps, soft-delete |
| **Related data** | Lore documents (`mgmt.lore_documents`), NPC links (`mgmt.campaign_npcs`), knowledge entities |
| **Session start** | Passes `campaign_id` to gateway via gRPC `StartWebSession` RPC |
| **NPC management** | Full CRUD per campaign, stored in `mgmt` schema |
| **Source files** | `internal/web/handlers_campaigns.go`, `internal/web/store.go`, `internal/web/migrations/001_initial.sql` |

### 1.2 Gateway / Discord Bot

| Aspect | Detail |
|--------|--------|
| **Campaign store** | `config.CampaignConfig` struct, loaded from YAML at startup |
| **Tenant campaign** | `Tenant.CampaignID` field exists in Go struct **but is NOT persisted** to the `tenants` PostgreSQL table (lost on gateway restart) |
| **NPC loading** | NPC configs read from YAML at startup, snapshot-sent to workers. The `npc_definitions` table exists but is used by full-mode entity store, not gateway-mode NPC loading |
| **`/campaign info`** | Reads from YAML config — shows nothing if config is empty |
| **`/campaign load`** | Uploads YAML and imports entities — **full mode only**, unavailable in gateway mode |
| **`/campaign switch`** | **Stub** — responds "Switched to campaign X" but doesn't actually change anything. Autocomplete only shows the single YAML config campaign name |
| **Session controller** | `GatewaySessionController.Start()` uses `gc.campaignID` from construction time, **ignores** `req.CampaignID` from the web service's `StartWebSession` request |
| **Source files** | `internal/discord/commands/campaign.go`, `internal/gateway/sessionctrl.go`, `internal/gateway/adminstore_postgres.go`, `internal/gateway/admin.go` |

### 1.3 Architecture Plan vs Reality

The architecture plan (01-architecture.md, Section 5) already prescribes the correct design:

| Concern | Web Management Service | Gateway | Shared (DB) |
|---------|----------------------|---------|-------------|
| **Campaign CRUD** | Owns | Reads campaign context | `campaigns` table |
| **NPC CRUD** | Owns (HTTP) | Reads NPC defs at session start | `npc_definitions` table |

**This hasn't been implemented.** The web service created its own `mgmt.*` tables, but the gateway was never updated to read from them. The two services evolved independently.

---

## 2. Gap Analysis

### Gap 1: No Shared Campaign Source of Truth

- **Web** writes to `mgmt.campaigns` (PostgreSQL, `mgmt` schema)
- **Gateway** reads from `config.CampaignConfig` (YAML file, loaded once at startup)
- Neither reads from the other

### Gap 2: Tenant `campaign_id` Not Persisted

The `tenants` table migration (`000001_tenants.up.sql`) has no `campaign_id` or `dm_role_id` columns:

```sql
CREATE TABLE IF NOT EXISTS tenants (
    id                    TEXT PRIMARY KEY,
    license_tier          TEXT NOT NULL DEFAULT 'shared',
    bot_token             TEXT NOT NULL DEFAULT '',
    guild_ids             TEXT[] DEFAULT '{}',
    monthly_session_hours NUMERIC(10,2) NOT NULL DEFAULT 0,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

The `PostgresAdminStore` SQL (SELECT/INSERT/UPDATE) also excludes these fields. The Go struct fields exist but are **in-memory ephemera**.

### Gap 3: `/campaign switch` Is a No-Op

```go
// handleSwitch in campaign.go — responds but changes nothing
discordbot.RespondEphemeral(e, fmt.Sprintf("Switched to campaign **%s**.", name))
```

The autocomplete only returns the current YAML config name:

```go
func (cc *CampaignCommands) autocompleteCampaignSwitch(...) {
    cfg := cc.getCfg()
    if cfg != nil && cfg.Name != "" {
        choices = append(choices, ...)  // Only one choice: current config
    }
}
```

### Gap 4: Web Campaign ID Ignored on Session Start

`GatewaySessionController.Start()` at `sessionctrl.go:151`:

```go
sessionID, err := gc.orch.ValidateAndCreate(ctx, gc.tenantID, gc.campaignID, ...)
//                                                              ^^^^^^^^^^^^^^
//                                              hardcoded from controller construction
//                                              req.CampaignID is never used
```

The web service sends `campaign_id` in `StartWebSession`, but it's silently discarded.

### Gap 5: NPC Definitions Not Shared

- Web manages NPCs via REST API, stored in `mgmt` schema tables
- Gateway loads NPCs from YAML `cfg.NPCs` at startup, converts to `gw.NPCConfigMsg`, snapshots to workers
- The `npc_definitions` table (in `npcstore` package) exists for the entity store but isn't used by gateway-mode NPC loading
- Same sync problem as campaigns

### Gap 6: No Change Notification

When a DM creates/updates a campaign or NPC in the web UI, the gateway has no mechanism to learn about the change. No webhooks, no polling, no pub/sub.

---

## 3. Proposed Solution

### 3.1 Design Principles

1. **`mgmt.campaigns` is the single source of truth** — all campaign CRUD flows through the web service's DB tables
2. **Gateway reads from DB, not config** — campaigns and NPCs loaded from PostgreSQL at session start, not from YAML
3. **YAML config becomes optional bootstrap** — existing YAML campaigns can be imported into the DB as a migration path
4. **Minimal gateway changes** — the gateway stays lean; the web service owns the data model
5. **PostgreSQL LISTEN/NOTIFY for real-time sync** — lightweight, no external message broker needed

### 3.2 Architecture

```
                    ┌─────────────────────────┐
                    │     Web Management UI    │
                    │  (campaign/NPC CRUD)     │
                    └────────────┬────────────┘
                                 │ REST API
                                 ▼
                    ┌─────────────────────────┐
                    │   Web Management Service │
                    │  (owns mgmt.campaigns,   │
                    │   mgmt.lore_documents,   │
                    │   npc_definitions)        │
                    └────────────┬────────────┘
                                 │ PostgreSQL writes +
                                 │ NOTIFY 'campaign_changed'
                                 ▼
                    ┌─────────────────────────┐
                    │       PostgreSQL         │
                    │  ┌───────────────────┐  │
                    │  │  mgmt.campaigns   │  │
                    │  │  npc_definitions  │  │
                    │  │  tenants          │  │
                    │  │  sessions         │  │
                    │  └───────────────────┘  │
                    └────────────┬────────────┘
                                 │ DB read + LISTEN
                                 ▼
    ┌────────────────────────────────────────────────┐
    │               Gateway                           │
    │  ┌──────────────┐   ┌──────────────────────┐   │
    │  │ BotManager   │   │ CampaignReader       │   │
    │  │ (per-tenant  │◄──│ (reads mgmt.campaigns│   │
    │  │  bots)       │   │  + npc_definitions   │   │
    │  └──────┬───────┘   │  on session start)   │   │
    │         │           └──────────────────────┘   │
    │         ▼                                       │
    │  ┌──────────────┐                               │
    │  │SessionCtrl   │ Uses req.CampaignID (not     │
    │  │(per session) │ hardcoded controller field)  │
    │  └──────────────┘                               │
    └────────────────────────────────────────────────┘
                                 │
    ┌────────────────────────────┼────────────────────┐
    │         Discord            │                     │
    │  /campaign info   ── reads from DB              │
    │  /campaign switch ── lists DB campaigns,         │
    │                      updates tenant.campaign_id │
    │  /campaign load   ── writes to mgmt.campaigns   │
    └─────────────────────────────────────────────────┘
```

### 3.3 Detailed Changes

#### A. Persist `campaign_id` and `dm_role_id` on tenants table

New migration `000002_tenant_campaign.up.sql`:

```sql
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS campaign_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS dm_role_id  TEXT NOT NULL DEFAULT '';
```

Update `PostgresAdminStore` SQL queries (SELECT, INSERT, UPDATE) to include both columns. Update `scanTenant()` to scan them.

#### B. Gateway reads campaigns from `mgmt.campaigns`

Create a lightweight `CampaignReader` in the gateway that queries `mgmt.campaigns`:

```go
// internal/gateway/campaignreader.go
type CampaignReader struct {
    pool *pgxpool.Pool
}

func (r *CampaignReader) ListForTenant(ctx context.Context, tenantID string) ([]CampaignSummary, error) {
    // SELECT id, name, system, language FROM mgmt.campaigns
    // WHERE tenant_id = $1 AND deleted_at IS NULL ORDER BY name
}

func (r *CampaignReader) Get(ctx context.Context, tenantID, campaignID string) (*CampaignSummary, error) {
    // SELECT id, name, system, language, description FROM mgmt.campaigns
    // WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
}
```

The gateway reads but never writes `mgmt.campaigns` — ownership stays with the web service.

#### C. Gateway reads NPC definitions from DB at session start

Instead of loading NPCs from YAML config at startup and snapshotting them forever, the session controller queries `npc_definitions` when starting a session:

```go
func (gc *GatewaySessionController) Start(ctx context.Context, req SessionStartRequest) error {
    // Use the request's CampaignID (from web or Discord), not gc.campaignID
    campaignID := req.CampaignID
    if campaignID == "" {
        campaignID = gc.campaignID  // fallback to tenant default
    }

    // Load NPCs fresh from DB for this campaign
    npcs, err := gc.npcStore.List(ctx, npcstore.ListOptions{CampaignID: campaignID})
    // ... convert to NPCConfigMsg and send to worker
}
```

#### D. Fix `/campaign switch` to actually switch

```go
func (cc *CampaignCommands) handleSwitch(e *events.ApplicationCommandInteractionCreate) {
    // 1. List available campaigns for this tenant from DB
    // 2. Validate the selected campaign exists
    // 3. Update tenant.campaign_id in the tenants table
    // 4. Reconnect the session controller with new campaign context
    // 5. Respond with confirmation
}

func (cc *CampaignCommands) autocompleteCampaignSwitch(e *events.AutocompleteInteractionCreate) {
    // Query mgmt.campaigns for this tenant, return as autocomplete choices
    // Filter by partial input match
}
```

#### E. Fix `/campaign load` to write to DB (not just entity store)

When a DM uploads a YAML campaign via Discord:
1. Parse the YAML as before
2. Create a `mgmt.campaigns` record (via a new gateway → web service internal API, or direct DB write)
3. Import entities into `npc_definitions` table
4. Set as the tenant's active campaign

#### F. Fix `/campaign info` to read from DB

Instead of reading from `config.CampaignConfig`, read from `mgmt.campaigns` using the tenant's `campaign_id`.

#### G. Session controller uses request CampaignID

In `GatewaySessionController.Start()`, use `req.CampaignID` instead of `gc.campaignID`:

```go
// Before (broken):
sessionID, err := gc.orch.ValidateAndCreate(ctx, gc.tenantID, gc.campaignID, ...)

// After (fixed):
campaignID := req.CampaignID
if campaignID == "" {
    campaignID = gc.campaignID  // fallback to tenant's default
}
sessionID, err := gc.orch.ValidateAndCreate(ctx, gc.tenantID, campaignID, ...)
```

#### H. Optional: PostgreSQL LISTEN/NOTIFY for cache invalidation

If the gateway caches campaign/NPC data (e.g., for autocomplete responsiveness):

```sql
-- Web service issues after campaign CRUD:
NOTIFY campaign_changed, '{"tenant_id":"abc","campaign_id":"xyz","action":"updated"}';
```

Gateway listens and invalidates its cache. Not required for correctness (fresh DB reads on session start are sufficient) but improves UX for autocomplete and `/campaign info`.

### 3.4 YAML Config Deprecation Path

| Phase | YAML Config Role | DB Role |
|-------|-----------------|---------|
| **Now** | Primary source for gateway campaigns | Web service only |
| **Phase 1** (this plan) | Optional bootstrap / import source | Primary source for both |
| **Phase 2** | Removed from gateway startup path | Sole source of truth |

YAML campaigns in existing `glyphoxa.yaml` configs are imported into the DB via a one-time migration command:

```bash
glyphoxa migrate-campaigns --config glyphoxa.yaml --tenant-id <id>
```

This reads `campaign.*` and `npcs.*` from the YAML, creates `mgmt.campaigns` and `npc_definitions` records, and sets the tenant's default `campaign_id`.

---

## 4. Implementation Steps

### Phase 1: Database Foundation (effort: S)

1. **Add tenant migration** — `000002_tenant_campaign.up.sql` with `campaign_id` and `dm_role_id` columns
2. **Update `PostgresAdminStore`** — include new columns in all SQL queries and `scanTenant()`
3. **Add `CampaignReader`** to gateway — read-only access to `mgmt.campaigns`
4. **Tests** — update admin store tests, add campaign reader tests

### Phase 2: Session Controller Fix (effort: S)

5. **Fix `GatewaySessionController.Start()`** — use `req.CampaignID` with fallback to tenant default
6. **Load NPCs from DB at session start** — query `npc_definitions` by campaign_id instead of using snapshot
7. **Inject `npcStore` into session controller** — add `npcstore.Store` dependency
8. **Tests** — update session controller tests, verify campaign_id flows through

### Phase 3: Discord Command Overhaul (effort: M)

9. **Inject `CampaignReader` into `CampaignCommands`** — replace `getCfg` dependency
10. **Rewrite `/campaign info`** — read from DB instead of YAML config
11. **Rewrite `/campaign switch`** — list campaigns from DB, validate selection, update tenant `campaign_id`, respond with embed
12. **Rewrite autocomplete** — query `mgmt.campaigns` for tenant, filter by partial input
13. **Update `/campaign load`** — write parsed campaign to `mgmt.campaigns` + `npc_definitions`, set as active
14. **Tests** — table-driven tests for all command handlers

### Phase 4: Migration Tooling (effort: S)

15. **`migrate-campaigns` CLI command** — imports YAML config campaigns into DB
16. **Deprecation warnings** — log warning if `campaign.*` config is set in YAML, pointing to web UI
17. **Documentation update** — update README and deployment docs

### Phase 5: Optional Enhancements (effort: S)

18. **LISTEN/NOTIFY** — web service emits notifications on campaign/NPC changes
19. **Gateway cache** — lightweight in-memory cache with NOTIFY-driven invalidation
20. **`/campaign create` Discord command** — create campaigns directly from Discord (writes to DB via gateway → web API or direct write)

---

## 5. NPC Sync — Same Pattern

The NPC definition sync follows the exact same pattern as campaigns. The web service already manages NPCs in the `mgmt` schema. The changes needed:

1. **Gateway reads `npc_definitions` from DB at session start** — the `npcstore.PostgresStore` already exists and supports `List(ctx, ListOptions{CampaignID: id})`
2. **Discord `/npc` commands read/write from DB** — instead of in-memory-only NPC state
3. **YAML NPC config becomes optional bootstrap** — same migration path as campaigns

This is addressed in Phase 2, steps 6-7 above. The `npcstore` package is already well-designed for this — it just needs to be wired into the gateway session startup path.

---

## 6. Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| **Schema migration on live DB** | Medium | High — tenants table is gateway-critical | Use `IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS`. Default values prevent null issues. Test migration on staging first. |
| **Cross-schema reads (gateway → mgmt)** | Low | Medium — if mgmt schema doesn't exist on gateway-only deployments | Gateway's `CampaignReader` handles missing schema gracefully (returns empty list). |
| **Performance of DB reads on session start** | Low | Low — single SELECT with index | `idx_mgmt_campaigns_tenant` already exists. NPC list query is indexed by `campaign_id`. Sub-millisecond. |
| **Backward compatibility** | Medium | Medium — existing YAML-based deployments break | Phase 4 migration tool + deprecation warnings provide smooth transition. YAML config continues working during transition (fallback to config if DB is empty). |
| **Concurrent campaign switches** | Low | Low — one campaign per tenant at a time | Tenant update is atomic (single UPDATE). Session start checks campaign exists. |
| **Gateway restart loses active campaign** | Previously High (not persisted) | **Resolved** by persisting `campaign_id` in tenants table | Phase 1, step 1-2. |

---

## 7. Out of Scope

- **Real-time campaign collaboration** (multiple DMs editing simultaneously) — future feature
- **Campaign versioning / history** — can be added later with an audit trail
- **Cross-tenant campaign sharing / templates** — separate feature
- **Campaign import from VTTs (Foundry, Roll20)** — existing VTT import is entity-level, not campaign-level
- **Billing integration** (campaign limits per tier) — tracked separately in the billing plan
