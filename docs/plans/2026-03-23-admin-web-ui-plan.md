---
title: "feat: Tenant/User/Campaign Admin Web UI"
type: feat
status: draft
date: 2026-03-23
---

# feat: Tenant/User/Campaign Admin Web UI

## Overview

Build a web-based administration panel for managing Glyphoxa tenants, users,
campaigns, NPCs, sessions, providers, and usage/billing. The UI will sit on top
of the existing Admin API (and its required extensions), providing a graphical
interface for everything currently done via `curl` against
`/api/v1/tenants`.

## Problem Statement / Motivation

Today, Glyphoxa management is entirely API-driven — tenant CRUD goes through
the Admin API with `X-API-Key` auth, NPC definitions live in YAML config files,
and there's no visibility into active sessions, usage, or provider health without
querying the database directly. This works for a single developer but does not
scale to:

- **Multiple tenants** who need self-service campaign/NPC management
- **DMs** who want to configure NPCs, voices, and personalities without editing YAML
- **Operators** who need session monitoring, usage dashboards, and provider health
- **Billing workflows** that require quota visibility and adjustment

A web UI makes Glyphoxa accessible to non-technical users and provides
operational visibility for production deployments.

## Current State Analysis

### What the Admin API covers today

| Endpoint                     | Method   | Purpose                        |
|------------------------------|----------|--------------------------------|
| `POST /api/v1/tenants`       | POST     | Create tenant                  |
| `GET /api/v1/tenants`        | GET      | List all tenants               |
| `GET /api/v1/tenants/{id}`   | GET      | Get tenant by ID               |
| `PUT /api/v1/tenants/{id}`   | PUT      | Update tenant                  |
| `DELETE /api/v1/tenants/{id}`| DELETE   | Delete tenant                  |

**Auth:** Single shared `GLYPHOXA_ADMIN_API_KEY` (Bearer token or X-API-Key header).

**Data model (Tenant):**
- `id`, `license_tier` (shared/dedicated), `bot_token`, `guild_ids[]`,
  `dm_role_id`, `campaign_id`, `monthly_session_hours`, timestamps

### What's missing from the API

| Domain           | Status      | Notes                                             |
|------------------|-------------|---------------------------------------------------|
| Tenant CRUD      | **Exists**  | Fully functional                                  |
| Campaign CRUD    | **Missing** | Campaigns defined in YAML, no API                 |
| NPC CRUD         | **Partial** | `npcstore.PostgresStore` exists but not exposed via HTTP |
| User/Role mgmt   | **Missing** | No user model — only API key + Discord `dm_role_id` |
| Session monitoring| **Missing** | `sessionorch` has data but no API to query it     |
| Usage/billing    | **Missing** | `usage.Store` has data but no API                 |
| Provider config  | **Missing** | Providers set via YAML config, no runtime API     |
| Health/metrics   | **Partial** | `/healthz`, `/readyz` exist; Prometheus metrics exposed |

### Existing database tables

**Gateway DB:**
- `tenants` — tenant records (with Vault-encrypted bot tokens)
- `sessions` — voice session lifecycle (state machine: pending→active→ended)
- `usage_records` — monthly aggregates per tenant (session_hours, llm_tokens, stt_seconds, tts_chars)

**Application DB (per-tenant schema):**
- `npc_definitions` — NPC config (personality, voice, engine, knowledge, tools, budget tier)
- `session_entries` — L1 transcript log (speaker, text, raw_text, npc_id, timestamps)
- `chunks` — L2 semantic embeddings (pgvector)
- `entities` — L3 knowledge graph nodes
- `relationships` — L3 knowledge graph edges
- `sessions` (memory) — session metadata (start/end times)
- `recaps` — generated session recap text + audio

---

## Architecture Decision: Where Does the UI Live?

### Option A: Embedded in the Gateway (Recommended)

The web UI is a static SPA served by the gateway process. The gateway's existing
HTTP server (`AdminAPI.Handler()`) is extended with new API endpoints and a file
server for static assets.

