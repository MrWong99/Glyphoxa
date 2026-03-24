---
title: "Glyphoxa Web Management Service — Architecture & Tech Stack"
type: architecture
status: draft
date: 2026-03-24
supersedes: docs/plans/2026-03-23-admin-web-ui-plan.md (Option A — embedded)
---

# Glyphoxa Web Management Service — Architecture Plan

## 1. Executive Summary

This document defines the architecture for the **Glyphoxa Web Management Service** — a
separate, independently deployable service that provides self-service management for
Dungeon Masters, tenant administration, NPC configuration, billing, and observability.

**Key constraints:**

- Separate service (NOT embedded in the gateway) — firm requirement
- Must scale to >1,000 concurrent users
- Self-service SaaS model with tiered pricing
- DMs manage their own campaigns, NPCs, and sessions without operator intervention

This supersedes the "Option A" (embedded in gateway) approach from the
[2026-03-23 admin UI plan](../2026-03-23-admin-web-ui-plan.md). The gateway remains
a lean voice-pipeline orchestrator; user-facing management moves to its own service.

---

## 2. Service Topology

```
                                    ┌─────────────────────────┐
                                    │     CDN / Edge Cache    │
                                    │  (static SPA assets,    │
                                    │   voice sample cache)   │
                                    └────────────┬────────────┘
                                                 │
                                    ┌────────────▼────────────┐
                               ┌───►│   Reverse Proxy (NPM /  │◄────┐
                               │    │   Traefik / Caddy)       │     │
                               │    └──┬──────────┬──────────┬─┘     │
                               │       │          │          │       │
                    Browser    │  /app/*│   /api/* │  /gw/*   │       │
                    (SPA)──────┘       │          │          │       │
                                       ▼          ▼          ▼       │
                             ┌─────────────┐ ┌─────────┐ ┌───────┐  │
                             │  SPA Static  │ │  Web    │ │Gateway│  │
                             │  File Server │ │ Mgmt API│ │ Admin │  │
                             │  (or CDN)    │ │  (Go)   │ │  API  │  │
                             └─────────────┘ └────┬────┘ └───┬───┘  │
                                                  │          │       │
                        ┌─────────────────────────┼──────────┘       │
                        │                         │                  │
              ┌─────────▼──────────┐   ┌──────────▼──────────┐       │
              │   PostgreSQL       │   │   Gateway (gRPC)    │       │
              │  (shared DB,       │   │  - Session control   │       │
              │   tenant schemas)  │   │  - NPC control       │       │
              └────────┬───────────┘   │  - Audio bridge      │       │
                       │               └──────────────────────┘       │
              ┌────────▼───────────┐                                  │
              │   Vault (Transit)  │   External Services:             │
              │  - API key encrypt │   - Stripe (billing)             │
              │  - Bot token store │   - Discord OAuth2               │
              │  - Secret mgmt    │   - Google OAuth2                 │
              └────────────────────┘   - ElevenLabs (voice samples)   │
                                       - S3/MinIO (file storage)      │
                                       - OTel Collector ──────────────┘
```

### Communication paths

| From | To | Protocol | Purpose |
|------|----|----------|---------|
| Browser | Web Mgmt API | HTTPS (REST + WebSocket) | All user interactions |
| Web Mgmt API | PostgreSQL | TCP (pgx pool) | Tenant, user, campaign, NPC, billing data |
| Web Mgmt API | Gateway Admin API | HTTP (internal) | Session control, bot management (proxy) |
| Web Mgmt API | Gateway gRPC | gRPC (internal) | Live session status, NPC mute/unmute/speak |
| Web Mgmt API | Vault | HTTP | Encrypt/decrypt API keys, bot tokens |
| Web Mgmt API | Stripe | HTTPS | Subscription lifecycle, webhooks |
| Web Mgmt API | Discord/Google | HTTPS | OAuth2 flows |
| Web Mgmt API | S3/MinIO | HTTPS | Voice sample upload/download |
| Web Mgmt API | OTel Collector | gRPC (OTLP) | Traces, metrics, logs |

### Why separate from the gateway?

| Concern | Embedded (Option A) | Separate (Option B — chosen) |
|---------|--------------------|-----------------------------|
| Scaling | Scales with gateway (voice pipeline) | Scales independently based on web traffic |
| Release cycle | Frontend changes require gateway redeploy (voice disruption) | Deploy frontend/backend independently |
| Security surface | Auth, OAuth, Stripe in the voice-critical path | Isolated — gateway stays lean and locked down |
| Failure isolation | UI bug or spike can impact voice sessions | Web service crash doesn't affect live sessions |
| Multi-gateway | One UI per gateway instance | Single management plane for N gateways |
| Complexity | Simpler for single-tenant | Required for multi-tenant SaaS at >1000 users |

---

## 3. Tech Stack

### 3.1 Backend: Go

| Choice | Rationale |
|--------|-----------|
| **Go 1.26+** | Same language as gateway — shared domain types, DB migration patterns, Vault client code. One team, one language, one toolchain. |
| **net/http (stdlib)** | Standard library router (`http.ServeMux` with method patterns, Go 1.22+) — same as gateway. No framework dependency. |
| **pgx v5** | Same PostgreSQL driver as gateway. Connection pooling via `pgxpool`. |
| **google.golang.org/grpc** | For calling gateway's gRPC services (session status, NPC control). |
| **golang-jwt/jwt/v5** | JWT issuance and validation (access + refresh tokens). |
| **markbates/goth** or **coreos/go-oidc** | OAuth2 provider abstraction (Discord, Google, GitHub). |
| **stripe/stripe-go** | Stripe subscription management + webhook verification. |

**Why Go over Node/Python?**

- The entire team (Luk) writes Go. Shared types with gateway (tenant model, NPC definition, config structs) can live in an importable `pkg/` package — no cross-language serialization.
- Go's concurrency model handles WebSocket fan-out and long-polling efficiently.
- Single static binary — same deployment model as gateway.
- If we ever need to merge web management back into the gateway (unlikely), the code is directly compatible.

