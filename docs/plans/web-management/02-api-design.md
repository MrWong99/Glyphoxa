---
title: "Web Management Service — API Design"
type: design
status: draft
date: 2026-03-24
parent: docs/plans/2026-03-23-admin-web-ui-plan.md
---

# Web Management Service — API Design

## Architecture Overview

The web management service is a **separate service** from the Glyphoxa gateway. It acts
as the control plane for all administrative operations, wrapping and extending the
gateway's existing Admin API while adding user authentication, campaign/NPC management,
billing, and observability.

```
┌─────────────────────────────────────────────────────────────┐
│                    Web Management Service                     │
│                                                              │
│  ┌──────────┐  ┌───────────┐  ┌──────────┐  ┌───────────┐  │
│  │ Auth API  │  │ Tenant API│  │ NPC API  │  │ Billing   │  │
│  │ (own)     │  │ (wraps GW)│  │ (direct) │  │ (own)     │  │
│  └─────┬────┘  └─────┬─────┘  └─────┬────┘  └─────┬─────┘  │
│        │             │              │             │          │
│        └─────────────┼──────────────┼─────────────┘          │
│                      │              │                        │
│              ┌───────┴──────┐  ┌────┴─────┐                  │
│              │Gateway Client│  │ Own DB   │                  │
│              │  (HTTP)      │  │(Postgres)│                  │
│              └───────┬──────┘  └──────────┘                  │
└──────────────────────┼──────────────────────────────────────┘
                       │
              ┌────────┴────────┐
              │  Glyphoxa       │
              │  Gateway        │
              │  Admin API      │
              │  (:8081)        │
              └─────────────────┘
```

**Key design principles:**

1. **Gateway is the source of truth** for tenants and sessions — the management service
   proxies/wraps those calls, never duplicates the data.
2. **Management service owns** users, auth, campaigns, billing, and subscription plans.
3. **NPC and memory data** lives in per-tenant schemas managed by the gateway — the
   management service connects to the same PostgreSQL cluster but reads/writes through
   its own store layer (not through the gateway HTTP API).
4. **All endpoints return JSON** with consistent envelope and error formats.
5. **OpenAPI 3.1 spec** is the contract — generated from Go struct tags, used to
   produce TypeScript client.

---

## Conventions

### Base URL

```
https://manage.glyphoxa.app/api/v1
```

All paths below are relative to this base.

### Authentication

Every endpoint requires authentication unless explicitly marked `public`.

| Method | Header | Description |
|--------|--------|-------------|
| JWT Bearer | `Authorization: Bearer <jwt>` | Primary auth for all user-facing endpoints |
| API Key | `X-API-Key: <key>` | Service-to-service and legacy admin access |