```
┌─────────────────────────────────────────────┐
│                  Gateway                     │
│                                              │
│  ┌─────────┐  ┌──────────┐  ┌────────────┐  │
│  │Admin API│  │ New API   │  │ Static SPA │  │
│  │(tenants)│  │ endpoints │  │  (React)   │  │
│  └────┬────┘  └─────┬────┘  └──────┬─────┘  │
│       │             │              │         │
│       └─────────────┼──────────────┘         │
│                     │                        │
│              ┌──────┴──────┐                 │
│              │ http.ServeMux│                 │
│              └─────────────┘                 │
└─────────────────────────────────────────────┘
```

**Pros:**
- Zero additional deployment complexity — same binary, same container
- SPA assets embedded via `embed.FS` (Go 1.16+) — no separate build step in prod
- Shares the same DB connections, stores, and auth middleware
- Natural CORS-free setup (same origin)
- Health checks, metrics, and TLS all inherited

**Cons:**
- Frontend dev requires a proxy or dev server during development
- Gateway binary grows ~2-5MB (compressed SPA assets)
- Tight coupling to gateway release cycle

### Option B: Separate Service

A standalone web server (Node.js or Go) that calls the Admin API over HTTP.

**Pros:** Independent deployment, separate scaling, choice of any tech stack.
**Cons:** Extra service to deploy/monitor, API key management, CORS, network hop,
duplicated auth logic. Overkill for current scale.

### Decision

**Option A** — embed in the gateway. Glyphoxa is a single-team project deployed
on K3s. Adding a separate service adds operational overhead without proportional
benefit. The SPA can always be extracted later if needed.

---

## Tech Stack

### Frontend: React + Vite + Tailwind CSS

| Choice       | Rationale                                                    |
|-------------|--------------------------------------------------------------|
| **React 19** | Most widely known, huge ecosystem, Luk can find contributors |
| **Vite**     | Fast HMR, modern bundling, small config surface              |
| **Tailwind** | Utility-first CSS, no custom design system needed            |
| **React Router** | Client-side routing for SPA                             |
| **TanStack Query** | Server-state management, caching, optimistic updates   |
| **shadcn/ui** | Copy-paste component library on top of Radix — accessible, customizable, no npm lock-in |
| **TypeScript** | Type safety for API contracts                             |

The built SPA is embedded into the Go binary via `//go:embed` at compile time.
During development, Vite's dev server proxies API requests to the gateway.

### Backend: Extend existing Go Admin API

New endpoints follow the existing pattern in `admin.go` — handler functions
registered on the shared `http.ServeMux`, guarded by `authMiddleware`.

### Why not HTMX/Go templates?

HTMX is tempting for simplicity but:
- NPC voice preview requires client-side audio playback (Web Audio API)
- Session monitoring benefits from WebSocket live updates
- Drag-and-drop NPC ordering, rich text for personalities — these need JS anyway
- React + TanStack Query gives better offline/optimistic UX for CRUD-heavy pages

---

## Authentication & Authorization

### Phase 1 (MVP): API Key Auth

Keep the existing `GLYPHOXA_ADMIN_API_KEY` mechanism. The SPA stores the key in
a session cookie (HttpOnly, Secure, SameSite=Strict) after the user enters it on
a login screen. All API requests include `Authorization: Bearer <key>`.

This is sufficient for single-operator deployments.

### Phase 2: User Auth with Roles

```
┌──────────────────────────────────────────────────┐
│                  Auth Flow                        │
│                                                   │
│  Discord OAuth2 ──→ JWT issued ──→ SPA stores JWT │
│                                                   │
│  JWT contains: user_id, tenant_id, role           │
│  Roles: super_admin, tenant_admin, dm, viewer     │
└──────────────────────────────────────────────────┘
```

**User model** (new `users` table):

```sql
CREATE TABLE IF NOT EXISTS users (
    id          TEXT PRIMARY KEY,           -- UUID
    tenant_id   TEXT NOT NULL REFERENCES tenants(id),
    discord_id  TEXT UNIQUE,               -- Discord user snowflake
    email       TEXT,
    name        TEXT NOT NULL,
    role        TEXT NOT NULL DEFAULT 'viewer',  -- super_admin, tenant_admin, dm, viewer
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_users_tenant ON users(tenant_id);
CREATE INDEX idx_users_discord ON users(discord_id);
```