**Why not a Go framework (Gin, Echo, Fiber)?**

- stdlib `net/http` + `http.ServeMux` is sufficient for REST APIs (Go 1.22+ has method routing).
- The gateway already uses this pattern — consistency matters more than framework features.
- Middleware chains are trivial with `func(http.Handler) http.Handler`.
- No dependency churn from framework major versions.

### 3.2 Frontend: React + Vite + Tailwind

| Choice | Rationale |
|--------|-----------|
| **React 19** | Largest ecosystem, easiest to find contributors, Luk can find help. |
| **Vite 6** | Fast HMR, modern ESM bundling, minimal config. |
| **TypeScript 5.x** | Type safety for API contracts. Non-negotiable for >30 components. |
| **Tailwind CSS 4** | Utility-first, no custom design system needed. Mobile-first responsive by default. |
| **shadcn/ui** | Copy-paste component library (Radix primitives). Accessible, customizable, no npm lock-in. |
| **TanStack Query v5** | Server-state management with caching, optimistic updates, background refetch. |
| **TanStack Router** | Type-safe routing with search params. Better than React Router for data-heavy apps. |
| **Recharts** | Charts for usage/billing dashboards. Lightweight, React-native. |
| **react-hook-form + zod** | Form validation — paired with zod schemas generated from OpenAPI spec. |

**Why SPA over SSR (Next.js, Remix)?**

- Management dashboards are inherently interactive — no SEO requirement.
- Voice preview requires Web Audio API (client-only).
- WebSocket session monitoring is client-driven.
- SPA deploys as static files to CDN — zero Node.js servers in production.
- Simpler deployment: static files + Go API binary.

**Why not HTMX?**

- Same reasoning as the original plan: voice preview, drag-and-drop NPC ordering, real-time session monitoring, and rich form editors all require significant client-side JS. HTMX would need so many `hx-ext` scripts that it becomes React-with-extra-steps.

### 3.3 API Contract: OpenAPI 3.1

- Go backend generates OpenAPI spec from struct tags + route definitions (via `swaggo/swag` or `oapi-codegen` annotations).
- TypeScript client auto-generated from spec (`openapi-typescript-codegen` or `@hey-api/openapi-ts`).
- Zod validation schemas generated from spec for form validation.
- Single source of truth — backend structs drive everything.

---

## 4. Multi-Tenancy Model

### Decision: Shared database, `tenant_id` column isolation

```
┌──────────────────────────────────────────────────────┐
│                   PostgreSQL                          │
│                                                       │
│  public schema (shared tables):                       │
│  ┌──────────┐ ┌──────────┐ ┌────────────────┐        │
│  │  users   │ │ tenants  │ │ subscriptions  │        │
│  └──────────┘ └──────────┘ └────────────────┘        │
│  ┌──────────┐ ┌──────────┐ ┌────────────────┐        │
│  │campaigns │ │ sessions │ │ usage_records  │        │
│  └──────────┘ └──────────┘ └────────────────┘        │
│  ┌──────────┐ ┌──────────┐                           │
│  │  npcs    │ │ invoices │                           │
│  └──────────┘ └──────────┘                           │
│                                                       │
│  Per-tenant schemas (existing — used by workers):     │
│  ┌─────────────────┐  ┌─────────────────┐            │
│  │ tenant_luk.*    │  │ tenant_demo.*   │            │
│  │ session_entries │  │ session_entries │            │
│  │ chunks          │  │ chunks          │            │
│  │ entities        │  │ entities        │            │
│  │ relationships   │  │ relationships   │            │
│  │ recaps          │  │ recaps          │            │
│  └─────────────────┘  └─────────────────┘            │
└──────────────────────────────────────────────────────┘
```

**Why shared DB + `tenant_id` columns (not separate DBs per tenant)?**

| Factor | Shared DB | Separate DBs |
|--------|-----------|-------------|
| Operational cost | 1 DB to manage, backup, monitor | N DBs — linear ops growth |
| Cross-tenant queries | Simple JOINs (admin dashboards, billing) | Requires federation or app-level aggregation |
| Connection count | 1 pool shared across tenants | N pools — connection explosion at >100 tenants |
| Schema migrations | Run once | Run N times (migration orchestrator needed) |
| Tenant isolation | Row-level (RLS or app-enforced WHERE) | Full schema isolation |
| Scale limit | ~10,000 tenants before RLS overhead matters | Unlimited (each DB independent) |

**Isolation mechanism:**

- All queries include `WHERE tenant_id = $1` — enforced at the repository layer.
- PostgreSQL Row-Level Security (RLS) as defense-in-depth (Phase 2).
- The existing per-tenant schemas (`tenant_<id>.*`) for session entries, memory chunks, and knowledge graph data remain unchanged — workers already use these.
- The web management service reads per-tenant schemas for transcript viewing and knowledge graph browsing.

**Compatibility with existing gateway DB:**

The web management service shares the same PostgreSQL instance. Tables owned by the gateway (`tenants`, `sessions`, `usage_records`) are accessed read-only by the web service for display. Write operations on these entities go through the gateway's Admin API (or a new internal API) to maintain the gateway as the source of truth for session-critical state.

New tables (`users`, `campaigns`, `subscriptions`, `invoices`, `voice_samples`, `audit_log`) are owned by the web management service.

---

## 5. Service Boundaries

### What lives where