JWTs are issued by the management service itself (see [Auth API](#1-auth--users)).

### Request / Response Format

**Request body:** `Content-Type: application/json` (unless file upload, then `multipart/form-data`).

**Success response:**
```json
{
  "data": { ... },
  "meta": {
    "page": 1,
    "per_page": 25,
    "total": 142
  }
}
```

Single-resource responses omit `meta`. List responses always include pagination metadata.

**Error response:**
```json
{
  "error": {
    "code": "tenant_not_found",
    "message": "Tenant 'foo' does not exist.",
    "details": {}
  }
}
```

### Pagination

List endpoints accept:

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `page` | int | 1 | Page number (1-indexed) |
| `per_page` | int | 25 | Items per page (max 100) |
| `sort` | string | varies | Sort field (e.g., `created_at`, `name`) |
| `order` | string | `desc` | Sort direction: `asc` or `desc` |

### Rate Limiting

Rate limits are per-user (JWT `sub` claim) or per-API-key. Limits are returned in
response headers:

```
X-RateLimit-Limit: 60
X-RateLimit-Remaining: 57
X-RateLimit-Reset: 1711324800
```

Default tiers:

| Tier | Read | Write | Description |
|------|------|-------|-------------|
| Standard | 60/min | 30/min | Regular users |
| Admin | 120/min | 60/min | tenant_admin and above |
| Super | 300/min | 120/min | super_admin |
| Webhook | 100/min | 100/min | Stripe webhooks |

### Role Hierarchy

```
super_admin > tenant_admin > dm > viewer
```

Permissions are cumulative — each role inherits all permissions from roles below it.

---

## API Domains

### 1. Auth & Users

#### 1.1 Social Login — Discord OAuth2

##### `GET /auth/discord`  {#auth-discord}

Initiates Discord OAuth2 flow. Redirects browser to Discord's authorization page.

| | |
|---|---|
| **Auth** | `public` |
| **Rate limit** | 10/min per IP |

**Query parameters:**

| Param | Type | Required | Description |
|-------|------|----------|-------------|
| `redirect_uri` | string | no | Post-login redirect (default: `/dashboard`) |
| `state` | string | no | CSRF state (generated if omitted) |

**Response:** `302 Redirect` to `https://discord.com/oauth2/authorize?...`

Scopes requested: `identify`, `email`, `guilds`.

---

##### `GET /auth/discord/callback`  {#auth-discord-callback}

Handles Discord OAuth2 callback. Exchanges code for tokens, upserts user, issues JWT.

| | |
|---|---|
| **Auth** | `public` |
| **Rate limit** | 10/min per IP |

**Query parameters:**

| Param | Type | Required | Description |
|-------|------|----------|-------------|
| `code` | string | yes | OAuth2 authorization code |
| `state` | string | yes | CSRF state token |

**Response:** `302 Redirect` to `redirect_uri` with cookies set.

Sets two cookies:
- `glyphoxa_access` — JWT access token (HttpOnly, Secure, SameSite=Strict, 15min)
- `glyphoxa_refresh` — Refresh token (HttpOnly, Secure, SameSite=Strict, 30d)

**Error cases:**
- `400` — Missing or invalid code/state
- `403` — Discord user not linked to any tenant (auto-provision as `viewer` if
  tenant can be inferred from guild membership, otherwise reject)

---

#### 1.2 Social Login — Google OAuth2

##### `GET /auth/google`  {#auth-google}

Initiates Google OAuth2 flow. Same pattern as Discord.

| | |
|---|---|
| **Auth** | `public` |
| **Rate limit** | 10/min per IP |

**Query parameters:** Same as [Discord](#auth-discord).

**Response:** `302 Redirect` to Google authorization endpoint.

Scopes: `openid`, `email`, `profile`.

---

##### `GET /auth/google/callback`  {#auth-google-callback}

Handles Google OAuth2 callback. Same flow as Discord — exchange, upsert, issue JWT.

| | |
|---|---|
| **Auth** | `public` |
| **Rate limit** | 10/min per IP |

**Query parameters / response / cookies:** Same pattern as [Discord callback](#auth-discord-callback).

---

#### 1.3 JWT Token Management

##### `POST /auth/token`  {#auth-token}

Exchange credentials for a JWT token pair. Supports multiple grant types for
programmatic access (CLI tools, API integrations).

| | |
|---|---|
| **Auth** | `public` |
| **Rate limit** | 5/min per IP |

**Request body:**

```json
{
  "grant_type": "api_key",
  "api_key": "glx_..."
}
```

Supported `grant_type` values:
- `api_key` — Exchange a management API key for JWT tokens
- `refresh_token` — Exchange a refresh token for new token pair

**Response:**

```json
{
  "data": {
    "access_token": "eyJ...",
    "refresh_token": "glx_rt_...",
    "token_type": "Bearer",
    "expires_in": 900,
    "user": {
      "id": "usr_abc123",
      "name": "Luk",
      "role": "super_admin",
      "tenant_id": "rabenheim"
    }
  }
}
```

**JWT claims:**

```json
{
  "sub": "usr_abc123",
  "tid": "rabenheim",
  "role": "super_admin",
  "iss": "glyphoxa-manage",
  "iat": 1711324800,
  "exp": 1711325700
}
```

---

##### `POST /auth/refresh`  {#auth-refresh}

Refresh an expired access token using a valid refresh token.

| | |
|---|---|
| **Auth** | `public` (refresh token in body or cookie) |
| **Rate limit** | 10/min per IP |

**Request body:**

```json
{
  "refresh_token": "glx_rt_..."
}
```

If omitted, reads from `glyphoxa_refresh` cookie.

**Response:** Same as [`POST /auth/token`](#auth-token) — new access + refresh token pair.
Previous refresh token is invalidated (rotation).

---

##### `POST /auth/revoke`  {#auth-revoke}

Revoke a refresh token (logout).

| | |
|---|---|
| **Auth** | JWT |
| **Rate limit** | 10/min |

**Request body:**

```json
{
  "refresh_token": "glx_rt_...",
  "all": false
}
```

| Field | Type | Description |
|-------|------|-------------|
| `refresh_token` | string | Specific token to revoke (optional if `all=true`) |
| `all` | bool | Revoke all refresh tokens for the current user |

**Response:** `204 No Content`

---

#### 1.4 User CRUD

##### `POST /users`  {#create-user}

Create a new user within a tenant.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Write |

**Request body:**

```json
{
  "name": "Hans",
  "email": "hans@example.com",
  "discord_id": "123456789012345678",
  "google_id": "108234567890123456789",
  "role": "dm",
  "tenant_id": "rabenheim"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Display name |
| `email` | string | no | Email address |
| `discord_id` | string | no | Discord user snowflake (must be unique) |
| `google_id` | string | no | Google `sub` claim (must be unique) |
| `role` | string | yes | One of: `viewer`, `dm`, `tenant_admin`, `super_admin` |
| `tenant_id` | string | no | Defaults to caller's tenant. Only `super_admin` can set cross-tenant |

**Response:** `201 Created`

```json
{
  "data": {
    "id": "usr_abc123",
    "name": "Hans",
    "email": "hans@example.com",
    "discord_id": "123456789012345678",
    "google_id": null,
    "role": "dm",
    "tenant_id": "rabenheim",
    "created_at": "2026-03-24T10:00:00Z",
    "updated_at": "2026-03-24T10:00:00Z"
  }
}
```

**Error cases:**
- `400` — Missing required fields, invalid role
- `403` — Cannot create user with role >= own role (except `super_admin`)
- `409` — `discord_id` or `google_id` already linked to another user

---

##### `GET /users`  {#list-users}

List users. Scoped to caller's tenant unless `super_admin`.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `tenant_id` | string | caller's | Filter by tenant (`super_admin` only) |
| `role` | string | | Filter by role |
| `search` | string | | Search by name or email (case-insensitive substring) |

**Response:** `200 OK` — Paginated list of user objects.

---

##### `GET /users/{user_id}`  {#get-user}

Get a single user.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` (own profile) / `tenant_admin` (any in tenant) |
| **Rate limit** | Read |

**Response:** `200 OK` — User object. `404` if not found or cross-tenant.

---

##### `PUT /users/{user_id}`  {#update-user}

Update a user's profile or role.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` (own name/email) / `tenant_admin` (role changes) |
| **Rate limit** | Write |

**Request body:** Partial update — only include fields to change.

```json
{
  "name": "Hans the Brave",
  "role": "tenant_admin"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Display name |
| `email` | string | Email address |
| `role` | string | Role (requires `tenant_admin`+) |

**Constraints:**
- Cannot elevate a user to a role >= your own (except `super_admin`)
- Cannot change your own role
- Cannot change `tenant_id` (must delete + recreate)

**Response:** `200 OK` — Updated user object.

---

##### `DELETE /users/{user_id}`  {#delete-user}

Delete a user.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Write |

**Response:** `204 No Content`

**Constraints:**
- Cannot delete yourself
- Cannot delete a user with role >= your own

---

#### 1.5 User Profile & Preferences

##### `GET /users/me`  {#get-me}

Get the current user's profile (convenience alias for `GET /users/{self}`).

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Response:** `200 OK` — User object with additional fields:

```json
{
  "data": {
    "id": "usr_abc123",
    "name": "Luk",
    "email": "luk@example.com",
    "discord_id": "123456789012345678",
    "role": "super_admin",
    "tenant_id": "rabenheim",
    "preferences": {
      "theme": "dark",
      "language": "de",
      "notifications": {
        "session_start": true,
        "session_end": true,
        "quota_warning": true
      },
      "dashboard_layout": "compact"
    },
    "created_at": "2026-03-24T10:00:00Z",
    "updated_at": "2026-03-24T10:00:00Z"
  }
}
```

---

##### `PATCH /users/me/preferences`  {#update-preferences}

Update the current user's preferences (deep merge).

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Write |

**Request body:**

```json
{
  "theme": "light",
  "notifications": {
    "quota_warning": false
  }
}
```

**Response:** `200 OK` — Full preferences object after merge.

---

### 2. Tenants

The management service wraps the gateway's existing Admin API (`POST/GET/PUT/DELETE
/api/v1/tenants`) and extends it with subscription and provider key management.

#### 2.1 Tenant CRUD

##### `POST /tenants`  {#create-tenant}

Create a new tenant. Proxied to gateway with enrichment (subscription plan defaults,
schema provisioning).

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `super_admin` |
| **Rate limit** | Write |

**Request body:**

```json
{
  "id": "rabenheim",
  "license_tier": "shared",
  "bot_token": "MTIzNDU2...",
  "guild_ids": ["1234567890"],
  "dm_role_id": "9876543210",
  "monthly_session_hours": 20,
  "plan_id": "plan_adventurer",
  "display_name": "Die Chroniken von Rabenheim",
  "contact_email": "luk@example.com"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | yes | Tenant ID (lowercase, alphanumeric + underscore, max 63 chars) |
| `license_tier` | string | yes | `shared` or `dedicated` |
| `bot_token` | string | no | Discord bot token (encrypted via Vault Transit) |
| `guild_ids` | string[] | no | Discord guild snowflakes |
| `dm_role_id` | string | no | Discord role ID for DM permissions |
| `monthly_session_hours` | number | no | Quota override (0 = use plan default) |
| `plan_id` | string | no | Subscription plan ID (see [Billing](#6-billing--usage)) |
| `display_name` | string | no | Human-readable tenant name (stored in management DB) |
| `contact_email` | string | no | Primary contact email (stored in management DB) |

**Flow:**
1. Validate request
2. `POST` to gateway Admin API to create tenant
3. Store extended fields (`plan_id`, `display_name`, `contact_email`) in management DB
4. Return combined response

**Response:** `201 Created`

```json
{
  "data": {
    "id": "rabenheim",
    "license_tier": "shared",
    "guild_ids": ["1234567890"],
    "dm_role_id": "9876543210",
    "campaign_id": "",
    "monthly_session_hours": 20,
    "plan_id": "plan_adventurer",
    "display_name": "Die Chroniken von Rabenheim",
    "contact_email": "luk@example.com",
    "created_at": "2026-03-24T10:00:00Z",
    "updated_at": "2026-03-24T10:00:00Z"
  }
}
```

Note: `bot_token` is never returned in responses.

---

##### `GET /tenants`  {#list-tenants}

List tenants. `super_admin` sees all; others see only their own tenant.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` (own) / `super_admin` (all) |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Description |
|-------|------|-------------|
| `license_tier` | string | Filter by tier |
| `search` | string | Search by ID or display name |

**Response:** `200 OK` — Paginated list of tenant objects (with management-DB extensions merged).

---

##### `GET /tenants/{tenant_id}`  {#get-tenant}

Get a single tenant with merged gateway + management data.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` (own tenant) / `super_admin` (any) |
| **Rate limit** | Read |

**Response:** `200 OK` — Tenant object. `404` if not found.

---

##### `PUT /tenants/{tenant_id}`  {#update-tenant}

Update tenant. Splits updates between gateway (core fields) and management DB
(extended fields).

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` (own, limited fields) / `super_admin` (any, all fields) |
| **Rate limit** | Write |

**Request body:** Partial update.

```json
{
  "bot_token": "new_token...",
  "monthly_session_hours": 40,
  "display_name": "Rabenheim Chronicles"
}
```

**tenant_admin-editable fields:** `display_name`, `contact_email`, `bot_token`, `guild_ids`, `dm_role_id`

**super_admin-only fields:** `license_tier`, `monthly_session_hours`, `plan_id`

**Response:** `200 OK` — Updated tenant object.

---

##### `DELETE /tenants/{tenant_id}`  {#delete-tenant}

Delete a tenant and all associated data. Cascades to campaigns, NPCs, sessions,
usage records, and per-tenant schema.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `super_admin` |
| **Rate limit** | Write |

**Response:** `204 No Content`

**Warning:** This is destructive and irreversible. The frontend should require
confirmation with the tenant ID typed out.

---

#### 2.2 Provider Key Management (BYOK)

Tenants can bring their own API keys for LLM/STT/TTS providers, overriding
the platform defaults.

##### `GET /tenants/{tenant_id}/provider-keys`  {#list-provider-keys}

List configured provider keys for a tenant. Keys are redacted.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Read |

**Response:**

```json
{
  "data": [
    {
      "provider_type": "llm",
      "provider_name": "openai",
      "key_hint": "sk-...7xQ",
      "base_url": "",
      "model": "gpt-4o",
      "status": "active",
      "last_verified_at": "2026-03-24T09:00:00Z",
      "created_at": "2026-03-20T10:00:00Z"
    },
    {
      "provider_type": "tts",
      "provider_name": "elevenlabs",
      "key_hint": "el_...f3a",
      "base_url": "",
      "model": "",
      "status": "active",
      "last_verified_at": "2026-03-24T09:00:00Z",
      "created_at": "2026-03-21T10:00:00Z"
    }
  ]
}
```

---

##### `PUT /tenants/{tenant_id}/provider-keys/{provider_type}`  {#upsert-provider-key}

Set or update a provider key for a specific provider type.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Write |

**Path parameters:**
- `provider_type` — One of: `llm`, `stt`, `tts`, `s2s`, `embeddings`

**Request body:**

```json
{
  "provider_name": "openai",
  "api_key": "sk-...",
  "base_url": "",
  "model": "gpt-4o",
  "options": {}
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `provider_name` | string | yes | Registered provider name (e.g., `openai`, `deepgram`, `elevenlabs`) |
| `api_key` | string | yes | Provider API key (encrypted via Vault Transit) |
| `base_url` | string | no | Override provider's default endpoint |
| `model` | string | no | Model name override |
| `options` | object | no | Provider-specific options |

**Flow:**
1. Validate `provider_name` is registered in the gateway's provider registry
2. Optionally verify the key works (fire a lightweight test call)
3. Encrypt key via Vault Transit
4. Store in management DB

**Response:** `200 OK` — Provider key object (redacted).

---

##### `DELETE /tenants/{tenant_id}/provider-keys/{provider_type}`  {#delete-provider-key}

Remove a tenant's custom provider key. Falls back to platform defaults.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Write |

**Response:** `204 No Content`

---

##### `POST /tenants/{tenant_id}/provider-keys/{provider_type}/verify`  {#verify-provider-key}

Test a provider key by making a lightweight API call.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | 5/min |

**Response:**

```json
{
  "data": {
    "valid": true,
    "latency_ms": 142,
    "provider_name": "openai",
    "models_available": ["gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"]
  }
}
```

On failure:
```json
{
  "data": {
    "valid": false,
    "error": "authentication failed: invalid API key"
  }
}
```

---

#### 2.3 Tenant Settings

##### `GET /tenants/{tenant_id}/settings`  {#get-tenant-settings}

Get tenant-level settings (feature flags, defaults, etc.).

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Read |

**Response:**

```json
{
  "data": {
    "default_engine": "cascaded",
    "default_budget_tier": "standard",
    "default_voice_provider": "elevenlabs",
    "max_npcs_per_campaign": 25,
    "max_concurrent_sessions": 1,
    "features": {
      "knowledge_graph": true,
      "voice_preview": true,
      "session_replay": false,
      "custom_voice_upload": false
    },
    "locale": "de",
    "session_timeout_minutes": 360
  }
}
```

---

##### `PATCH /tenants/{tenant_id}/settings`  {#update-tenant-settings}

Update tenant settings (deep merge).

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` (own) / `super_admin` (feature flags) |
| **Rate limit** | Write |

**Request body:** Partial settings object.

**Response:** `200 OK` — Full settings object after merge.

---

### 3. Campaigns

Campaigns are a first-class entity owned by the management service. The gateway's
`Tenant.CampaignID` field becomes a reference into this table.

#### 3.1 Campaign CRUD

##### `POST /tenants/{tenant_id}/campaigns`  {#create-campaign}

Create a new campaign within a tenant.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | Write |

**Request body:**

```json
{
  "name": "Die Chroniken von Rabenheim",
  "system": "dnd5e",
  "description": "A dark fantasy campaign set in the cursed city of Rabenheim...",
  "lore": "## History\n\nRabenheim was founded in 1247...",
  "settings": {
    "language": "de",
    "default_voice_provider": "elevenlabs",
    "default_engine": "cascaded",
    "entity_files": [],
    "vtt_imports": []
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Campaign name |
| `system` | string | no | Game system identifier (`dnd5e`, `pf2e`, `coc7e`, `custom`) |
| `description` | string | no | Short description |
| `lore` | string | no | Campaign lore / world-building text (markdown) |
| `settings` | object | no | Campaign-specific configuration |

**Constraints:**
- Shared-tier tenants: max 1 campaign (unless plan allows more)
- Dedicated-tier tenants: unlimited campaigns

**Response:** `201 Created`

```json
{
  "data": {
    "id": "cmp_abc123",
    "tenant_id": "rabenheim",
    "name": "Die Chroniken von Rabenheim",
    "system": "dnd5e",
    "description": "A dark fantasy campaign set in the cursed city of Rabenheim...",
    "lore": "## History\n\nRabenheim was founded in 1247...",
    "settings": { ... },
    "npc_count": 0,
    "last_session_at": null,
    "created_at": "2026-03-24T10:00:00Z",
    "updated_at": "2026-03-24T10:00:00Z"
  }
}
```

---

##### `GET /tenants/{tenant_id}/campaigns`  {#list-campaigns}

List campaigns for a tenant.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Description |
|-------|------|-------------|
| `system` | string | Filter by game system |
| `search` | string | Search by name (case-insensitive) |

**Response:** `200 OK` — Paginated list of campaign objects (includes `npc_count` and `last_session_at`).

---

##### `GET /campaigns/{campaign_id}`  {#get-campaign}

Get a single campaign.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Response:** `200 OK` — Campaign object.

---

##### `PUT /campaigns/{campaign_id}`  {#update-campaign}

Update a campaign.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | Write |

**Request body:** Partial update — only include fields to change.

**Response:** `200 OK` — Updated campaign object.

---

##### `DELETE /campaigns/{campaign_id}`  {#delete-campaign}

Delete a campaign. NPCs within the campaign are also deleted. Session history is
preserved (orphaned but queryable by campaign_id).

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Write |

**Response:** `204 No Content`

---

#### 3.2 Campaign NPC Assignment

##### `POST /campaigns/{campaign_id}/npcs/{npc_id}`  {#link-npc}

Link an existing NPC to a campaign.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | Write |

**Response:** `204 No Content`

**Note:** NPCs are created within a campaign context (see [NPC CRUD](#41-npc-crud)),
so this endpoint is primarily for moving NPCs between campaigns.

---

##### `DELETE /campaigns/{campaign_id}/npcs/{npc_id}`  {#unlink-npc}

Unlink an NPC from a campaign. Does not delete the NPC — it becomes orphaned.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | Write |

**Response:** `204 No Content`

---

### 4. NPCs

NPC data lives in the gateway's per-tenant PostgreSQL schema (`npc_definitions` table).
The management service connects directly to this table through its own store layer
(using the same `npcstore.Store` interface).

#### 4.1 NPC CRUD

##### `POST /campaigns/{campaign_id}/npcs`  {#create-npc}

Create a new NPC within a campaign.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | Write |

**Request body:**

```json
{
  "name": "Heinrich der Wächter",
  "personality": "Ein strenger aber gerechter Stadtwächter der seit 20 Jahren...",
  "engine": "cascaded",
  "voice": {
    "provider": "elevenlabs",
    "voice_id": "pNInz6obpgDQGcFmaJgB",
    "pitch_shift": -1.5,
    "speed_factor": 0.9
  },
  "knowledge_scope": ["rabenheim_history", "guard_duties", "city_layout"],
  "secret_knowledge": ["mayor_corruption", "hidden_tunnels"],
  "behavior_rules": [
    "Spricht immer Deutsch",
    "Misstraut Fremden zunächst",
    "Wird gesprächiger nach Bestechung"
  ],
  "tools": ["patrol_route", "check_papers"],
  "budget_tier": "standard",
  "gm_helper": false,
  "address_only": true,
  "attributes": {
    "alignment": "lawful neutral",
    "race": "human",
    "class": "fighter"
  }
}
```

| Field | Type | Required | Validation | Description |
|-------|------|----------|------------|-------------|
| `name` | string | yes | non-empty | NPC's in-world display name |
| `personality` | string | no | | Free-text persona for LLM system prompt |
| `engine` | string | no | `cascaded`/`s2s`/`sentence_cascade` | Pipeline mode (default: `cascaded`) |
| `voice` | object | no | | Voice configuration |
| `voice.provider` | string | no | | TTS provider name |
| `voice.voice_id` | string | no | | Provider-specific voice identifier |
| `voice.pitch_shift` | number | no | [-10, 10] | Semitone pitch adjustment |
| `voice.speed_factor` | number | no | [0.5, 2.0] | Speed multiplier (0 = provider default) |
| `knowledge_scope` | string[] | no | | Topic domains for knowledge retrieval |
| `secret_knowledge` | string[] | no | | Knowledge only this NPC has |
| `behavior_rules` | string[] | no | | Behavioral instructions |
| `tools` | string[] | no | | MCP tool names this NPC can invoke |
| `budget_tier` | string | no | `fast`/`standard`/`deep` | Resource allocation tier (default: `fast`) |
| `gm_helper` | bool | no | | GM assistant mode (at most 1 per campaign) |
| `address_only` | bool | no | | Only responds when explicitly addressed |
| `attributes` | object | no | | Arbitrary key-value metadata |

**Constraints:**
- `gm_helper: true` — at most one per campaign. Returns `409` if another exists.
- Plan-based NPC limits enforced (e.g., free tier = 2 NPCs max).

**Response:** `201 Created`

```json
{
  "data": {
    "id": "npc_abc123",
    "campaign_id": "cmp_abc123",
    "name": "Heinrich der Wächter",
    "personality": "...",
    "engine": "cascaded",
    "voice": { ... },
    "knowledge_scope": ["rabenheim_history", "guard_duties", "city_layout"],
    "secret_knowledge": ["mayor_corruption", "hidden_tunnels"],
    "behavior_rules": ["Spricht immer Deutsch", "..."],
    "tools": ["patrol_route", "check_papers"],
    "budget_tier": "standard",
    "gm_helper": false,
    "address_only": true,
    "attributes": { "alignment": "lawful neutral", ... },
    "created_at": "2026-03-24T10:00:00Z",
    "updated_at": "2026-03-24T10:00:00Z"
  }
}
```

---

##### `GET /campaigns/{campaign_id}/npcs`  {#list-npcs}

List NPCs in a campaign.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Description |
|-------|------|-------------|
| `engine` | string | Filter by engine type |
| `search` | string | Search by name |

**Response:** `200 OK` — Paginated list of NPC objects.

---

##### `GET /npcs/{npc_id}`  {#get-npc}

Get a single NPC by ID.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Response:** `200 OK` — NPC object. `404` if not found.

---

##### `PUT /npcs/{npc_id}`  {#update-npc}

Update an NPC. Partial update — only include fields to change.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | Write |

**Request body:** Same fields as create, all optional.

**Response:** `200 OK` — Updated NPC object.

---

##### `DELETE /npcs/{npc_id}`  {#delete-npc}

Delete an NPC.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | Write |

**Response:** `204 No Content`

---

#### 4.2 Voice Preview

##### `POST /npcs/{npc_id}/voice-preview`  {#voice-preview}

Generate a TTS audio preview using the NPC's voice configuration.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | 5/min per user |

**Request body:**

```json
{
  "text": "Halt! Wer geht da in der Nacht durch die Straßen von Rabenheim?"
}
```

| Field | Type | Required | Validation | Description |
|-------|------|----------|------------|-------------|
| `text` | string | no | max 500 chars | Text to synthesize (default: auto-generated sample from personality) |

**Response:** `200 OK`

```
Content-Type: audio/mpeg
Content-Length: 24576
X-TTS-Provider: elevenlabs
X-TTS-Latency-Ms: 340
```

Binary audio data (MP3 format).

**Error cases:**
- `402` — TTS provider key invalid or quota exhausted
- `429` — Rate limit exceeded (voice previews are expensive)
- `503` — TTS provider unavailable

---

#### 4.3 Voice Sample Upload

##### `POST /npcs/{npc_id}/voice-samples`  {#upload-voice-sample}

Upload audio samples for custom voice creation (voice cloning). Provider-specific
(currently ElevenLabs Instant Voice Cloning).

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | 3/hour per tenant |
| **Content-Type** | `multipart/form-data` |

**Form fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `samples` | file[] | yes | Audio files (MP3/WAV/OGG, 1-25 files, each 1-10MB) |
| `name` | string | yes | Voice name for the provider |
| `description` | string | no | Voice description |

**Response:** `202 Accepted` — Voice creation is async.

```json
{
  "data": {
    "voice_job_id": "vj_abc123",
    "status": "processing",
    "estimated_completion_seconds": 120
  }
}
```

---

##### `GET /npcs/{npc_id}/voice-samples/{voice_job_id}`  {#get-voice-job}

Check the status of a voice creation job.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Read |

**Response:**

```json
{
  "data": {
    "voice_job_id": "vj_abc123",
    "status": "completed",
    "provider_voice_id": "pNInz6obpgDQGcFmaJgB",
    "created_at": "2026-03-24T10:00:00Z",
    "completed_at": "2026-03-24T10:02:15Z"
  }
}
```

Status values: `processing`, `completed`, `failed`.

---

#### 4.4 NPC Templates / Presets

##### `GET /npc-templates`  {#list-npc-templates}

List built-in NPC templates/presets for common archetypes.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Description |
|-------|------|-------------|
| `system` | string | Filter by game system (`dnd5e`, `pf2e`, etc.) |
| `category` | string | Filter by archetype (`tavern`, `guard`, `merchant`, `noble`, `villain`) |

**Response:**

```json
{
  "data": [
    {
      "id": "tmpl_tavern_keeper",
      "name": "Tavern Keeper",
      "system": "dnd5e",
      "category": "tavern",
      "description": "A warm, gossip-loving innkeeper who knows everyone's business.",
      "personality": "You are a warm and welcoming tavern keeper...",
      "behavior_rules": ["Offers food and drink first", "Shares rumors for coin"],
      "knowledge_scope": ["local_gossip", "tavern_menu", "travelers"],
      "suggested_voice": {
        "provider": "elevenlabs",
        "voice_id": "...",
        "pitch_shift": 0,
        "speed_factor": 1.0
      },
      "attributes": { "alignment": "neutral good" }
    }
  ]
}
```

---

##### `POST /campaigns/{campaign_id}/npcs/from-template`  {#create-npc-from-template}

Create an NPC from a template, with optional overrides.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | Write |

**Request body:**

```json
{
  "template_id": "tmpl_tavern_keeper",
  "overrides": {
    "name": "Greta die Wirtin",
    "personality": "Eine resolute Wirtin aus Bayern...",
    "voice": {
      "provider": "elevenlabs",
      "voice_id": "custom_id"
    }
  }
}
```

**Response:** `201 Created` — Full NPC object (same as [create NPC](#create-npc)).

---

### 5. Sessions

Session lifecycle data lives in the gateway's `sessions` table. The management service
queries it directly (read-only) and proxies control operations to the gateway.

#### 5.1 Session Queries

##### `GET /sessions`  {#list-sessions}

List sessions with filtering.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `tenant_id` | string | caller's | Filter by tenant (`super_admin` only for cross-tenant) |
| `campaign_id` | string | | Filter by campaign |
| `state` | string | | Filter by state: `pending`, `active`, `ended` |
| `guild_id` | string | | Filter by Discord guild |
| `after` | datetime | | Sessions started after this time (ISO 8601) |
| `before` | datetime | | Sessions started before this time |
| `has_error` | bool | | Filter to sessions with/without errors |

**Response:** `200 OK`

```json
{
  "data": [
    {
      "id": "sess_abc123",
      "tenant_id": "rabenheim",
      "campaign_id": "cmp_abc123",
      "guild_id": "1234567890",
      "channel_id": "9876543210",
      "license_tier": "shared",
      "state": "active",
      "error": "",
      "worker_pod": "worker-0",
      "duration_seconds": 2535,
      "entry_count": 147,
      "started_at": "2026-03-24T18:00:00Z",
      "ended_at": null,
      "last_heartbeat": "2026-03-24T18:42:15Z"
    }
  ],
  "meta": { "page": 1, "per_page": 25, "total": 42 }
}
```

---

##### `GET /sessions/active`  {#list-active-sessions}

List all currently active sessions. Convenience endpoint with enriched data.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Response:** `200 OK` — List of active session objects with additional live data:

```json
{
  "data": [
    {
      "id": "sess_abc123",
      "tenant_id": "rabenheim",
      "campaign_id": "cmp_abc123",
      "campaign_name": "Die Chroniken von Rabenheim",
      "guild_id": "1234567890",
      "channel_id": "9876543210",
      "state": "active",
      "worker_pod": "worker-0",
      "duration_seconds": 2535,
      "entry_count": 147,
      "active_npcs": [
        { "id": "npc_abc", "name": "Heinrich der Wächter", "muted": false },
        { "id": "npc_def", "name": "Greta die Wirtin", "muted": false }
      ],
      "started_at": "2026-03-24T18:00:00Z",
      "last_heartbeat": "2026-03-24T18:42:15Z"
    }
  ]
}
```

---

##### `GET /sessions/{session_id}`  {#get-session}

Get session details.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Response:** `200 OK`

```json
{
  "data": {
    "id": "sess_abc123",
    "tenant_id": "rabenheim",
    "campaign_id": "cmp_abc123",
    "campaign_name": "Die Chroniken von Rabenheim",
    "guild_id": "1234567890",
    "channel_id": "9876543210",
    "license_tier": "shared",
    "state": "ended",
    "error": "",
    "worker_pod": "worker-0",
    "duration_seconds": 4980,
    "entry_count": 312,
    "usage": {
      "session_hours": 1.38,
      "llm_tokens": 45230,
      "stt_seconds": 2840.5,
      "tts_chars": 18420
    },
    "npcs": [
      { "id": "npc_abc", "name": "Heinrich der Wächter" },
      { "id": "npc_def", "name": "Greta die Wirtin" }
    ],
    "started_at": "2026-03-24T18:00:00Z",
    "ended_at": "2026-03-24T19:23:00Z",
    "last_heartbeat": "2026-03-24T19:22:50Z"
  }
}
```

---

#### 5.2 Session Transcript

##### `GET /sessions/{session_id}/transcript`  {#get-transcript}

Get the session transcript (L1 entries).

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `after` | datetime | | Entries after this timestamp |
| `before` | datetime | | Entries before this timestamp |
| `speaker_id` | string | | Filter by speaker |
| `npc_id` | string | | Filter by NPC |
| `search` | string | | Full-text search within transcript text |
| `include_raw` | bool | false | Include uncorrected STT text |
| `format` | string | `json` | Response format: `json`, `text`, `srt` |

**Response (`json`):** `200 OK`

```json
{
  "data": {
    "session_id": "sess_abc123",
    "entries": [
      {
        "speaker_id": "123456789",
        "speaker_name": "Luk",
        "speaker_role": "gm",
        "text": "Ihr betretet die nebligen Straßen von Rabenheim.",
        "raw_text": "Ihr betretet die nebligen Strassen von Rabenheim",
        "npc_id": "",
        "timestamp": "2026-03-24T18:00:05Z",
        "duration_ms": 3200
      },
      {
        "speaker_id": "npc_abc123",
        "speaker_name": "Heinrich der Wächter",
        "speaker_role": "",
        "text": "Halt! Wer geht da?",
        "raw_text": "",
        "npc_id": "npc_abc123",
        "timestamp": "2026-03-24T18:00:09Z",
        "duration_ms": 1800
      }
    ],
    "total_entries": 312
  },
  "meta": { "page": 1, "per_page": 100, "total": 312 }
}
```

**Response (`text`):** `200 OK` — Plain text format:

```
Content-Type: text/plain; charset=utf-8

[18:00:05] Luk (GM): Ihr betretet die nebligen Straßen von Rabenheim.
[18:00:09] Heinrich der Wächter: Halt! Wer geht da?
```

**Response (`srt`):** `200 OK` — SRT subtitle format for future replay (issue #36):

```
Content-Type: text/srt; charset=utf-8

1
00:00:05,000 --> 00:00:08,200
[Luk] Ihr betretet die nebligen Straßen von Rabenheim.

2
00:00:09,000 --> 00:00:10,800
[Heinrich der Wächter] Halt! Wer geht da?
```

---

#### 5.3 Active Session Monitoring

##### `GET /sessions/{session_id}/live` (WebSocket)  {#session-live}

WebSocket endpoint for live session monitoring. Streams transcript entries and
audio stats in real-time.

| | |
|---|---|
| **Auth** | JWT (passed as `token` query param for WebSocket upgrade) |
| **Min role** | `viewer` |
| **Protocol** | `wss://` |

**Connection:** `wss://manage.glyphoxa.app/api/v1/sessions/{session_id}/live?token=<jwt>`

**Server → Client messages:**

Transcript entry:
```json
{
  "type": "transcript",
  "data": {
    "speaker_id": "npc_abc123",
    "speaker_name": "Heinrich der Wächter",
    "text": "Halt! Wer geht da?",
    "npc_id": "npc_abc123",
    "timestamp": "2026-03-24T18:00:09Z"
  }
}
```

Audio stats (every 5s):
```json
{
  "type": "audio_stats",
  "data": {
    "vad_active": true,
    "stt_latency_ms": 145,
    "tts_queue_depth": 2,
    "active_speaker": "123456789",
    "timestamp": "2026-03-24T18:42:15Z"
  }
}
```

NPC state change:
```json
{
  "type": "npc_state",
  "data": {
    "npc_id": "npc_abc123",
    "name": "Heinrich der Wächter",
    "muted": true
  }
}
```

Session ended:
```json
{
  "type": "session_ended",
  "data": {
    "reason": "user_stopped",
    "duration_seconds": 4980,
    "ended_at": "2026-03-24T19:23:00Z"
  }
}
```

**Client → Server messages:**

Ping (keepalive):
```json
{ "type": "ping" }
```

---

#### 5.4 Session Control

##### `POST /sessions/{session_id}/stop`  {#stop-session}

Force-stop an active session. Proxied to the gateway's worker client.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | Write |

**Request body:** (optional)

```json
{
  "reason": "DM ended session via admin panel"
}
```

**Response:** `200 OK`

```json
{
  "data": {
    "session_id": "sess_abc123",
    "previous_state": "active",
    "new_state": "ended",
    "stopped_at": "2026-03-24T19:23:00Z"
  }
}
```

**Error cases:**
- `404` — Session not found
- `409` — Session already ended

---

#### 5.5 Session Replay (Future — Issue #36)

##### `GET /sessions/{session_id}/replay`  {#session-replay}

Get replay data for a completed session, including audio segments and transcript
timeline. Reserved for future implementation.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |
| **Status** | `planned` — not yet implemented |

**Response:** `501 Not Implemented` (until issue #36 is resolved)

Planned response:

```json
{
  "data": {
    "session_id": "sess_abc123",
    "duration_seconds": 4980,
    "timeline": [
      {
        "offset_ms": 5000,
        "type": "speech",
        "speaker_name": "Luk",
        "text": "...",
        "audio_url": "/api/v1/sessions/sess_abc123/replay/audio/segment_001.opus"
      }
    ],
    "recap": {
      "text": "In tonight's session, the party entered Rabenheim...",
      "audio_url": "/api/v1/sessions/sess_abc123/replay/recap.mp3"
    }
  }
}
```

---

### 6. Billing & Usage

#### 6.1 Subscription Plans (Admin)

##### `POST /admin/plans`  {#create-plan}

Create a subscription plan.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `super_admin` |
| **Rate limit** | Write |

**Request body:**

```json
{
  "id": "plan_adventurer",
  "name": "Adventurer",
  "description": "For casual DMs running regular sessions.",
  "price_monthly_cents": 900,
  "price_yearly_cents": 9000,
  "currency": "eur",
  "features": {
    "max_sessions_per_month": 8,
    "max_session_hours": 20,
    "max_npcs_per_campaign": 10,
    "max_campaigns": 3,
    "voice_quality": "standard",
    "llm_tier": "standard",
    "knowledge_graph": true,
    "custom_voice_upload": false,
    "session_replay": false,
    "priority_support": false
  },
  "stripe_price_id_monthly": "price_...",
  "stripe_price_id_yearly": "price_...",
  "visible": true,
  "sort_order": 2
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | yes | Plan identifier (e.g., `plan_apprentice`, `plan_adventurer`) |
| `name` | string | yes | Display name |
| `description` | string | no | Plan description |
| `price_monthly_cents` | int | yes | Monthly price in smallest currency unit |
| `price_yearly_cents` | int | no | Annual price (0 = no annual option) |
| `currency` | string | yes | ISO 4217 currency code |
| `features` | object | yes | Feature limits and flags (see below) |
| `stripe_price_id_monthly` | string | no | Stripe Price ID for monthly billing |
| `stripe_price_id_yearly` | string | no | Stripe Price ID for annual billing |
| `visible` | bool | no | Show on pricing page (default: true) |
| `sort_order` | int | no | Display order on pricing page |

**Plan features schema:**

| Feature | Type | Description |
|---------|------|-------------|
| `max_sessions_per_month` | int | Max sessions per month (0 = unlimited) |
| `max_session_hours` | float | Max total session hours per month |
| `max_npcs_per_campaign` | int | NPC limit per campaign |
| `max_campaigns` | int | Campaign limit per tenant |
| `voice_quality` | string | `basic`, `standard`, `premium` |
| `llm_tier` | string | `budget`, `standard`, `premium` |
| `knowledge_graph` | bool | Access to L3 knowledge graph |
| `custom_voice_upload` | bool | Can upload voice samples |
| `session_replay` | bool | Access to session replay |
| `priority_support` | bool | Priority support queue |

**Response:** `201 Created` — Plan object.

---

##### `GET /admin/plans`  {#list-plans}

List all subscription plans.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `super_admin` (all) / `viewer` (visible only) |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Description |
|-------|------|-------------|
| `visible` | bool | Filter by visibility |

**Response:** `200 OK` — List of plan objects, sorted by `sort_order`.

---

##### `GET /admin/plans/{plan_id}`  {#get-plan}

Get a single plan.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Response:** `200 OK` — Plan object.

---

##### `PUT /admin/plans/{plan_id}`  {#update-plan}

Update a plan. Changes affect new subscriptions; existing subscribers keep their
current terms until renewal.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `super_admin` |
| **Rate limit** | Write |

**Request body:** Partial update.

**Response:** `200 OK` — Updated plan object.

---

##### `DELETE /admin/plans/{plan_id}`  {#delete-plan}

Soft-delete a plan. Sets `visible: false` and `archived: true`. Existing
subscribers remain on the plan until they change.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `super_admin` |
| **Rate limit** | Write |

**Response:** `204 No Content`

---

#### 6.2 Public Plans Listing

##### `GET /plans`  {#list-public-plans}

List visible subscription plans for the pricing page.

| | |
|---|---|
| **Auth** | `public` |
| **Rate limit** | 30/min per IP |

**Response:** `200 OK`

```json
{
  "data": [
    {
      "id": "plan_apprentice",
      "name": "Apprentice",
      "description": "Try Glyphoxa for free.",
      "price_monthly_cents": 0,
      "price_yearly_cents": 0,
      "currency": "eur",
      "features": {
        "max_sessions_per_month": 2,
        "max_session_hours": 4,
        "max_npcs_per_campaign": 2,
        "max_campaigns": 1,
        "voice_quality": "basic",
        "llm_tier": "budget",
        "knowledge_graph": false,
        "custom_voice_upload": false,
        "session_replay": false,
        "priority_support": false
      }
    },
    {
      "id": "plan_adventurer",
      "name": "Adventurer",
      "description": "For casual DMs running regular sessions.",
      "price_monthly_cents": 900,
      "price_yearly_cents": 9000,
      "currency": "eur",
      "features": { ... }
    },
    {
      "id": "plan_dungeon_master",
      "name": "Dungeon Master",
      "description": "For serious DMs who want it all.",
      "price_monthly_cents": 1900,
      "price_yearly_cents": 19000,
      "currency": "eur",
      "features": { ... }
    }
  ]
}
```

---

#### 6.3 User Subscription Management

##### `GET /subscriptions/current`  {#get-subscription}

Get the current user's (tenant's) active subscription.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Read |

**Response:** `200 OK`

```json
{
  "data": {
    "id": "sub_abc123",
    "tenant_id": "rabenheim",
    "plan_id": "plan_adventurer",
    "plan_name": "Adventurer",
    "status": "active",
    "billing_cycle": "monthly",
    "current_period_start": "2026-03-01T00:00:00Z",
    "current_period_end": "2026-04-01T00:00:00Z",
    "cancel_at_period_end": false,
    "stripe_subscription_id": "sub_stripe_...",
    "stripe_customer_id": "cus_stripe_...",
    "features": { ... },
    "usage_this_period": {
      "session_hours": 12.5,
      "session_hours_limit": 20,
      "sessions_count": 5,
      "sessions_limit": 8,
      "npcs_count": 7,
      "npcs_limit": 10
    },
    "created_at": "2026-01-15T10:00:00Z"
  }
}
```

Subscription statuses: `active`, `trialing`, `past_due`, `canceled`, `unpaid`.

---

##### `POST /subscriptions`  {#create-subscription}

Create a new subscription (or start a free trial). Creates a Stripe Checkout session.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Write |

**Request body:**

```json
{
  "plan_id": "plan_adventurer",
  "billing_cycle": "monthly",
  "success_url": "https://manage.glyphoxa.app/billing?success=true",
  "cancel_url": "https://manage.glyphoxa.app/billing?canceled=true"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `plan_id` | string | yes | Plan to subscribe to |
| `billing_cycle` | string | yes | `monthly` or `yearly` |
| `success_url` | string | yes | Redirect URL after successful payment |
| `cancel_url` | string | yes | Redirect URL if user cancels checkout |

**Response:** `200 OK`

```json
{
  "data": {
    "checkout_url": "https://checkout.stripe.com/c/pay/cs_...",
    "session_id": "cs_..."
  }
}
```

For free plans (`price = 0`): subscription is created immediately without Stripe,
returns the subscription object directly.

---

##### `POST /subscriptions/change-plan`  {#change-plan}

Change to a different plan (upgrade or downgrade).

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Write |

**Request body:**

```json
{
  "plan_id": "plan_dungeon_master",
  "billing_cycle": "yearly",
  "prorate": true
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `plan_id` | string | yes | New plan ID |
| `billing_cycle` | string | no | Change billing cycle (default: keep current) |
| `prorate` | bool | no | Prorate charges (default: true) |

**Response:** `200 OK` — Updated subscription object.

Upgrades take effect immediately. Downgrades take effect at the end of the
current billing period.

---

##### `POST /subscriptions/cancel`  {#cancel-subscription}

Cancel the current subscription.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Write |

**Request body:**

```json
{
  "at_period_end": true,
  "reason": "too_expensive",
  "feedback": "Would come back if there were more voices."
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `at_period_end` | bool | no | Cancel at end of period (default: true) vs immediately |
| `reason` | string | no | Cancellation reason code |
| `feedback` | string | no | Free-text feedback |

**Response:** `200 OK` — Subscription object with `cancel_at_period_end: true`.

---

##### `POST /subscriptions/resume`  {#resume-subscription}

Resume a subscription that was set to cancel at period end.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Write |

**Response:** `200 OK` — Subscription object with `cancel_at_period_end: false`.

---

#### 6.4 Usage Tracking & Reporting

##### `GET /usage`  {#get-usage-overview}

Get usage overview for the current tenant (or all tenants for `super_admin`).

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `tenant_id` | string | caller's | Tenant filter (`super_admin` only for cross-tenant) |
| `period` | string | current month | ISO 8601 month (`2026-03`) |

**Response:** `200 OK`

```json
{
  "data": {
    "tenant_id": "rabenheim",
    "period": "2026-03",
    "session_hours": 12.5,
    "session_hours_limit": 20,
    "llm_tokens": 245000,
    "stt_seconds": 14200.5,
    "tts_chars": 89400,
    "sessions_count": 5,
    "sessions_limit": 8,
    "quota_percentage": 62.5,
    "estimated_cost_cents": 625
  }
}
```

---

##### `GET /usage/history`  {#get-usage-history}

Get usage history across multiple periods.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `tenant_id` | string | caller's | Tenant filter |
| `from` | string | 6 months ago | Start period (`2026-01`) |
| `to` | string | current month | End period (`2026-03`) |
| `granularity` | string | `monthly` | `daily` or `monthly` |

**Response:** `200 OK`

```json
{
  "data": {
    "tenant_id": "rabenheim",
    "periods": [
      {
        "period": "2026-01",
        "session_hours": 18.3,
        "llm_tokens": 389000,
        "stt_seconds": 21400,
        "tts_chars": 134000,
        "sessions_count": 7
      },
      {
        "period": "2026-02",
        "session_hours": 14.7,
        "llm_tokens": 312000,
        "stt_seconds": 17800,
        "tts_chars": 105000,
        "sessions_count": 6
      }
    ]
  }
}
```

---

##### `GET /usage/breakdown`  {#get-usage-breakdown}

Per-session usage breakdown for a billing period.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `period` | string | current month | Billing period |

**Response:** `200 OK`

```json
{
  "data": {
    "tenant_id": "rabenheim",
    "period": "2026-03",
    "sessions": [
      {
        "session_id": "sess_abc123",
        "campaign_name": "Die Chroniken von Rabenheim",
        "started_at": "2026-03-15T18:00:00Z",
        "duration_seconds": 4980,
        "session_hours": 1.38,
        "llm_tokens": 45230,
        "stt_seconds": 2840.5,
        "tts_chars": 18420,
        "estimated_cost_cents": 138
      }
    ]
  }
}
```

---

##### `GET /usage/export`  {#export-usage}

Export usage data as CSV.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | 5/min |

**Query parameters:**

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `from` | string | start of current month | Start date |
| `to` | string | now | End date |
| `format` | string | `csv` | Export format: `csv` or `json` |

**Response:** `200 OK`

```
Content-Type: text/csv
Content-Disposition: attachment; filename="glyphoxa-usage-2026-03.csv"

session_id,campaign,started_at,duration_hours,llm_tokens,stt_seconds,tts_chars
sess_abc123,Die Chroniken von Rabenheim,2026-03-15T18:00:00Z,1.38,45230,2840.5,18420
```

---

#### 6.5 Quota Management

##### `GET /tenants/{tenant_id}/quota`  {#get-quota}

Get current quota status for a tenant.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Read |

**Response:** `200 OK`

```json
{
  "data": {
    "tenant_id": "rabenheim",
    "plan_id": "plan_adventurer",
    "monthly_session_hours": 20,
    "used_session_hours": 12.5,
    "remaining_session_hours": 7.5,
    "monthly_sessions": 8,
    "used_sessions": 5,
    "remaining_sessions": 3,
    "can_start_session": true,
    "quota_resets_at": "2026-04-01T00:00:00Z"
  }
}
```

---

##### `PUT /tenants/{tenant_id}/quota`  {#update-quota}

Manually override a tenant's quota (admin override).

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `super_admin` |
| **Rate limit** | Write |

**Request body:**

```json
{
  "monthly_session_hours": 40,
  "reason": "Compensating for outage on 2026-03-20"
}
```

**Response:** `200 OK` — Updated quota object.

The override is synced to the gateway's `Tenant.MonthlySessionHours` field.

---

#### 6.6 Stripe Webhook

##### `POST /webhooks/stripe`  {#stripe-webhook}

Stripe webhook endpoint for payment events.

| | |
|---|---|
| **Auth** | Stripe signature verification (`Stripe-Signature` header) |
| **Rate limit** | Webhook tier |

**Handled events:**

| Event | Action |
|-------|--------|
| `checkout.session.completed` | Activate subscription, update tenant plan |
| `invoice.paid` | Record payment, reset period quota |
| `invoice.payment_failed` | Mark subscription `past_due`, notify tenant admin |
| `customer.subscription.updated` | Sync plan changes from Stripe |
| `customer.subscription.deleted` | Mark subscription `canceled`, downgrade to free |
| `customer.subscription.trial_will_end` | Send trial ending notification (3 days before) |

**Response:** `200 OK` — `{"received": true}`

All events are idempotent — reprocessing the same event ID is a no-op.

---

### 7. Memory / Knowledge

These endpoints provide read/write access to the 3-layer memory system. Data lives
in per-tenant PostgreSQL schemas. The management service connects directly using
the existing `pkg/memory/postgres` store.

#### 7.1 L1 — Transcript Queries

##### `GET /campaigns/{campaign_id}/memory/transcripts`  {#list-transcripts}

List sessions with transcript data for a campaign.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Description |
|-------|------|-------------|
| `limit` | int | Max sessions to return (default: 10) |

**Response:** `200 OK`

```json
{
  "data": [
    {
      "session_id": "sess_abc123",
      "started_at": "2026-03-24T18:00:00Z",
      "ended_at": "2026-03-24T19:23:00Z",
      "entry_count": 312
    }
  ]
}
```

---

##### `GET /campaigns/{campaign_id}/memory/transcripts/{session_id}`  {#get-memory-transcript}

Get transcript entries for a specific session from the memory store.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Description |
|-------|------|-------------|
| `after` | datetime | Filter entries after timestamp |
| `before` | datetime | Filter entries before timestamp |
| `speaker_id` | string | Filter by speaker |
| `limit` | int | Max entries (default: 100) |

**Response:** `200 OK` — List of `TranscriptEntry` objects (same format as
[session transcript](#get-transcript)).

---

##### `POST /campaigns/{campaign_id}/memory/transcripts/search`  {#search-transcripts}

Full-text search across all transcripts in a campaign.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Request body:**

```json
{
  "query": "Bürgermeister Korruption",
  "session_id": "",
  "after": "2026-01-01T00:00:00Z",
  "before": "",
  "speaker_id": "",
  "limit": 50
}
```

**Response:** `200 OK`

```json
{
  "data": {
    "results": [
      {
        "session_id": "sess_abc123",
        "speaker_name": "Heinrich der Wächter",
        "text": "Der Bürgermeister? Frag nicht nach dem...",
        "timestamp": "2026-03-15T19:15:30Z",
        "relevance_score": 0.87
      }
    ],
    "total": 3
  }
}
```

---

#### 7.2 L2 — Semantic Search

##### `POST /campaigns/{campaign_id}/memory/semantic-search`  {#semantic-search}

Search campaign memory using semantic similarity (vector search via pgvector).

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | 10/min |

**Request body:**

```json
{
  "query": "What does Heinrich know about the mayor's corruption?",
  "top_k": 10,
  "filter": {
    "session_id": "",
    "speaker_id": "",
    "entity_id": "",
    "after": "2026-01-01T00:00:00Z",
    "before": ""
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `query` | string | yes | Natural language query (embedded server-side) |
| `top_k` | int | no | Number of results (default: 10, max: 50) |
| `filter` | object | no | Filter criteria (maps to `ChunkFilter`) |

**Response:** `200 OK`

```json
{
  "data": {
    "results": [
      {
        "chunk_id": "chk_abc123",
        "session_id": "sess_abc123",
        "content": "Heinrich flüsterte: 'Der Bürgermeister hat seine Hände...'",
        "speaker_id": "npc_abc123",
        "entity_id": "",
        "topic": "corruption",
        "distance": 0.15,
        "timestamp": "2026-03-15T19:15:30Z"
      }
    ]
  }
}
```

---

#### 7.3 L3 — Knowledge Graph

##### `GET /campaigns/{campaign_id}/knowledge/entities`  {#list-entities}

List entities in the campaign's knowledge graph.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Description |
|-------|------|-------------|
| `type` | string | Entity type (`npc`, `player`, `location`, `item`, `faction`, `event`, `quest`, `concept`) |
| `name` | string | Name substring search |
| `attribute` | string | Attribute filter (`key:value` format, e.g., `alignment:chaotic evil`) |

**Response:** `200 OK`

```json
{
  "data": [
    {
      "id": "ent_abc123",
      "type": "npc",
      "name": "Heinrich der Wächter",
      "attributes": {
        "alignment": "lawful neutral",
        "race": "human",
        "occupation": "city guard"
      },
      "created_at": "2026-03-10T10:00:00Z",
      "updated_at": "2026-03-24T19:00:00Z"
    }
  ],
  "meta": { "page": 1, "per_page": 25, "total": 34 }
}
```

---

##### `POST /campaigns/{campaign_id}/knowledge/entities`  {#create-entity}

Create a new entity in the knowledge graph.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | Write |

**Request body:**

```json
{
  "type": "location",
  "name": "Der Rabe Taverne",
  "attributes": {
    "district": "Altstadt",
    "owner": "Greta die Wirtin",
    "description": "A dimly lit tavern in the old quarter..."
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | Entity type |
| `name` | string | yes | Entity name |
| `attributes` | object | no | Arbitrary key-value metadata |

**Response:** `201 Created` — Entity object.

---

##### `GET /campaigns/{campaign_id}/knowledge/entities/{entity_id}`  {#get-entity}

Get a single entity with its relationships.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `include_relationships` | bool | true | Include related entities |
| `relationship_depth` | int | 1 | Depth of relationship traversal (max: 3) |

**Response:** `200 OK`

```json
{
  "data": {
    "entity": {
      "id": "ent_abc123",
      "type": "npc",
      "name": "Heinrich der Wächter",
      "attributes": { ... },
      "created_at": "2026-03-10T10:00:00Z",
      "updated_at": "2026-03-24T19:00:00Z"
    },
    "relationships": [
      {
        "source_id": "ent_abc123",
        "target_id": "ent_def456",
        "target_name": "Greta die Wirtin",
        "target_type": "npc",
        "rel_type": "knows",
        "attributes": { "closeness": "acquaintance" },
        "provenance": {
          "session_id": "sess_abc123",
          "timestamp": "2026-03-15T19:00:00Z",
          "confidence": 0.85,
          "source": "inferred",
          "dm_confirmed": false
        }
      },
      {
        "source_id": "ent_abc123",
        "target_id": "ent_ghi789",
        "target_name": "Stadtwache",
        "target_type": "faction",
        "rel_type": "member_of",
        "attributes": { "rank": "Hauptmann" },
        "provenance": {
          "session_id": "",
          "confidence": 1.0,
          "source": "stated",
          "dm_confirmed": true
        }
      }
    ]
  }
}
```

---

##### `PUT /campaigns/{campaign_id}/knowledge/entities/{entity_id}`  {#update-entity}

Update entity attributes (merge).

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | Write |

**Request body:**

```json
{
  "attributes": {
    "alignment": "neutral neutral",
    "new_attribute": "some value"
  }
}
```

**Response:** `200 OK` — Updated entity object.

---

##### `DELETE /campaigns/{campaign_id}/knowledge/entities/{entity_id}`  {#delete-entity}

Delete an entity and all its relationships.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | Write |

**Response:** `204 No Content`

---

##### `POST /campaigns/{campaign_id}/knowledge/relationships`  {#create-relationship}

Create a relationship between two entities.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | Write |

**Request body:**

```json
{
  "source_id": "ent_abc123",
  "target_id": "ent_def456",
  "rel_type": "hates",
  "attributes": {
    "reason": "Greta refused to serve him after midnight"
  },
  "provenance": {
    "confidence": 0.9,
    "source": "stated",
    "dm_confirmed": true
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `source_id` | string | yes | Source entity ID |
| `target_id` | string | yes | Target entity ID |
| `rel_type` | string | yes | Relationship type (e.g., `knows`, `hates`, `owns`, `member_of`) |
| `attributes` | object | no | Relationship metadata |
| `provenance` | object | no | Source and confidence info |

**Response:** `201 Created`

---

##### `DELETE /campaigns/{campaign_id}/knowledge/relationships`  {#delete-relationship}

Delete a specific relationship.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | Write |

**Query parameters:**

| Param | Type | Required | Description |
|-------|------|----------|-------------|
| `source_id` | string | yes | Source entity ID |
| `target_id` | string | yes | Target entity ID |
| `rel_type` | string | yes | Relationship type |

**Response:** `204 No Content`

---

##### `GET /campaigns/{campaign_id}/knowledge/subgraph/{npc_id}`  {#get-npc-subgraph}

Get the visible knowledge subgraph for a specific NPC. Returns all entities and
relationships that this NPC would have access to (based on knowledge scope).

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | Read |

**Response:** `200 OK`

```json
{
  "data": {
    "npc_id": "npc_abc123",
    "npc_name": "Heinrich der Wächter",
    "entities": [ ... ],
    "relationships": [ ... ]
  }
}
```

Designed for rendering as a force-directed graph in the frontend.

---

##### `GET /campaigns/{campaign_id}/knowledge/paths`  {#find-path}

Find the shortest path between two entities in the knowledge graph.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `dm` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Required | Description |
|-------|------|----------|-------------|
| `from` | string | yes | Source entity ID |
| `to` | string | yes | Target entity ID |
| `max_depth` | int | no | Maximum hops (default: 5) |

**Response:** `200 OK`

```json
{
  "data": {
    "path": [
      { "id": "ent_abc", "name": "Heinrich", "type": "npc" },
      { "id": "ent_def", "name": "Stadtwache", "type": "faction" },
      { "id": "ent_ghi", "name": "Bürgermeister Krause", "type": "npc" }
    ],
    "relationships": [
      { "source_id": "ent_abc", "target_id": "ent_def", "rel_type": "member_of" },
      { "source_id": "ent_ghi", "target_id": "ent_def", "rel_type": "controls" }
    ]
  }
}
```

---

#### 7.4 Memory Admin

##### `POST /campaigns/{campaign_id}/memory/rebuild-indexes`  {#rebuild-indexes}

Rebuild semantic search indexes (L2 pgvector indexes) for a campaign. Use after
bulk imports or embedding model changes.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | 1/hour |

**Response:** `202 Accepted`

```json
{
  "data": {
    "job_id": "job_abc123",
    "status": "queued",
    "estimated_duration_seconds": 300
  }
}
```

---

##### `DELETE /campaigns/{campaign_id}/memory`  {#clear-memory}

Clear all memory data (L1 + L2 + L3) for a campaign. Destructive and irreversible.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `tenant_admin` |
| **Rate limit** | Write |

**Request body:**

```json
{
  "confirm": "DELETE rabenheim/cmp_abc123",
  "layers": ["l1", "l2", "l3"]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `confirm` | string | yes | Confirmation string: `DELETE {tenant_id}/{campaign_id}` |
| `layers` | string[] | no | Layers to clear (default: all). Options: `l1`, `l2`, `l3` |

**Response:** `200 OK`

```json
{
  "data": {
    "cleared": {
      "l1_entries": 1247,
      "l2_chunks": 892,
      "l3_entities": 34,
      "l3_relationships": 78
    }
  }
}
```

---

### 8. Support

#### 8.1 Third-Party Ticket Integration

The management service integrates with an external ticket system rather than
building its own. The API provides a thin proxy layer that normalizes ticket
CRUD across providers.

**Supported providers:** Freshdesk (planned), Linear (planned), GitHub Issues (planned).

The provider is configured via environment variable:
`GLYPHOXA_SUPPORT_PROVIDER=freshdesk`

##### `POST /support/tickets`  {#create-ticket}

Create a support ticket.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | 5/hour |

**Request body:**

```json
{
  "subject": "NPC voice not working after update",
  "description": "After updating Heinrich's voice config, the voice preview returns...",
  "priority": "normal",
  "category": "bug",
  "metadata": {
    "tenant_id": "rabenheim",
    "npc_id": "npc_abc123",
    "browser": "Firefox 130",
    "url": "/npcs/npc_abc123"
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `subject` | string | yes | Ticket subject |
| `description` | string | yes | Detailed description |
| `priority` | string | no | `low`, `normal`, `high`, `urgent` (default: `normal`) |
| `category` | string | no | `bug`, `feature`, `question`, `billing` |
| `metadata` | object | no | Auto-populated context (tenant, browser, page URL) |

**Response:** `201 Created`

```json
{
  "data": {
    "id": "tkt_abc123",
    "external_id": "FD-12345",
    "external_url": "https://glyphoxa.freshdesk.com/support/tickets/12345",
    "subject": "NPC voice not working after update",
    "status": "open",
    "priority": "normal",
    "created_at": "2026-03-24T10:00:00Z"
  }
}
```

---

##### `GET /support/tickets`  {#list-tickets}

List support tickets for the current user.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Description |
|-------|------|-------------|
| `status` | string | Filter by status: `open`, `pending`, `resolved`, `closed` |

**Response:** `200 OK` — Paginated list of ticket objects.

---

##### `GET /support/tickets/{ticket_id}`  {#get-ticket}

Get ticket details including conversation history.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` (own tickets) / `tenant_admin` (tenant's tickets) |
| **Rate limit** | Read |

**Response:** `200 OK`

```json
{
  "data": {
    "id": "tkt_abc123",
    "external_id": "FD-12345",
    "external_url": "https://glyphoxa.freshdesk.com/support/tickets/12345",
    "subject": "NPC voice not working after update",
    "description": "...",
    "status": "pending",
    "priority": "normal",
    "messages": [
      {
        "author": "Support Agent",
        "body": "Thanks for reporting this. Can you share the NPC configuration?",
        "created_at": "2026-03-24T11:00:00Z"
      }
    ],
    "created_at": "2026-03-24T10:00:00Z",
    "updated_at": "2026-03-24T11:00:00Z"
  }
}
```

---

##### `POST /support/tickets/{ticket_id}/reply`  {#reply-to-ticket}

Add a reply to an existing ticket.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `viewer` |
| **Rate limit** | 10/hour per ticket |

**Request body:**

```json
{
  "body": "Here's the NPC config: ..."
}
```

**Response:** `200 OK` — Updated ticket object.

---

### 9. Admin / Observability

#### 9.1 System Health

##### `GET /admin/health`  {#system-health}

Aggregated health status across all components. Combines the management service's
own health with gateway health probes.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `super_admin` |
| **Rate limit** | Read |

**Response:** `200 OK`

```json
{
  "data": {
    "status": "healthy",
    "components": {
      "management_db": { "status": "ok", "latency_ms": 2 },
      "gateway": { "status": "ok", "url": "http://gateway:8081", "latency_ms": 5 },
      "gateway_db": { "status": "ok", "latency_ms": 3 },
      "stripe": { "status": "ok", "latency_ms": 120 },
      "redis": { "status": "ok", "latency_ms": 1 }
    },
    "version": "0.12.0",
    "build_sha": "2d28fc0",
    "uptime_seconds": 86400
  }
}
```

---

##### `GET /admin/health/gateway`  {#gateway-health}

Proxy to the gateway's `/healthz` and `/readyz` endpoints.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `super_admin` |
| **Rate limit** | Read |

**Response:** `200 OK`

```json
{
  "data": {
    "liveness": { "status": "ok" },
    "readiness": {
      "status": "ok",
      "checks": {
        "database": "ok",
        "providers": "ok"
      }
    }
  }
}
```

---

#### 9.2 OpenTelemetry Dashboard Proxy

##### `GET /admin/metrics`  {#metrics-proxy}

Proxy to the gateway's Prometheus metrics endpoint. Returns raw Prometheus
exposition format for dashboard consumption.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `super_admin` |
| **Rate limit** | 30/min |

**Response:** `200 OK`

```
Content-Type: text/plain; version=0.0.4; charset=utf-8

# HELP glyphoxa_sessions_active Number of active voice sessions
# TYPE glyphoxa_sessions_active gauge
glyphoxa_sessions_active{tenant_id="rabenheim"} 1
...
```

---

##### `GET /admin/metrics/summary`  {#metrics-summary}

Pre-aggregated metrics summary for the admin dashboard. Avoids the frontend
having to parse Prometheus format.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `super_admin` |
| **Rate limit** | Read |

**Response:** `200 OK`

```json
{
  "data": {
    "active_sessions": 2,
    "total_sessions_today": 5,
    "total_tenants": 3,
    "total_users": 12,
    "total_npcs": 24,
    "provider_health": {
      "llm": { "provider": "openai", "status": "healthy", "p50_ms": 340, "p99_ms": 1200 },
      "stt": { "provider": "deepgram", "status": "healthy", "p50_ms": 145, "p99_ms": 380 },
      "tts": { "provider": "elevenlabs", "status": "healthy", "p50_ms": 280, "p99_ms": 850 }
    },
    "usage_this_month": {
      "total_session_hours": 47.3,
      "total_llm_tokens": 1245000,
      "total_stt_seconds": 68400,
      "total_tts_chars": 425000
    },
    "error_rate_24h": 0.02
  }
}
```

---

#### 9.3 Billing Reports

##### `GET /admin/billing/report`  {#billing-report}

Aggregated billing report across all tenants.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `super_admin` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `period` | string | current month | Billing period (`2026-03`) |

**Response:** `200 OK`

```json
{
  "data": {
    "period": "2026-03",
    "summary": {
      "total_revenue_cents": 4700,
      "total_cost_cents": 1850,
      "margin_percentage": 60.6,
      "active_subscriptions": 3,
      "churned_subscriptions": 0,
      "new_subscriptions": 1
    },
    "by_plan": [
      {
        "plan_id": "plan_apprentice",
        "plan_name": "Apprentice",
        "subscriber_count": 1,
        "revenue_cents": 0
      },
      {
        "plan_id": "plan_adventurer",
        "plan_name": "Adventurer",
        "subscriber_count": 2,
        "revenue_cents": 1800
      },
      {
        "plan_id": "plan_dungeon_master",
        "plan_name": "Dungeon Master",
        "subscriber_count": 1,
        "revenue_cents": 1900
      }
    ],
    "by_tenant": [
      {
        "tenant_id": "rabenheim",
        "plan_name": "Dungeon Master",
        "revenue_cents": 1900,
        "cost_cents": 820,
        "session_hours": 18.3,
        "sessions": 7
      }
    ]
  }
}
```

---

##### `GET /admin/billing/mrr`  {#mrr}

Monthly Recurring Revenue (MRR) trend.

| | |
|---|---|
| **Auth** | JWT |
| **Min role** | `super_admin` |
| **Rate limit** | Read |

**Query parameters:**

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `months` | int | 12 | Number of months of history |

**Response:** `200 OK`

```json
{
  "data": {
    "current_mrr_cents": 4700,
    "trend": [
      { "month": "2026-01", "mrr_cents": 1900, "subscribers": 1 },
      { "month": "2026-02", "mrr_cents": 2800, "subscribers": 2 },
      { "month": "2026-03", "mrr_cents": 4700, "subscribers": 3 }
    ]
  }
}
```

---

## Endpoint Summary

### Public Endpoints (no auth)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/auth/discord` | Initiate Discord OAuth2 |
| GET | `/auth/discord/callback` | Discord OAuth2 callback |
| GET | `/auth/google` | Initiate Google OAuth2 |
| GET | `/auth/google/callback` | Google OAuth2 callback |
| POST | `/auth/token` | Exchange credentials for JWT |
| POST | `/auth/refresh` | Refresh access token |
| GET | `/plans` | List public subscription plans |
| POST | `/webhooks/stripe` | Stripe payment webhooks |

### Auth Required — viewer+

| Method | Path | Description |
|--------|------|-------------|
| POST | `/auth/revoke` | Revoke refresh token |
| GET | `/users/me` | Get own profile |
| PATCH | `/users/me/preferences` | Update preferences |
| GET | `/tenants/{id}` | Get own tenant |
| GET | `/tenants/{id}/campaigns` | List campaigns |
| GET | `/campaigns/{id}` | Get campaign |
| GET | `/campaigns/{id}/npcs` | List NPCs |
| GET | `/npcs/{id}` | Get NPC |
| GET | `/sessions` | List sessions |
| GET | `/sessions/active` | List active sessions |
| GET | `/sessions/{id}` | Get session |
| GET | `/sessions/{id}/transcript` | Get transcript |
| WS | `/sessions/{id}/live` | Live session stream |
| GET | `/campaigns/{id}/memory/transcripts` | List transcript sessions |
| GET | `/campaigns/{id}/memory/transcripts/{sid}` | Get session transcript |
| POST | `/campaigns/{id}/memory/transcripts/search` | Search transcripts |
| GET | `/campaigns/{id}/knowledge/entities` | List entities |
| GET | `/campaigns/{id}/knowledge/entities/{eid}` | Get entity |
| GET | `/campaigns/{id}/knowledge/paths` | Find path |
| GET | `/npc-templates` | List NPC templates |
| POST | `/support/tickets` | Create support ticket |
| GET | `/support/tickets` | List own tickets |
| GET | `/support/tickets/{id}` | Get ticket |
| POST | `/support/tickets/{id}/reply` | Reply to ticket |

### Auth Required — dm+

| Method | Path | Description |
|--------|------|-------------|
| POST | `/tenants/{id}/campaigns` | Create campaign |
| PUT | `/campaigns/{id}` | Update campaign |
| POST | `/campaigns/{id}/npcs` | Create NPC |
| POST | `/campaigns/{id}/npcs/from-template` | Create NPC from template |
| POST | `/campaigns/{id}/npcs/{nid}` | Link NPC to campaign |
| DELETE | `/campaigns/{id}/npcs/{nid}` | Unlink NPC |
| PUT | `/npcs/{id}` | Update NPC |
| DELETE | `/npcs/{id}` | Delete NPC |
| POST | `/npcs/{id}/voice-preview` | Voice preview |
| POST | `/sessions/{id}/stop` | Force-stop session |
| POST | `/campaigns/{id}/memory/semantic-search` | Semantic search |
| POST | `/campaigns/{id}/knowledge/entities` | Create entity |
| PUT | `/campaigns/{id}/knowledge/entities/{eid}` | Update entity |
| DELETE | `/campaigns/{id}/knowledge/entities/{eid}` | Delete entity |
| POST | `/campaigns/{id}/knowledge/relationships` | Create relationship |
| DELETE | `/campaigns/{id}/knowledge/relationships` | Delete relationship |
| GET | `/campaigns/{id}/knowledge/subgraph/{nid}` | NPC knowledge subgraph |

### Auth Required — tenant_admin+

| Method | Path | Description |
|--------|------|-------------|
| PUT | `/tenants/{id}` | Update own tenant |
| DELETE | `/campaigns/{id}` | Delete campaign |
| POST | `/users` | Create user |
| GET | `/users` | List users |
| PUT | `/users/{id}` | Update user |
| DELETE | `/users/{id}` | Delete user |
| GET | `/tenants/{id}/provider-keys` | List provider keys |
| PUT | `/tenants/{id}/provider-keys/{type}` | Set provider key |
| DELETE | `/tenants/{id}/provider-keys/{type}` | Remove provider key |
| POST | `/tenants/{id}/provider-keys/{type}/verify` | Verify provider key |
| GET | `/tenants/{id}/settings` | Get tenant settings |
| PATCH | `/tenants/{id}/settings` | Update tenant settings |
| GET | `/subscriptions/current` | Get subscription |
| POST | `/subscriptions` | Create subscription |
| POST | `/subscriptions/change-plan` | Change plan |
| POST | `/subscriptions/cancel` | Cancel subscription |
| POST | `/subscriptions/resume` | Resume subscription |
| GET | `/usage` | Usage overview |
| GET | `/usage/history` | Usage history |
| GET | `/usage/breakdown` | Per-session breakdown |
| GET | `/usage/export` | Export usage CSV |
| GET | `/tenants/{id}/quota` | Get quota |
| POST | `/npcs/{id}/voice-samples` | Upload voice samples |
| GET | `/npcs/{id}/voice-samples/{jid}` | Check voice job |
| POST | `/campaigns/{id}/memory/rebuild-indexes` | Rebuild search indexes |
| DELETE | `/campaigns/{id}/memory` | Clear memory |

### Auth Required — super_admin

| Method | Path | Description |
|--------|------|-------------|
| POST | `/tenants` | Create tenant |
| GET | `/tenants` | List all tenants |
| DELETE | `/tenants/{id}` | Delete tenant |
| PUT | `/tenants/{id}/quota` | Override quota |
| POST | `/admin/plans` | Create plan |
| GET | `/admin/plans` | List all plans |
| GET | `/admin/plans/{id}` | Get plan |
| PUT | `/admin/plans/{id}` | Update plan |
| DELETE | `/admin/plans/{id}` | Archive plan |
| GET | `/admin/health` | System health |
| GET | `/admin/health/gateway` | Gateway health |
| GET | `/admin/metrics` | Prometheus metrics |
| GET | `/admin/metrics/summary` | Metrics summary |
| GET | `/admin/billing/report` | Billing report |
| GET | `/admin/billing/mrr` | MRR trend |

---

## Database Schema (Management Service)

The management service has its **own PostgreSQL database**, separate from the
gateway's database. It connects to the gateway's DB in read-only mode for
session and NPC data.

```sql
-- Users (management DB)
CREATE TABLE users (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL,
    discord_id  TEXT UNIQUE,
    google_id   TEXT UNIQUE,
    email       TEXT,
    name        TEXT NOT NULL,
    role        TEXT NOT NULL DEFAULT 'viewer',
    preferences JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Refresh tokens (management DB)
CREATE TABLE refresh_tokens (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Campaigns (management DB)
CREATE TABLE campaigns (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL,
    name        TEXT NOT NULL,
    system      TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    lore        TEXT NOT NULL DEFAULT '',
    settings    JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Tenant extensions (management DB — supplements gateway tenant data)
CREATE TABLE tenant_profiles (
    tenant_id     TEXT PRIMARY KEY,
    plan_id       TEXT REFERENCES subscription_plans(id),
    display_name  TEXT NOT NULL DEFAULT '',
    contact_email TEXT NOT NULL DEFAULT '',
    settings      JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Provider keys (management DB — encrypted via Vault)
CREATE TABLE provider_keys (
    id             BIGSERIAL PRIMARY KEY,
    tenant_id      TEXT NOT NULL,
    provider_type  TEXT NOT NULL,
    provider_name  TEXT NOT NULL,
    api_key        TEXT NOT NULL,  -- vault-encrypted
    base_url       TEXT NOT NULL DEFAULT '',
    model          TEXT NOT NULL DEFAULT '',
    options        JSONB NOT NULL DEFAULT '{}',
    status         TEXT NOT NULL DEFAULT 'active',
    last_verified  TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(tenant_id, provider_type)
);

-- Subscription plans (management DB)
CREATE TABLE subscription_plans (
    id                     TEXT PRIMARY KEY,
    name                   TEXT NOT NULL,
    description            TEXT NOT NULL DEFAULT '',
    price_monthly_cents    INT NOT NULL DEFAULT 0,
    price_yearly_cents     INT NOT NULL DEFAULT 0,
    currency               TEXT NOT NULL DEFAULT 'eur',
    features               JSONB NOT NULL DEFAULT '{}',
    stripe_price_id_monthly TEXT,
    stripe_price_id_yearly  TEXT,
    visible                BOOLEAN NOT NULL DEFAULT true,
    archived               BOOLEAN NOT NULL DEFAULT false,
    sort_order             INT NOT NULL DEFAULT 0,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Subscriptions (management DB)
CREATE TABLE subscriptions (
    id                     TEXT PRIMARY KEY,
    tenant_id              TEXT NOT NULL UNIQUE,
    plan_id                TEXT NOT NULL REFERENCES subscription_plans(id),
    status                 TEXT NOT NULL DEFAULT 'active',
    billing_cycle          TEXT NOT NULL DEFAULT 'monthly',
    current_period_start   TIMESTAMPTZ NOT NULL,
    current_period_end     TIMESTAMPTZ NOT NULL,
    cancel_at_period_end   BOOLEAN NOT NULL DEFAULT false,
    cancellation_reason    TEXT,
    cancellation_feedback  TEXT,
    stripe_subscription_id TEXT UNIQUE,
    stripe_customer_id     TEXT,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Support tickets (management DB — thin layer over external system)
CREATE TABLE support_tickets (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id),
    tenant_id   TEXT NOT NULL,
    external_id TEXT,
    external_url TEXT,
    subject     TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'open',
    priority    TEXT NOT NULL DEFAULT 'normal',
    category    TEXT,
    metadata    JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- NPC templates (management DB — platform-wide, not per-tenant)
CREATE TABLE npc_templates (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    system      TEXT NOT NULL DEFAULT '',
    category    TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    config      JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes
CREATE INDEX idx_users_tenant ON users(tenant_id);
CREATE INDEX idx_users_discord ON users(discord_id);
CREATE INDEX idx_users_google ON users(google_id);
CREATE INDEX idx_refresh_tokens_user ON refresh_tokens(user_id);
CREATE INDEX idx_refresh_tokens_expires ON refresh_tokens(expires_at);
CREATE INDEX idx_campaigns_tenant ON campaigns(tenant_id);
CREATE INDEX idx_provider_keys_tenant ON provider_keys(tenant_id);
CREATE INDEX idx_subscriptions_stripe ON subscriptions(stripe_subscription_id);
CREATE INDEX idx_support_tickets_user ON support_tickets(user_id);
CREATE INDEX idx_support_tickets_tenant ON support_tickets(tenant_id);
```

---

## Error Codes

Standardized error codes used across all endpoints:

| Code | HTTP Status | Description |
|------|-------------|-------------|
| `validation_error` | 400 | Request body failed validation |
| `invalid_json` | 400 | Malformed JSON in request body |
| `missing_field` | 400 | Required field missing |
| `unauthorized` | 401 | Missing or expired auth token |
| `forbidden` | 403 | Insufficient role/permissions |
| `not_found` | 404 | Resource not found |
| `conflict` | 409 | Resource already exists or constraint violated |
| `rate_limited` | 429 | Too many requests |
| `quota_exceeded` | 402 | Tenant usage quota exceeded |
| `payment_required` | 402 | Subscription inactive or past due |
| `provider_error` | 502 | Upstream provider (TTS/LLM/STT) failed |
| `gateway_error` | 502 | Gateway Admin API unreachable or returned error |
| `internal_error` | 500 | Unhandled server error |

---

## Open Design Questions

1. **Gateway communication:** HTTP client vs gRPC? The gateway currently only exposes
   HTTP. Adding gRPC would be more efficient but requires gateway changes. Recommend
   HTTP for MVP, gRPC later if latency becomes an issue.

2. **Cache layer:** Should the management service cache gateway responses (tenant data,
   session lists)? A Redis cache with short TTLs (5-30s) would reduce gateway load
   but adds operational complexity. Defer to Phase 2 unless load requires it.

3. **Webhook reliability:** Stripe webhooks need guaranteed delivery. Use a `webhook_events`
   table for idempotent processing with status tracking (`pending`, `processed`, `failed`).

4. **Tenant provisioning flow:** When a new user signs up via OAuth2 and creates a
   subscription, the full flow is: create user → create subscription → create tenant
   (via gateway) → create campaign. Should this be a single transactional endpoint
   or separate steps? Recommend a `POST /onboarding` orchestration endpoint.

5. **Multi-tenant NPC access:** The management service needs to read/write NPC data
   across tenant schemas. Options: (a) connect with a superuser role that can switch
   schemas, (b) maintain a connection pool per tenant schema. Recommend (a) for
   simplicity, with schema name validated against the tenant registry.