**Role permissions:**

| Action                     | super_admin | tenant_admin | dm    | viewer |
|----------------------------|:-----------:|:------------:|:-----:|:------:|
| Manage tenants             | ✓           |              |       |        |
| Manage provider config     | ✓           |              |       |        |
| Manage users in own tenant | ✓           | ✓            |       |        |
| Manage campaigns           | ✓           | ✓            | ✓     |        |
| Manage NPCs               | ✓           | ✓            | ✓     |        |
| View sessions/transcripts  | ✓           | ✓            | ✓     | ✓      |
| View usage/billing         | ✓           | ✓            |       |        |
| Start/stop sessions (API)  | ✓           | ✓            | ✓     |        |

**OAuth2 flow:**
1. User clicks "Login with Discord"
2. Redirect to Discord OAuth2 (`identify` + `guilds` scopes)
3. Backend exchanges code for Discord user info
4. Match `discord_id` to existing user record (or auto-provision as `viewer`)
5. Issue JWT (HS256, 24h expiry) with `{user_id, tenant_id, role}`
6. SPA stores JWT, includes in all API requests

---

## API Extensions Required

### Campaign API

```
POST   /api/v1/campaigns                    Create campaign
GET    /api/v1/campaigns                    List campaigns (filterable by tenant)
GET    /api/v1/campaigns/{id}               Get campaign
PUT    /api/v1/campaigns/{id}               Update campaign
DELETE /api/v1/campaigns/{id}               Delete campaign (cascade NPCs?)
```

**New table:**

```sql
CREATE TABLE IF NOT EXISTS campaigns (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    system      TEXT NOT NULL DEFAULT '',       -- dnd5e, pf2e, etc.
    description TEXT NOT NULL DEFAULT '',
    settings    JSONB NOT NULL DEFAULT '{}',    -- game-specific config
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_campaigns_tenant ON campaigns(tenant_id);
```

**Impact:** The current `Tenant.CampaignID` (single string) becomes a foreign key
into this table. Tenants on `dedicated` tier can have multiple campaigns.

### NPC API

Expose the existing `npcstore.PostgresStore` via HTTP:

```
POST   /api/v1/campaigns/{campaign_id}/npcs           Create NPC
GET    /api/v1/campaigns/{campaign_id}/npcs           List NPCs for campaign
GET    /api/v1/npcs/{id}                               Get NPC
PUT    /api/v1/npcs/{id}                               Update NPC
DELETE /api/v1/npcs/{id}                               Delete NPC
POST   /api/v1/npcs/{id}/voice-preview                 Generate TTS preview audio
```

The NPC store already supports all CRUD operations — this is primarily wiring
HTTP handlers to existing `npcstore.Store` methods.

### User API (Phase 2)

```
POST   /api/v1/users                        Create user
GET    /api/v1/users                        List users (filtered by tenant)
GET    /api/v1/users/{id}                   Get user
PUT    /api/v1/users/{id}                   Update user (role changes)
DELETE /api/v1/users/{id}                   Delete user
GET    /api/v1/auth/discord                 Initiate Discord OAuth2
GET    /api/v1/auth/discord/callback        OAuth2 callback
POST   /api/v1/auth/refresh                 Refresh JWT
```

### Session API

Expose session orchestrator data:

```
GET    /api/v1/sessions                     List sessions (filterable: tenant, state, date range)
GET    /api/v1/sessions/{id}                Get session details
GET    /api/v1/sessions/{id}/transcript     Get session transcript (L1 entries)
GET    /api/v1/sessions/active              List active sessions across all tenants
DELETE /api/v1/sessions/{id}                Force-stop a session
```

**WebSocket endpoint for live monitoring (Phase 2):**
```
WS     /api/v1/sessions/{id}/live           Stream live transcript + audio stats
```

### Usage API

Expose usage store:

