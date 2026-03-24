---
title: "Web Management Service — Plan Index"
type: overview
status: draft
date: 2026-03-24
supersedes: docs/plans/2026-03-23-admin-web-ui-plan.md
---

# Web Management Service — Plan Overview

This folder contains the design plans for the **Glyphoxa Web Management Service** — a separate, independently deployable service that provides self-service management for Dungeon Masters, tenant administration, NPC configuration, billing, and observability.

The web management service supersedes the earlier "embedded admin web UI" plan (`docs/plans/2026-03-23-admin-web-ui-plan.md`). The key decision was to build a **separate service** rather than embedding a web UI in the gateway.

---

## Plan Documents

| # | Document | Description |
|---|----------|-------------|
| 01 | [Architecture & Tech Stack](01-architecture.md) | Service architecture, tech stack (Go backend, Next.js frontend), deployment model, security boundaries. Defines the separation between web service and gateway. |
| 02 | [API Design](02-api-design.md) | REST API endpoints, authentication flows (Discord OAuth2, API key), request/response schemas, error handling. Wraps and extends the gateway's existing Admin API. |
| 03 | [Database Schema](03-database-schema.md) | PostgreSQL schema design in a dedicated `mgmt` schema. Tables for users, subscriptions, sessions, audit logs. Same PostgreSQL instance as gateway, cross-schema foreign keys. |
| 04 | [Frontend Design](04-frontend-design.md) | Next.js frontend architecture, page designs, component hierarchy, interaction patterns. Self-service portal for non-technical DMs — no YAML, no CLI. |
| 05 | [Billing & Pricing](05-billing-pricing.md) | Subscription tiers, Stripe integration, quota enforcement, self-hosted vs SaaS billing model. |

## Supporting Research

| Document | Description |
|----------|-------------|
| [Pricing Models](pricing-models.md) | Market research on TTRPG AI tool pricing ($5-15/month expected range). |
| [Pricing Models Assessment](pricing-models-assessment.md) | Detailed assessment of pricing model options for Glyphoxa. |

---

## Key Design Decisions

1. **Separate service, not embedded** — The web management service runs as its own binary (`cmd/glyphoxa-web/`) with its own deployment. This avoids coupling the web UI lifecycle to the gateway and allows independent scaling.

2. **Same database, separate schema** — Uses the `mgmt` schema in the same PostgreSQL instance as the gateway. Cross-schema foreign keys maintain referential integrity without a second database.

3. **Discord OAuth2 for DM login** — DMs authenticate via Discord OAuth2. The web service maps Discord user IDs to tenants and campaigns.

4. **Gateway Admin API as backend** — The web service wraps the gateway's existing Admin API for tenant/session operations, adding authentication, rate limiting, and a user-friendly interface on top.

---

## Implementation Status

- **Backend MVP**: `cmd/glyphoxa-web/` and `internal/web/` — initial implementation merged (auth, tenant/campaign/NPC/session handlers, PostgreSQL store)
- **Frontend MVP**: `glyphoxa-web/` — Next.js with shadcn/ui and TanStack Query (initial scaffold)
- **Billing**: Not yet implemented
- **Deployment**: Not yet deployed to K3s