| Concern | Web Management Service | Gateway | Shared (DB) |
|---------|----------------------|---------|-------------|
| User auth (OAuth2, JWT) | ✓ owns | — | `users` table |
| Tenant CRUD | ✓ owns (replaces gateway admin API for external use) | Internal API only | `tenants` table |
| Campaign CRUD | ✓ owns | Reads campaign context | `campaigns` table |
| NPC CRUD | ✓ owns (HTTP) | Reads NPC defs at session start | `npc_definitions` table |
| Session start/stop | Proxies to gateway | ✓ owns (orchestrator + dispatcher) | `sessions` table |
| Session monitoring | Reads DB + gateway gRPC | ✓ owns (live state) | `sessions` table |
| Usage tracking | Reads + displays | ✓ owns (writes during sessions) | `usage_records` table |
| Billing/subscriptions | ✓ owns | Checks quota via usage store | `subscriptions` table |
| Voice sample upload | ✓ owns | — | S3/MinIO |
| Transcript viewing | ✓ reads | Worker writes | `tenant_<id>.session_entries` |
| Knowledge graph browse | ✓ reads | Worker writes | `tenant_<id>.entities/relationships` |
| Provider config | ✓ manages overrides | Reads at session start | `provider_configs` table |
| Bot token management | ✓ manages (via Vault) | Uses at runtime | Vault Transit |
| Observability dashboard | ✓ owns (queries Grafana/OTel) | Emits telemetry | OTel Collector |
| Support tickets | ✓ owns (integrates third-party) | — | External system |

### Internal communication contract

The web management service calls the gateway for operations the gateway **must** own (voice session lifecycle):

```go
// Web service → Gateway (HTTP, internal network only)
POST   /internal/v1/sessions/{tenant_id}/start   // Start voice session
POST   /internal/v1/sessions/{session_id}/stop    // Stop voice session
GET    /internal/v1/sessions/active               // List active sessions

// Web service → Gateway (gRPC, internal network only)
rpc GetStatus(GetStatusRequest) returns (GetStatusResponse)
rpc ListNPCs(ListNPCsRequest) returns (ListNPCsResponse)
rpc MuteNPC / UnmuteNPC / SpeakNPC               // NPC control during session
```

The gateway's existing external Admin API (`/api/v1/tenants`) can be deprecated or restricted to internal-only once the web management service takes over tenant CRUD. During migration, both coexist.

---

## 6. Authentication & Authorization

### Auth architecture

```
┌──────────┐   OAuth2    ┌──────────────┐   JWT      ┌──────────────┐
│ Discord  │◄───────────►│              │──────────►│              │
│ Google   │  code grant  │  Web Mgmt    │  access +  │   Browser    │
│ GitHub   │              │  Service     │  refresh   │   (SPA)      │
└──────────┘              │              │            └──────┬───────┘
                          │  /auth/*     │                   │
                          └──────┬───────┘                   │
                                 │                           │
                          ┌──────▼───────┐            ┌──────▼───────┐
                          │  users table │            │ All /api/*   │
                          │  + sessions  │            │ requests     │
                          └──────────────┘            │ carry JWT    │
                                                      └──────────────┘
```

### Token strategy

| Token | Storage | Lifetime | Purpose |
|-------|---------|----------|---------|
| Access token (JWT) | In-memory (SPA state) | 15 minutes | API authorization |
| Refresh token | HttpOnly, Secure, SameSite=Strict cookie | 7 days | Silent token refresh |
| CSRF token | Custom header (`X-CSRF-Token`) | Per-session | Prevent CSRF on cookie-based refresh |

**Why short-lived access tokens + refresh cookie?**

- Access token in memory (not localStorage) — immune to XSS-based token theft.
- Refresh token in HttpOnly cookie — immune to JS access.
- 15-minute access token limits damage window if somehow leaked.
- Refresh endpoint rotates the refresh token (rotation + reuse detection).

### User model

```sql
CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID REFERENCES tenants(id) ON DELETE CASCADE,
    email           TEXT UNIQUE,
    name            TEXT NOT NULL,
    avatar_url      TEXT,
    role            TEXT NOT NULL DEFAULT 'dm',
    -- OAuth provider links
    discord_id      TEXT UNIQUE,
    google_id       TEXT UNIQUE,
    github_id       TEXT UNIQUE,
    -- Billing
    stripe_customer_id TEXT UNIQUE,
    -- Lifecycle
    email_verified  BOOLEAN NOT NULL DEFAULT false,
    last_login_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_users_tenant ON users(tenant_id);
```

### Roles

| Role | Description | Scope |
|------|-------------|-------|
| `super_admin` | Platform operator (Luk) | Global — all tenants |
| `tenant_owner` | Tenant creator / billing contact | Own tenant — full control |
| `dm` | Dungeon Master | Own tenant — campaigns, NPCs, sessions |
| `viewer` | Read-only (invited players) | Own tenant — transcripts, session history |

**Permission matrix:**

| Action | super_admin | tenant_owner | dm | viewer |
|--------|:-----------:|:------------:|:--:|:------:|
| Manage all tenants | ✓ | | | |
| Platform observability | ✓ | | | |
| Manage own tenant settings | ✓ | ✓ | | |
| Manage billing/subscription | ✓ | ✓ | | |
| Invite/remove users | ✓ | ✓ | | |
| Manage campaigns | ✓ | ✓ | ✓ | |
| Manage NPCs | ✓ | ✓ | ✓ | |
| Start/stop sessions | ✓ | ✓ | ✓ | |
| Upload voice samples | ✓ | ✓ | ✓ | |
| View transcripts | ✓ | ✓ | ✓ | ✓ |
| View usage | ✓ | ✓ | ✓ | ✓ |
| View session history | ✓ | ✓ | ✓ | ✓ |

### Self-service onboarding flow

```
1. DM visits glyphoxa.app → "Sign up with Discord"
2. Discord OAuth2 → identify + guilds scopes
3. Web service creates:
   a. User record (role: tenant_owner)
   b. Tenant record (license_tier: shared, empty config)
   c. Redirect to onboarding wizard
4. Onboarding wizard:
   a. "Name your first campaign" → creates campaign
   b. "Add your Discord bot token" → encrypted via Vault
   c. "Select your guild" → guild picker from Discord API
   d. "Choose a plan" → Stripe checkout
   e. "Create your first NPC" → NPC editor
5. DM is live — can start sessions from Discord
```

---

## 7. API Design

### Route structure