```
GET    /api/v1/usage                        List usage across tenants (current period)
GET    /api/v1/usage/{tenant_id}            Get usage for tenant (with period filter)
PUT    /api/v1/tenants/{id}/quota           Update tenant quota
```

### Provider API (Phase 3)

```
GET    /api/v1/providers                    List configured providers (redacted keys)
PUT    /api/v1/providers/{slot}             Update provider config (llm, stt, tts, etc.)
POST   /api/v1/providers/{slot}/test        Test provider connectivity
GET    /api/v1/providers/registry           List available provider implementations
```

**Note:** Provider configuration currently lives in the YAML config. Runtime
provider swapping requires extending the config system with a database-backed
override layer. This is the most complex API extension and is deferred to Phase 3.

---

## UI Pages & Wireframes

### 1. Dashboard (Home)

```
┌──────────────────────────────────────────────────────────┐
│  Glyphoxa Admin                          [User] [Logout] │
├──────────┬───────────────────────────────────────────────┤
│          │                                               │
│ Dashboard│   ┌─────────┐ ┌─────────┐ ┌─────────┐        │
│ Tenants  │   │Tenants: │ │Sessions:│ │  Hours  │        │
│ Campaigns│   │    3    │ │  2 live │ │ 47/100  │        │
│ NPCs     │   └─────────┘ └─────────┘ └─────────┘        │
│ Sessions │                                               │
│ Usage    │   Active Sessions                             │
│ Providers│   ┌──────────────────────────────────────┐    │
│ Users    │   │ luk / Rabenheim  │ active │ 0:42:15  │    │
│          │   │ demo / Tutorial  │ active │ 0:05:30  │    │
│          │   └──────────────────────────────────────┘    │
│          │                                               │
│          │   Recent Activity                             │
│          │   • Session ended: luk/Rabenheim (1h 23m)     │
│          │   • NPC created: "Erzähler" in Rabenheim      │
│          │   • Tenant "demo" created                     │
│          │                                               │
└──────────┴───────────────────────────────────────────────┘
```

**Key metrics:** Total tenants, active sessions, usage vs. quota bar,
system health (provider status from `/readyz`).

### 2. Tenant Management

**List view:** Table with ID, license tier, guilds, campaign, usage bar, actions.

**Detail/Edit view:**
- Tenant ID (read-only after creation)
- License tier dropdown (shared / dedicated)
- Bot token input (masked, with "Test Connection" button)
- Guild IDs (multi-select with Discord guild name resolution)
- DM Role ID
- Campaign assignment (dropdown from campaigns table)
- Monthly session hours quota (number input)
- Danger zone: delete tenant

### 3. Campaign Management

**List view:** Cards per campaign with name, game system, NPC count, last session date.

**Detail/Edit view:**
- Campaign name, game system (dropdown: D&D 5e, Pathfinder 2e, custom)
- Description (markdown editor)
- NPC list (inline, linked to NPC management)
- Entity import: upload YAML entity files or Foundry/Roll20 JSON
- Session history (linked to session monitoring)
- Knowledge graph explorer (Phase 3): visual graph of entities + relationships

### 4. NPC Management

**List view:** Cards with NPC name, avatar/icon, engine badge, voice provider badge.

**Detail/Edit view:**
```
┌──────────────────────────────────────────────────────────┐
│  NPC: Heinrich der Wächter                    [Save]     │
├──────────────────────────────────────────────────────────┤
│                                                          │
│  Name: [Heinrich der Wächter          ]                  │
│  Campaign: [Die Chroniken von Rabenheim ▼]               │
│                                                          │
│  ── Personality ──                                       │
│  ┌──────────────────────────────────────────────────┐    │
│  │ Ein strenger aber gerechter Stadtwächter...      │    │
│  │ (multi-line text area)                           │    │
│  └──────────────────────────────────────────────────┘    │
│                                                          │
│  ── Voice ──                                             │
│  Provider: [ElevenLabs ▼]                                │
│  Voice ID: [Helmut       ] [▶ Preview]                   │
│  Pitch:    [-2.0 ────●──── +2.0]                         │
│  Speed:    [0.5 ──●──────── 2.0]                         │
│                                                          │
│  ── Engine ──                                            │
│  Type: (●) Cascaded  ( ) S2S  ( ) Sentence Cascade       │
│  Budget Tier: ( ) Fast  (●) Standard  ( ) Deep           │
│                                                          │
│  ── Knowledge ──                                         │
│  Scope: [Rabenheim history] [guard duties] [+ Add]       │
│  Secrets: [The mayor's corruption] [+ Add]               │
│                                                          │
│  ── Behavior Rules ──                                    │
│  • Spricht immer Deutsch            [✕]                  │
│  • Misstraut Fremden zunächst       [✕]                  │
│  [+ Add Rule]                                            │
│                                                          │
│  ── Advanced ──                                          │
│  MCP Tools: [patrol_route] [check_papers] [+ Add]        │
│  GM Helper: [ ]   Address Only: [✓]                      │
│  Attributes: { "alignment": "lawful neutral" }           │
│                                                          │
└──────────────────────────────────────────────────────────┘
```

