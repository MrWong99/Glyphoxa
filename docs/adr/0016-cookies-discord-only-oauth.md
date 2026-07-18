# Cookie sessions + Discord-only OAuth in v1.0

Auth uses a server-side `sessions` table `(id, user_id, created_at, last_seen_at, expires_at, ip, ua)` with an opaque token in `glyphoxa_session=<random>; HttpOnly; Secure; SameSite=Lax`. CSRF is double-submit (`X-CSRF-Token` header) for state-changing Connect calls.

OAuth in v1.0 is **Discord-only** — every GM is a Discord User by construction (per ADR-0003), and Google/GitHub raise account-linking complexity for no v1.0 win. Login screen shows Google/GitHub slots disabled with a "coming soon" hint; wired for real in v1.5+.

API keys are bearer-only (`Authorization: Bearer glx_<random>`) for service accounts (CI, scripts), schema `api_keys (id, tenant_id, user_id, name, hash, last_four, scopes, created_at, last_used_at, expires_at)`. The API-key field is **dropped from the human login page** — service-account keys and human session cookies should age and revoke differently.

Tenant scoping uses an `X-Tenant-Id` header on every Connect call, validated server-side against `tenant_members.user_id`. Tenant switching is a DOM update, not a session rotation; two browser tabs can hold two tenants simultaneously.

~~First-time tenant flow is open-by-default — any authenticated Discord user can create a Tenant on `/onboarding/create-tenant`. Operator config flag `GLYPHOXA_OPEN_TENANT_CREATION=false` disables open creation; falls back to admin-mediated provisioning where existing Tenant owners can create Tenants for other users.~~ **Superseded for the single-operator web tier by ADR-0041:** login is gated by the mandatory `GLYPHOXA_OPERATOR_IDS` allowlist, and Tenants are claimed-or-created only for allowlisted Operators. Open tenant creation may return with the multi-tenant tier. **It returns as ADR-0055's `open` Admission Mode (2026-07-18):** create-only provisioning, a mode switch rather than the `GLYPHOXA_OPEN_TENANT_CREATION` flag shape, and the allowlist re-scoped rather than retired.

**Why JWT is rejected:** revocation pain, XSS attack surface, refresh-token dance complexity. Server-side sessions revoke instantly with a row delete.