```
/auth/discord              GET    Initiate Discord OAuth2
/auth/discord/callback     GET    Discord OAuth2 callback
/auth/google               GET    Initiate Google OAuth2
/auth/google/callback      GET    Google OAuth2 callback
/auth/refresh              POST   Refresh access token
/auth/logout               POST   Revoke refresh token

/api/v1/me                 GET    Current user profile
/api/v1/me                 PUT    Update profile

/api/v1/tenants            POST   Create tenant (self-service)
/api/v1/tenants/{id}       GET    Get tenant
/api/v1/tenants/{id}       PUT    Update tenant settings
/api/v1/tenants/{id}       DELETE Delete tenant

/api/v1/tenants/{id}/users         GET    List users in tenant
/api/v1/tenants/{id}/users         POST   Invite user
/api/v1/tenants/{id}/users/{uid}   PUT    Update user role
/api/v1/tenants/{id}/users/{uid}   DELETE Remove user

/api/v1/campaigns                  POST   Create campaign
/api/v1/campaigns                  GET    List campaigns (tenant-scoped)
/api/v1/campaigns/{id}             GET    Get campaign
/api/v1/campaigns/{id}             PUT    Update campaign
/api/v1/campaigns/{id}             DELETE Delete campaign

/api/v1/campaigns/{id}/npcs        POST   Create NPC
/api/v1/campaigns/{id}/npcs        GET    List NPCs for campaign
/api/v1/npcs/{id}                  GET    Get NPC
/api/v1/npcs/{id}                  PUT    Update NPC
/api/v1/npcs/{id}                  DELETE Delete NPC
/api/v1/npcs/{id}/voice-preview    POST   Generate TTS preview audio

/api/v1/sessions                   GET    List sessions (filterable)
/api/v1/sessions/active            GET    Active sessions
/api/v1/sessions/{id}              GET    Session details
/api/v1/sessions/{id}/transcript   GET    Session transcript
/api/v1/sessions/{id}/stop         POST   Force-stop session
/api/v1/sessions/{id}/live         WS     Live transcript stream

/api/v1/voice-samples              POST   Upload voice sample
/api/v1/voice-samples              GET    List voice samples
/api/v1/voice-samples/{id}         GET    Get voice sample
/api/v1/voice-samples/{id}         DELETE Delete voice sample

/api/v1/usage                      GET    Usage summary (tenant-scoped)
/api/v1/usage/export               GET    Export usage as CSV

/api/v1/billing/subscription       GET    Current subscription
/api/v1/billing/subscription       POST   Create/change subscription
/api/v1/billing/portal             POST   Create Stripe billing portal session
/api/v1/billing/webhook            POST   Stripe webhook receiver

/api/v1/providers                  GET    List provider configs (redacted keys)
/api/v1/providers/{slot}           PUT    Update provider config
/api/v1/providers/{slot}/test      POST   Test provider connectivity

/api/v1/support/tickets            POST   Create support ticket
/api/v1/support/tickets            GET    List tickets
/api/v1/support/tickets/{id}       GET    Get ticket

# Super admin only
/api/v1/admin/tenants              GET    List all tenants
/api/v1/admin/observability        GET    System health + metrics
/api/v1/admin/users                GET    List all users
```

### API gateway / reverse proxy

```
┌──────────────────────────────────────────────────┐
│             Reverse Proxy (Traefik / Caddy)       │
│                                                    │
│  app.glyphoxa.app/*          → SPA static files   │
│  app.glyphoxa.app/api/*      → Web Mgmt Service   │
│  app.glyphoxa.app/auth/*     → Web Mgmt Service   │
│                                                    │
│  gw.glyphoxa.app/internal/*  → Gateway Admin API   │
│  (internal network only — not internet-facing)     │
│                                                    │
│  TLS termination, rate limiting, request logging   │
└──────────────────────────────────────────────────┘
```

For local/K3s deployment, Nginx Proxy Manager (already in use) handles this. For production SaaS, Traefik (K8s-native) or Caddy (auto-TLS) are preferred.

**Rate limiting:**

| Endpoint group | Limit | Window |
|---------------|-------|--------|
| `/auth/*` | 10 req | 1 min (per IP) |
| `/api/v1/npcs/*/voice-preview` | 5 req | 1 min (per user) |
| `/api/v1/billing/webhook` | 100 req | 1 min (Stripe IPs only) |
| All other `/api/*` | 60 req | 1 min (per user) |

---

## 8. Scaling Strategy

### Horizontal scaling

```
                    ┌─────────────────────┐
                    │   Load Balancer      │
                    └──┬──────┬──────┬────┘
                       │      │      │
                  ┌────▼─┐ ┌─▼────┐ ┌▼────┐
                  │Web #1│ │Web #2│ │Web #3│   Stateless Go instances
                  └──┬───┘ └──┬───┘ └──┬──┘
                     │        │        │
                  ┌──▼────────▼────────▼──┐
                  │   PostgreSQL (pgx pool) │   Connection pooling
                  │   + PgBouncer (optional)│
                  └────────────────────────┘
```

**Why this works:**

- Web management service is **stateless** — all state lives in PostgreSQL + Vault + S3.
- JWT validation is local (no session store needed).
- WebSocket connections are per-instance (no cross-instance fan-out needed — each browser connects to one instance, and sessions are scoped).
- Go's goroutine model handles thousands of concurrent connections per instance.

### Scaling targets

| Component | 1-100 users | 100-1,000 users | 1,000+ users |
|-----------|-------------|-----------------|--------------|
| Web service | 1 replica | 2-3 replicas | HPA (CPU-based) |
| PostgreSQL | Single instance | Single + read replica | Primary + read replicas |
| Static assets | Same origin | CDN (Cloudflare/BunnyCDN) | CDN |
| File storage (voice samples) | Local disk / MinIO | MinIO | S3 / R2 |
| Redis (optional) | Not needed | Rate limiting + sessions | Rate limiting + caching |

### Database connection pooling

- **pgxpool** in Go — per-instance pool (default: 10 idle, 25 max per instance).
- At >500 users: add **PgBouncer** in transaction mode between Go instances and PostgreSQL to multiplex connections.
- Read-heavy queries (transcript viewing, usage dashboards) can target a read replica (configurable DSN).