**Voice preview:** POST to `/api/v1/npcs/{id}/voice-preview` with a sample
sentence → returns audio blob → play via Web Audio API. This calls the TTS
provider with the NPC's voice config.

### 5. Session Monitoring

**Active sessions list:**
- Real-time updating (polling every 5s, or WebSocket in Phase 2)
- Per session: tenant, campaign, guild, channel, state, duration, worker pod
- Actions: force-stop

**Session detail view:**
- Session metadata (ID, times, worker, state)
- Live transcript (scrolling log of speaker → text entries)
- Audio stats: VAD activity, STT latency, TTS queue depth (from worker metrics)
- NPC activity: which NPCs responded, response times

**Session history:**
- Filterable table: date range, tenant, campaign, state
- Per session: duration, transcript entry count, error (if failed)
- Click through to transcript viewer

**Transcript viewer:**
- Chronological display with speaker avatars/names
- Color-coded: player utterances vs NPC responses
- Raw vs corrected text toggle
- Search within transcript (full-text search via existing GIN index)
- Export as text/JSON

### 6. Usage & Billing

**Overview:**
- Per-tenant usage cards: session hours (bar chart vs quota), LLM tokens,
  STT seconds, TTS characters
- Time period selector (current month, previous months)

**Detail view per tenant:**
- Line chart: daily session hours over the billing period
- Breakdown table: per-session usage (duration, tokens, STT time, TTS chars)
- Quota management: edit `monthly_session_hours`
- Export as CSV

### 7. Provider Configuration (Phase 3)

**Provider slots grid:**
```
┌──────────┐ ┌──────────┐ ┌──────────┐
│   LLM    │ │   STT    │ │   TTS    │
│ OpenAI   │ │ Deepgram │ │ Eleven   │
│  gpt-4o  │ │  nova-2  │ │ Labs v2  │
│    ✓     │ │    ✓     │ │    ✓     │
└──────────┘ └──────────┘ └──────────┘
┌──────────┐ ┌──────────┐ ┌──────────┐
│   VAD    │ │   S2S    │ │Embeddings│
│ Silero   │ │  (none)  │ │ Gemini   │
│  v5      │ │          │ │ emb-001  │
│    ✓     │ │    —     │ │    ✓     │
└──────────┘ └──────────┘ └──────────┘
```

Each card: provider name, model, status indicator, latency P50/P99.
Click to edit: API key (masked), base URL, model, provider-specific options.
"Test Connection" button per provider.

### 8. User Management (Phase 2)

**User list:** Table with name, Discord username, role, tenant, last active.

**Invite flow:** Generate invite link or add by Discord ID → assign role.

---

## Deployment Strategy

### Development

```
glyphoxa/
├── web/                          # SPA source (gitignored build output)
│   ├── package.json
│   ├── vite.config.ts
│   ├── src/
│   │   ├── main.tsx
│   │   ├── api/                  # Generated API client (from OpenAPI spec)
│   │   ├── components/           # shadcn/ui components
│   │   ├── pages/                # Route-level components
│   │   ├── hooks/                # TanStack Query hooks
│   │   └── lib/                  # Utilities
│   └── dist/                     # Build output (embedded into Go)
├── internal/gateway/
│   ├── admin.go                  # Extended with new routes
│   ├── admin_campaigns.go        # Campaign handlers
│   ├── admin_npcs.go             # NPC handlers
│   ├── admin_sessions.go         # Session query handlers
│   ├── admin_usage.go            # Usage handlers
│   ├── admin_users.go            # User handlers (Phase 2)
│   ├── admin_providers.go        # Provider handlers (Phase 3)
│   └── webui.go                  # embed.FS + SPA fallback handler
```

**Embedding:**
```go
// internal/gateway/webui.go
package gateway

import "embed"

//go:embed all:web/dist
var webAssets embed.FS

func (a *AdminAPI) registerWebUI() {
    // Serve static files, fallback to index.html for SPA routing
    a.mux.Handle("GET /", spaHandler(webAssets))
}
```

**Dev workflow:**
1. `cd web && npm run dev` — Vite dev server on :5173
2. `vite.config.ts` proxies `/api/*` to gateway on :8081
3. Hot module replacement for instant frontend iteration

### Production (K3s)

No changes to the existing deployment — the SPA is baked into the gateway binary.

```dockerfile
# Multi-stage Dockerfile addition
FROM node:22-alpine AS frontend
WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26 AS backend
# ... existing build steps ...
COPY --from=frontend /app/web/dist ./web/dist
RUN go build -o /glyphoxa ./cmd/glyphoxa
```

### Makefile additions

```makefile
web-install:
	cd web && npm ci

web-dev:
	cd web && npm run dev

web-build:
	cd web && npm run build

web-lint:
	cd web && npm run lint

build: web-build  # Add web-build as dependency to existing build target
```

---

## Phase Breakdown

### Phase 1: MVP (Foundation + Tenant/Campaign/NPC CRUD)

**Scope:** Get a working UI for the most common operations.

**Backend:**
- [ ] Create `campaigns` table + `CampaignStore` (PostgreSQL)
- [ ] Add campaign CRUD endpoints (`/api/v1/campaigns`)
- [ ] Expose NPC store via HTTP (`/api/v1/campaigns/{id}/npcs`, `/api/v1/npcs/{id}`)
- [ ] Add session list/detail endpoints (`/api/v1/sessions`)
- [ ] Add usage query endpoint (`/api/v1/usage/{tenant_id}`)
- [ ] Add NPC voice preview endpoint (`POST /api/v1/npcs/{id}/voice-preview`)
- [ ] SPA embedding infrastructure (`embed.FS`, SPA fallback handler)
- [ ] OpenAPI spec generation (for typed API client)

**Frontend:**
- [ ] Vite + React + TypeScript + Tailwind + shadcn/ui scaffolding
- [ ] API key login page (stores key in HttpOnly cookie)
- [ ] Dashboard with metric cards and active sessions list
- [ ] Tenant list + create/edit/delete
- [ ] Campaign list + create/edit/delete
- [ ] NPC list + create/edit with voice preview
- [ ] Session list with transcript viewer
- [ ] Basic usage display per tenant

**Auth:** Existing API key mechanism. Single admin role.

**Estimated scope:** ~15-20 new Go files, ~30-40 React components, 1 new DB migration.

### Phase 2: User Auth + Live Monitoring

**Scope:** Multi-user access, Discord OAuth2, live session monitoring.

**Backend:**
- [ ] `users` table + user CRUD endpoints
- [ ] Discord OAuth2 flow (authorize, callback, JWT issuance)
- [ ] JWT auth middleware (alongside existing API key auth)
- [ ] Role-based access control middleware
- [ ] WebSocket endpoint for live session transcript streaming
- [ ] Session audio stats endpoint (from worker metrics)