### CDN for static assets

The SPA build output (`dist/`) is deployed to a CDN or object storage with aggressive caching:

- `index.html` — `Cache-Control: no-cache` (always fresh, checks ETag)
- `assets/*.js` / `assets/*.css` — `Cache-Control: public, max-age=31536000, immutable` (content-hashed filenames)
- Voice sample playback URLs — signed, time-limited S3 presigned URLs

---

## 9. Secret Management

### Vault integration

```
┌─────────────────────────────────────────────────────┐
│                    Vault                              │
│                                                       │
│  Transit engine (glyphoxa-bot-tokens):               │
│  ├── Bot tokens (existing — shared with gateway)     │
│  └── Tenant API keys (BYO provider keys)             │
│                                                       │
│  KV v2 engine (glyphoxa-web/):                       │
│  ├── stripe-secret-key                               │
│  ├── discord-oauth-client-secret                     │
│  ├── google-oauth-client-secret                      │
│  ├── jwt-signing-key                                 │
│  └── s3-access-credentials                           │
│                                                       │
│  PKI engine (existing):                               │
│  └── mTLS certs for web-service ↔ gateway            │
└─────────────────────────────────────────────────────┘
```

### Secret flow for "bring your own API keys"

```
1. DM enters ElevenLabs API key in the web UI
2. Web service calls Vault Transit: encrypt(plaintext=key, key=glyphoxa-bot-tokens)
3. Encrypted ciphertext stored in `provider_configs` table
4. At session start: gateway reads provider_configs, calls Vault Transit: decrypt()
5. Decrypted key passed to worker via gRPC StartSessionRequest (TLS-encrypted in transit)
6. Worker uses key for TTS calls, never persists it
```

**Key principles:**

- Plaintext secrets **never** touch the database.
- The web service can encrypt but only the gateway needs to decrypt (separation of concern possible via Vault policies, but shared for simplicity now).
- Vault Transit key rotation is transparent — old ciphertexts remain decryptable.
- If Vault is unreachable, the web service rejects secret-write operations (no graceful degradation for writes — this is intentional for security).

---

## 10. Billing & Pricing Integration

### Pricing tiers (from [pricing assessment](./pricing-models.md))

| Tier | Price | Sessions/mo | NPCs | Voices | Model tier | Target |
|------|-------|-------------|------|--------|-----------|--------|
| **Apprentice** (Free) | $0 | 2 | 2 | Basic (gTTS) | Gemini Flash | Trial |
| **Adventurer** | $9/mo | 8 | 10 | Standard (ElevenLabs) | GPT-4o-mini | Casual DMs |
| **Dungeon Master** | $19/mo | Unlimited | Unlimited | Premium voices | GPT-4o | Serious DMs |
| **Guild** | $29/mo | Unlimited | Unlimited | Premium + custom training | GPT-4o | Groups (5 seats) |

Annual discount: 2 months free ($90/yr, $190/yr, $290/yr).

### Stripe integration

```
┌─────────┐  checkout   ┌──────────┐  webhook    ┌──────────────┐
│ Browser  │───────────►│  Stripe   │────────────►│ Web Service  │
│          │◄───────────│ Checkout  │             │ /billing/    │
│          │  redirect   │          │             │  webhook     │
└─────────┘             └──────────┘             └──────┬───────┘
                                                        │
                                                 ┌──────▼───────┐
                                                 │ subscriptions│
                                                 │    table     │
                                                 └──────────────┘
```

**Subscription data model:**

```sql
CREATE TABLE subscriptions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL UNIQUE REFERENCES tenants(id),
    stripe_subscription_id TEXT UNIQUE,
    stripe_customer_id  TEXT NOT NULL,
    tier                TEXT NOT NULL DEFAULT 'apprentice',
    status              TEXT NOT NULL DEFAULT 'active',  -- active, past_due, canceled, trialing
    current_period_start TIMESTAMPTZ,
    current_period_end  TIMESTAMPTZ,
    cancel_at           TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**Webhook events handled:**

| Event | Action |
|-------|--------|
| `checkout.session.completed` | Create subscription record, upgrade tenant tier |
| `invoice.paid` | Extend period, clear past_due status |
| `invoice.payment_failed` | Mark past_due, send email, grace period (7 days) |
| `customer.subscription.updated` | Sync tier changes (up/downgrade) |
| `customer.subscription.deleted` | Downgrade to Apprentice (free) tier |

**Quota enforcement:**

The gateway's existing `usage.Store.CheckQuota()` mechanism is reused. The web management service writes `monthly_session_hours` to the tenant record based on the subscription tier. The gateway checks this at session start via `ValidateAndCreate()`.

| Tier | `monthly_session_hours` | Max concurrent sessions |
|------|------------------------|------------------------|
| Apprentice | 8 (≈2 sessions × 4h) | 1 |
| Adventurer | 32 (≈8 sessions × 4h) | 1 |
| Dungeon Master | 0 (unlimited) | 3 |
| Guild | 0 (unlimited) | 5 |

### Self-hosted / BYO-keys mode

For users who self-host Glyphoxa (open-core model), the billing system is optional. Config flag `--billing=disabled` skips Stripe integration and sets all tenants to unlimited. The self-hosted user provides their own LLM/TTS/STT API keys.

---

## 11. Voice Sample Upload

### Flow

```
1. DM uploads .wav/.mp3 in the NPC editor (max 10MB, 10 seconds)
2. Web service validates format (ffprobe), rejects invalid files
3. File stored in S3/MinIO: voice-samples/{tenant_id}/{sample_id}.wav
4. Metadata stored in DB: voice_samples table
5. For ElevenLabs custom voice: web service calls ElevenLabs Voice Clone API
6. Returns voice_id for use in NPC config
7. Preview endpoint returns presigned S3 URL (1-hour TTL)
```

**Storage:**

```sql
CREATE TABLE voice_samples (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    file_key    TEXT NOT NULL,          -- S3 object key
    file_size   BIGINT NOT NULL,
    duration_ms INT NOT NULL,
    format      TEXT NOT NULL,          -- wav, mp3
    provider_voice_id TEXT,             -- ElevenLabs voice ID after cloning
    status      TEXT NOT NULL DEFAULT 'uploaded',  -- uploaded, processing, ready, failed
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

---

## 12. Deployment Architecture

### K3s deployment (current infrastructure)

```
┌──────────────────────────────────────────────────────────────────┐
│                         K3s Cluster                               │
│                                                                    │
│  ┌───────────────────┐  ┌───────────────────┐  ┌──────────────┐  │
│  │ web-mgmt (Deploy) │  │ gateway (Deploy)  │  │ postgres     │  │
│  │ replicas: 1-3     │  │ replicas: 1       │  │ (StatefulSet)│  │
│  │ port: 8080        │  │ ports: 8080,50051 │  │ port: 5432   │  │
│  └────────┬──────────┘  └────────┬──────────┘  └──────┬───────┘  │
│           │                      │                     │          │
│           └──────────────────────┼─────────────────────┘          │
│                                  │                                │
│  ┌───────────────────┐  ┌───────▼───────────┐  ┌──────────────┐  │
│  │ vault (StatefulSet)│  │ worker (Job, N)   │  │ minio        │  │
│  │ port: 8200        │  │ ephemeral pods    │  │ (StatefulSet) │  │
│  └───────────────────┘  └───────────────────┘  │ port: 9000   │  │
│                                                 └──────────────┘  │
│  ┌───────────────────────────────────────────────────────────┐    │
│  │ Nginx Proxy Manager / Traefik Ingress                     │    │
│  │ app.glyphoxa.lan → web-mgmt:8080                          │    │
│  │ gw.glyphoxa.lan  → gateway:8080 (internal only)           │    │
│  └───────────────────────────────────────────────────────────┘    │
└──────────────────────────────────────────────────────────────────┘
```

### Helm chart additions

New chart or subchart: `deploy/helm/glyphoxa-web/`

```yaml
# values.yaml (web management service)
replicaCount: 1

image:
  repository: ghcr.io/mrwong99/glyphoxa-web
  tag: latest

env:
  DATABASE_DSN: "{{ .Values.global.databaseDSN }}"
  VAULT_ADDR: "{{ .Values.global.vaultAddr }}"
  GATEWAY_INTERNAL_URL: "http://glyphoxa-gateway:8080"
  GATEWAY_GRPC_ADDR: "glyphoxa-gateway:50051"
  STRIPE_WEBHOOK_SECRET: "{{ .Values.stripe.webhookSecret }}"
  S3_ENDPOINT: "http://minio:9000"
  OTEL_EXPORTER_OTLP_ENDPOINT: "http://otel-collector:4317"

resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 512Mi
```

### CI/CD pipeline

```
┌──────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐
│  Push to │───►│  CI      │───►│  Build   │───►│  Deploy  │
│  main    │    │  Checks  │    │  Images  │    │  K3s     │
└──────────┘    │          │    │          │    │          │
                │ lint     │    │ frontend │    │ helm     │
                │ test     │    │ backend  │    │ upgrade  │
                │ vet      │    │ multi-   │    │          │
                │ typecheck│    │ stage    │    │          │
                └──────────┘    └──────────┘    └──────────┘
```

**Build pipeline:**

1. **Frontend**: `npm ci && npm run build` → `dist/` folder
2. **Backend**: Multi-stage Dockerfile:
   - Stage 1 (Node): Build SPA → `dist/`
   - Stage 2 (Go): `COPY dist/ → embed → go build`
   - Stage 3 (Distroless): Copy binary only
3. **Push**: `ghcr.io/mrwong99/glyphoxa-web:${SHA}`
4. **Deploy**: `helm upgrade glyphoxa-web ./deploy/helm/glyphoxa-web`

The SPA is embedded in the Go binary via `//go:embed` — the web management service is a single binary that serves both the API and the static frontend. No separate static file server needed.

---

## 13. Monitoring & Observability

### Instrumentation

```
┌─────────────────────────────────────────────────────────────┐
│                   Web Management Service                     │
│                                                               │
│  ┌───────────┐  ┌───────────┐  ┌────────────────────────┐   │
│  │  Traces   │  │  Metrics  │  │  Structured Logs       │   │
│  │  (OTel)   │  │ (Prom)   │  │  (slog → JSON)         │   │
│  └─────┬─────┘  └─────┬─────┘  └───────────┬────────────┘   │
│        │              │                     │                │
│        └──────────────┼─────────────────────┘                │
│                       │                                       │
│                ┌──────▼──────┐                                │
│                │ OTel SDK    │                                │
│                └──────┬──────┘                                │
└───────────────────────┼──────────────────────────────────────┘
                        │ OTLP/gRPC
                 ┌──────▼──────┐
                 │OTel Collector│
                 └──┬─────┬────┘
                    │     │
             ┌──────▼┐  ┌─▼────────┐
             │ Loki  │  │Prometheus │
             │(logs) │  │(metrics)  │
             └───┬───┘  └────┬─────┘
                 │           │
             ┌───▼───────────▼───┐
             │      Grafana       │
             │  (dashboards)      │
             └────────────────────┘
```

### Key metrics (Prometheus)

```
# HTTP request metrics (auto-instrumented via OTel middleware)
http_server_duration_seconds{method, route, status_code}
http_server_active_requests{method, route}

# Business metrics
glyphoxa_web_active_users_total{tenant_id}
glyphoxa_web_signups_total{tier}
glyphoxa_web_subscription_changes_total{from_tier, to_tier}
glyphoxa_web_voice_previews_total{tenant_id}
glyphoxa_web_voice_uploads_total{tenant_id, status}

# Session proxy metrics
glyphoxa_web_session_starts_total{tenant_id, result}
glyphoxa_web_session_stops_total{tenant_id, reason}
```

### Super admin observability dashboard

The super admin dashboard aggregates:

1. **System health**: Gateway status, worker pod count, DB connection pool stats
2. **Business metrics**: Signups, active subscriptions by tier, MRR, churn
3. **Usage**: Total session hours, LLM tokens, STT seconds, TTS chars (all from `usage_records`)
4. **Per-tenant drill-down**: Usage vs quota, session history, error rates
5. **Provider health**: Latency P50/P99, error rates (from gateway's Prometheus metrics)

Implementation: Embed Grafana dashboards via iframe (Grafana supports anonymous/embedded mode), or build custom charts in React using the same Prometheus query API.

### Health probes

```
GET /healthz         → 200 OK (liveness — process is running)
GET /readyz          → 200 OK / 503 (readiness — DB connected, Vault reachable)
GET /metrics         → Prometheus exposition format
```

---

## 14. Database Schema Overview

### New tables (owned by web management service)

```sql
-- Users (see section 6)
-- Subscriptions (see section 10)
-- Voice samples (see section 11)

CREATE TABLE campaigns (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    game_system TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    settings    JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_campaigns_tenant ON campaigns(tenant_id);

CREATE TABLE provider_configs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    slot        TEXT NOT NULL,               -- llm, stt, tts, s2s, vad, embeddings
    provider    TEXT NOT NULL,               -- openai, elevenlabs, deepgram, etc.
    model       TEXT NOT NULL DEFAULT '',
    api_key_enc TEXT NOT NULL DEFAULT '',    -- Vault Transit encrypted
    base_url    TEXT NOT NULL DEFAULT '',
    options     JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(tenant_id, slot)
);

CREATE TABLE audit_log (
    id          BIGSERIAL PRIMARY KEY,
    tenant_id   UUID REFERENCES tenants(id),
    user_id     UUID REFERENCES users(id),
    action      TEXT NOT NULL,               -- tenant.create, npc.update, session.stop, etc.
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    details     JSONB,                       -- before/after diff
    ip_address  INET,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_tenant_time ON audit_log(tenant_id, created_at DESC);

CREATE TABLE support_tickets (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id),
    user_id     UUID NOT NULL REFERENCES users(id),
    external_id TEXT,                        -- ID in third-party system (Freshdesk, etc.)
    subject     TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'open',
    priority    TEXT NOT NULL DEFAULT 'normal',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### Migration strategy

- Use `golang-migrate/migrate` (same as gateway).
- Migration files in `internal/webmgmt/migrations/` (or similar).
- Migrations run on service startup (same pattern as gateway).
- Shared tables (`tenants`, `sessions`, `usage_records`) are NOT migrated by the web service — gateway owns those schemas.

---

## 15. Project Structure

```
glyphoxa-web/                      # Could be a separate repo or a directory in the monorepo
├── cmd/
│   └── glyphoxa-web/
│       └── main.go                # Entry point, config, DI wiring, graceful shutdown
├── internal/
│   ├── auth/                      # OAuth2 providers, JWT, middleware
│   │   ├── discord.go
│   │   ├── google.go
│   │   ├── jwt.go
│   │   └── middleware.go
│   ├── api/                       # HTTP handlers
│   │   ├── campaigns.go
│   │   ├── npcs.go
│   │   ├── sessions.go
│   │   ├── users.go
│   │   ├── billing.go
│   │   ├── providers.go
│   │   ├── voice_samples.go
│   │   ├── admin.go               # Super admin endpoints
│   │   └── router.go              # Route registration + middleware chains
│   ├── store/                     # Database repositories
│   │   ├── users.go
│   │   ├── campaigns.go
│   │   ├── subscriptions.go
│   │   ├── voice_samples.go
│   │   ├── audit.go
│   │   └── providers.go
│   ├── gateway/                   # Gateway client (HTTP + gRPC)
│   │   ├── client.go
│   │   └── session_proxy.go
│   ├── billing/                   # Stripe integration
│   │   ├── stripe.go
│   │   └── webhook.go
│   ├── storage/                   # S3/MinIO file storage
│   │   └── s3.go
│   ├── vault/                     # Vault Transit client (reuse from gateway pkg/)
│   │   └── transit.go
│   ├── observe/                   # OTel setup
│   │   └── otel.go
│   └── migrations/                # golang-migrate SQL files
│       ├── 000001_users.up.sql
│       ├── 000001_users.down.sql
│       ├── 000002_campaigns.up.sql
│       └── ...
├── web/                           # SPA frontend source
│   ├── package.json
│   ├── vite.config.ts
│   ├── tsconfig.json
│   ├── src/
│   │   ├── main.tsx
│   │   ├── api/                   # Generated TypeScript client
│   │   ├── components/            # shadcn/ui + custom components
│   │   ├── pages/                 # Route-level components
│   │   ├── hooks/                 # TanStack Query hooks
│   │   └── lib/                   # Utils, auth context, theme
│   └── dist/                      # Build output (embedded into Go binary)
├── Dockerfile                     # Multi-stage: Node → Go → Distroless
├── Makefile
└── go.mod
```

### Monorepo vs separate repo

**Recommendation: Start in the monorepo (`Glyphoxa/`), extract later if needed.**

- Shared Go types (`pkg/` — tenant, NPC definition, config) are importable directly.
- Single CI pipeline, single version, single `go.mod`.
- When the web service stabilizes and the team grows, extract to a separate repo with a shared `pkg/` module.

If monorepo, the web service lives at `cmd/glyphoxa-web/` with its packages under `internal/webmgmt/` to avoid polluting the gateway's `internal/gateway/` namespace.

---

## 16. Decision Log

| # | Decision | Chosen | Alternatives considered | Rationale |
|---|----------|--------|------------------------|-----------|
| D1 | Service topology | Separate service | Embedded in gateway (Option A) | Independent scaling, failure isolation, separate release cycle. Gateway stays lean for voice-critical path. Required for multi-tenant SaaS at >1000 users. |
| D2 | Backend language | Go | Node.js (Express/Fastify), Rust (Axum) | Same language as gateway — shared types, shared Vault/DB patterns, single toolchain. Luk writes Go. No cross-language serialization overhead. |
| D3 | Backend framework | stdlib `net/http` | Gin, Echo, Fiber, chi | Consistency with gateway. Go 1.22+ `http.ServeMux` has method routing. No framework churn. Middleware chains are trivial. |
| D4 | Frontend framework | React 19 + Vite | Svelte, Vue, HTMX, Go templates | Largest ecosystem, easiest hiring, Luk can find help. Voice preview + WebSocket monitoring + rich NPC editor require significant client-side JS — rules out HTMX. |
| D5 | Component library | shadcn/ui (Radix) | MUI, Ant Design, Chakra | Copy-paste ownership (no npm lock-in), accessible (Radix primitives), Tailwind-native. |
| D6 | Multi-tenancy | Shared DB, `tenant_id` columns | Separate DB per tenant, schema-per-tenant | Simpler ops (1 DB), cross-tenant queries for admin, connection pool efficiency. RLS for defense-in-depth. Scale limit ~10k tenants is well beyond target. |
| D7 | Auth strategy | OAuth2 (Discord/Google) + JWT | API key only, session cookies, Clerk/Auth0 | Self-service requires real user identity. JWT is stateless (scales horizontally). Discord OAuth is natural for TTRPG audience. Third-party auth (Clerk) adds cost + vendor lock-in. |
| D8 | Token storage | Access: memory / Refresh: HttpOnly cookie | localStorage, sessionStorage | Memory is immune to XSS. HttpOnly cookie immune to JS access. Best security posture without a token store. |
| D9 | Billing provider | Stripe | Paddle, LemonSqueezy, custom | Industry standard, excellent webhook reliability, Stripe Billing handles subscription lifecycle. Tax compliance via Stripe Tax. |
| D10 | Secret storage | Vault Transit (encrypt at rest) | AWS KMS, DB-level encryption, env vars | Already deployed, gateway uses it for bot tokens. Consistent encryption for BYO API keys. Key rotation is transparent. |
| D11 | File storage | MinIO (S3-compatible) | Local disk, Cloudflare R2 | Self-hosted (K3s), S3 API compatible, easy migration to cloud S3 later. |
| D12 | Deployment | Single Go binary (SPA embedded) on K3s | Separate frontend deploy (Vercel/Netlify) + API | Simpler ops (1 artifact), no CORS, consistent versioning. CDN layer can sit in front. |
| D13 | API contract | OpenAPI 3.1 spec → generated TS client | GraphQL, tRPC, manual client | REST is sufficient for CRUD-heavy management UI. OpenAPI gives typed client generation, Swagger docs, and validation schemas. |
| D14 | Pricing model | Tiered subscription (Apprentice→Guild) | Usage-based, session packs, flat rate | TTRPG community expects predictable costs. Session-based caps align with how DMs think. Free tier essential for adoption. See [pricing assessment](./pricing-models.md). |
| D15 | Support system | Third-party integration (Freshdesk/Zendesk) | Custom built, email only | Building a ticket system is not core value. Integrate via API — display in-app, manage externally. |
| D16 | Project location | Monorepo (start), extract later | Separate repo from day 1 | Shared Go types, single CI, simpler DX. Extract when team grows or release cycles diverge. |

---

## 17. Phase Breakdown

### Phase 1: Foundation (MVP)

**Goal:** DMs can sign up, create a campaign, configure NPCs, and see their session history.

- OAuth2 login (Discord)
- Tenant + campaign + NPC CRUD
- Session list + transcript viewer (read-only from gateway DB)
- Basic usage display
- API key management (BYO keys stored via Vault)
- SPA: dashboard, campaign editor, NPC editor with voice preview
- Deploy on K3s alongside gateway

**Auth:** Discord OAuth2 + JWT. Single role: `tenant_owner` (all DMs are owners of their tenant).

### Phase 2: Billing + Multi-user

**Goal:** Stripe subscriptions enforced, multiple users per tenant.

- Stripe integration (checkout, portal, webhooks)
- Tier-based quotas enforced
- User invite flow (Discord ID → assign role)
- Role-based access control
- Voice sample upload (S3/MinIO)
- Live session monitoring (WebSocket transcript stream)
- Onboarding wizard for new DMs

### Phase 3: Scale + Polish

**Goal:** Production-ready SaaS for >1000 users.

- Google OAuth2 + GitHub OAuth2
- Provider config UI (with test buttons)
- Knowledge graph browser (D3/react-force-graph)
- Super admin observability dashboard (Grafana embed or custom)
- Audit log
- Support ticket integration
- CDN for static assets
- PgBouncer for connection pooling
- Horizontal autoscaling (HPA)
- Rate limiting (Redis-backed)

---

## 18. Open Questions

1. **Monorepo vs multi-repo?** This plan assumes monorepo to start. If the web service diverges significantly in release cadence, extract to its own repo with a shared `pkg/` Go module.

2. **Gateway internal API authentication?** The web service needs to call the gateway for session control. Options: shared secret (simple), mTLS (Vault PKI is already available), or K8s NetworkPolicy (restrict access by namespace). Recommend: mTLS for production, shared secret for dev.

3. **Session start from web UI?** Currently sessions start via Discord slash commands. Should the web UI also be able to start sessions (selecting guild + channel)? This requires the gateway to expose a start-session-by-API endpoint. Recommend: yes, Phase 2.

4. **Multi-gateway support?** If Glyphoxa scales to multiple gateway instances (e.g., regional), the web management service needs a gateway registry. Defer until needed — single gateway is sufficient for >1000 users.

5. **Email notifications?** For billing events (payment failed, subscription expiring), session alerts, and support ticket updates. Recommend: Resend or SES, Phase 2.

6. **NPC avatar/image upload?** Adds visual identity in the UI. Can share the same S3/MinIO infrastructure as voice samples. Recommend: Phase 2 (nice-to-have).

7. **Mobile app?** The responsive SPA should work well on mobile browsers. A native app is not warranted until user demand is demonstrated. The SPA can be wrapped as a PWA for app-like experience.