**Frontend:**
- [ ] Discord OAuth2 login flow
- [ ] User management page (invite, role assignment)
- [ ] Role-based navigation (hide pages user can't access)
- [ ] Live session monitoring with WebSocket transcript stream
- [ ] Session audio stats visualization (latency charts)

**Estimated scope:** ~10 new Go files, ~15-20 React components, 1 new DB migration.

### Phase 3: Provider Config + Advanced Features

**Scope:** Runtime provider management, knowledge graph explorer, advanced billing.

**Backend:**
- [ ] Database-backed provider config override layer
- [ ] Provider CRUD endpoints with connectivity testing
- [ ] Provider health/latency metrics endpoint
- [ ] Knowledge graph query API (entities + relationships)
- [ ] Usage export endpoint (CSV)
- [ ] Audit log table + endpoints

**Frontend:**
- [ ] Provider configuration page with test buttons
- [ ] Knowledge graph visualization (force-directed graph, e.g., D3 or react-force-graph)
- [ ] Usage export/download
- [ ] Audit log viewer
- [ ] Campaign entity/relationship browser

**Estimated scope:** ~8-12 new Go files, ~15-20 React components, 2 new DB migrations.

---

## Key Design Decisions & Trade-offs

### 1. SPA vs Server-Rendered

**Decision:** SPA (React).
**Why:** Voice preview (Web Audio), live session monitoring (WebSocket), rich
NPC editing (tag inputs, sliders, drag-and-drop) all require significant
client-side JS. An SPA also enables offline-capable editing and optimistic
updates via TanStack Query.

### 2. Embedded vs External Service

**Decision:** Embedded in gateway binary.
**Why:** Single binary deployment, no CORS, shared auth, minimal ops overhead.
The `embed.FS` approach means zero runtime dependencies for serving the UI.

### 3. API Key First, OAuth2 Later

**Decision:** Ship Phase 1 with API key auth, add Discord OAuth2 in Phase 2.
**Why:** API key auth already works and is secure for single-operator use.
OAuth2 adds significant complexity (token refresh, session management, Discord
API integration) that shouldn't block the MVP.

### 4. OpenAPI Spec as Contract

Generate an OpenAPI 3.1 spec from Go struct tags + route definitions. Use
`oapi-codegen` or similar to generate a TypeScript API client. This keeps
frontend and backend type-safe without manual sync.

### 5. Campaign as First-Class Entity

Currently, `campaign_id` is just a string field on tenants and NPCs. Promoting
campaigns to a proper table with metadata enables:
- Multiple campaigns per tenant (dedicated tier)
- Campaign-level settings (game system, description)
- Clean foreign key relationships
- Campaign-scoped NPC listing in the UI

### 6. Tenant Isolation in the UI

The UI must respect tenant boundaries. In Phase 1 (API key = super admin), all
data is visible. In Phase 2 (user auth), the backend filters all queries by the
user's `tenant_id` — the frontend never sees cross-tenant data.

---

## Security Considerations

- **Bot tokens:** Never returned in API responses (existing behavior). UI shows
  "••••••••" with a "Change" button.
- **API keys:** Stored in Vault Transit (existing infrastructure).
- **CSRF:** SameSite=Strict cookies + custom header requirement.
- **XSS:** React's default escaping + CSP headers. No `dangerouslySetInnerHTML`.
- **Rate limiting:** Apply existing NPM rate limiting or add Go-side rate limiter.
- **Input validation:** Server-side validation on all endpoints (existing pattern
  with `Validate()` methods). Client-side validation is UX only.
- **Audit logging (Phase 3):** All write operations logged with user, timestamp,
  and change diff.

---

## Open Questions

1. **Voice preview cost:** TTS API calls for previews cost money. Rate-limit
   to N previews per minute? Cache common samples?
2. **Multi-campaign on shared tier?** Currently shared tier = 1 session at a time.
   Should shared tenants be limited to 1 campaign, or can they have multiple
   (but only run 1 session)?
3. **NPC image/avatar upload?** Nice for the UI but adds blob storage complexity.
   Defer to Phase 3? Or use Discord avatar URLs?
4. **Localization?** Glyphoxa is used in German (Rabenheim campaign). Should the
   UI support i18n from the start? Recommend English-only MVP with i18n hooks
   (react-i18next) ready for Phase 2.
5. **Mobile responsive?** DMs might manage NPCs from a phone during sessions.
   Tailwind makes responsive easy — design mobile-first from Phase 1.
