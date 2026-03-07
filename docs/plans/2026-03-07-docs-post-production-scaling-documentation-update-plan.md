---
title: "docs: Post-Production-Scaling Documentation Update"
type: docs
status: completed
date: 2026-03-07
---

# Post-Production-Scaling Documentation Update

## Overview

Phases 0-5 of the production scaling plan have been implemented, introducing
multi-tenant architecture, gateway/worker binary modes, Kubernetes deployment
via Helm, gRPC transport, observability, usage/quota tracking, campaign
export/import, and tier-aware node scheduling. The codebase has changed
substantially but most documentation still describes the original
single-process, single-tenant architecture.

This plan covers updating every affected document so that a new developer,
self-hoster, or SaaS operator can understand and deploy the current system.

## Problem Statement

A developer reading the README, architecture docs, or deployment guide today
will see a single-process application with Docker Compose as the only
deployment method. The following are undocumented:

- Four binary modes (`--mode=full|gateway|worker|mcp-gateway`)
- Multi-tenant model (TenantContext, schema-per-tenant, campaign_id)
- Gateway subsystem (admin API, BotManager, SessionOrchestrator, usage/quota)
- Session subsystem (SessionRuntime, WorkerHandler)
- gRPC transport between gateway and workers
- Kubernetes/Helm deployment (3 deployments, worker Job template, NetworkPolicies, HPA, tier-aware scheduling)
- Per-tenant metrics labels (`tenant_id`, `campaign_id`)
- Campaign export/import

## Proposed Solution

Update existing documents in-place. Create one new document
(`docs/multi-tenant.md`) for the gateway/admin API/tenant lifecycle since this
is too large to fold into existing pages. No other new documents.

---

## Phase 1: High-Priority Updates

These documents are the most visible and most stale.

### 1.1 README.md

**Current state:** Shows single-process architecture diagram, project structure
missing 6+ packages, no mention of binary modes or Kubernetes.

- [x] Add "Deployment Modes" section after Features listing the four `--mode` values with one-line descriptions
- [x] Update architecture diagram to show gateway/worker topology (can be a second diagram below the existing single-process one, labelled "Distributed Mode")
- [x] Update Project Structure tree to include `internal/gateway/`, `internal/session/`, `internal/observe/`, `internal/health/`, `internal/resilience/`, `internal/feedback/`, `deploy/helm/`
- [x] Add row to Documentation table for the new `docs/multi-tenant.md`
- [x] Update Quick Start to mention `--mode=full` is the default (no change needed for existing users)
- [x] Update test count if it has changed

**Reference:** Current README at `README.md:1-181`

### 1.2 docs/deployment.md

**Current state:** Covers Docker Compose, binary releases, build from source.
No mention of Kubernetes, Helm, or `--mode` flags.

- [x] Add "Binary Modes" section near the top explaining `--mode=full` (default, self-hosted), `--mode=gateway`, `--mode=worker`, `--mode=mcp-gateway`
- [x] Add "Kubernetes / Helm Deployment" section covering:
  - Prerequisites (cluster, node pools, Sealed Secrets controller)
  - `helm install` for shared topology with `values-shared.yaml`
  - Dedicated tenant provisioning with `values-dedicated.yaml`
  - Key `values.yaml` parameters (gateway replicas, worker profiles, resource limits, ports)
  - Tier-aware node scheduling (shared/dedicated/GPU node pools)
  - NetworkPolicy topology (what can talk to what)
  - Worker Job lifecycle (created by gateway, TTL cleanup)
  - HPA configuration
- [x] Add "Environment Variables" subsection for gateway/worker mode env vars (`GLYPHOXA_ADMIN_KEY`, `GLYPHOXA_GRPC_ADDR`, `GLYPHOXA_GATEWAY_ADDR`, `GLYPHOXA_DATABASE_DSN`, `GLYPHOXA_MCP_GATEWAY_URL`)
- [x] Link to new `docs/multi-tenant.md` for admin API and tenant management

**Reference:** Current deployment doc at `docs/deployment.md:1-60+`, Helm chart at `deploy/helm/glyphoxa/`

### 1.3 docs/multi-tenant.md (NEW)

**Current state:** Does not exist.

- [x] Create `docs/multi-tenant.md` covering the gateway/admin API and multi-tenant model
- [x] Sections:
  - Overview of multi-tenant architecture (schema-per-tenant shared, dedicated instance)
  - TenantContext and how it flows through the system
  - Admin API endpoints (tenant CRUD, session management)
  - Bot management (BotManager, multi-bot, ring-based token distribution)
  - Session orchestration (PostgreSQL-backed state, constraint enforcement, heartbeat)
  - Usage and quota tracking (session hour limits, quota guard)
  - Campaign export/import (.tar.gz format, what's included, how to restore)
  - BYOK (bring-your-own-key) — document as planned/not-yet-implemented
- [x] Document the gRPC contract between gateway and worker (reference `proto/glyphoxa/v1/`)
- [x] Document the local transport fallback for `--mode=full`

**Reference:** `internal/gateway/admin.go`, `internal/gateway/botmanager.go`, `internal/gateway/sessionorch/orchestrator.go`, `internal/gateway/usage/`, `internal/gateway/grpctransport/`, `internal/gateway/contract.go`

### 1.4 CLAUDE.md — Key Subsystems

**Current state:** Lists 8 subsystems. Missing gateway, session, observe,
health, resilience, feedback.

- [x] Add `internal/gateway/` entry — Multi-bot gateway, admin API, session orchestrator, gRPC transport, usage/quota tracking
- [x] Add `internal/session/` entry — SessionRuntime owns voice pipeline lifecycle, WorkerHandler for gRPC, shared by full and worker modes
- [x] Add `internal/observe/` entry — Prometheus metrics, OTel traces, HTTP middleware, per-tenant labels
- [x] Add `internal/health/` entry — Health probe system (`/healthz`, `/readyz`)
- [x] Add `internal/resilience/` entry — Circuit breaker, retry patterns
- [x] Update Core Data Flow to note that in gateway+worker mode, control signals go via gRPC while voice audio goes directly between worker and Discord

**Reference:** `CLAUDE.md:48-58`

---

## Phase 2: Architecture Documents

### 2.1 docs/architecture.md

**Current state:** Shows single-process architecture with detailed diagram.
Missing gateway/worker split entirely.

- [x] Add "Deployment Topologies" section after System Overview explaining full vs gateway+worker modes
- [x] Add a second architecture diagram showing the distributed topology: Gateway (admin API, bot management, session orchestration) -> gRPC -> Worker (VAD, STT, LLM, TTS, Mixer) -> Discord Voice
- [x] Add `internal/gateway/` and sub-packages to the Key Packages table
- [x] Add `internal/session/` to the Key Packages table
- [x] Add `internal/observe/` and `internal/health/` to the Key Packages table
- [x] Add `internal/resilience/` to the Key Packages table
- [x] Add `pkg/memory/export/` to the Key Packages table (campaign export)
- [x] Note that the existing single-process diagram remains accurate for `--mode=full`

**Reference:** `docs/architecture.md:1-60+`

### 2.2 docs/design/01-architecture.md

**Current state:** Describes architectural layers and data flow for
single-process. No mention of distributed mode.

- [x] Add "Distributed Topology" section after Architectural Layers table explaining gateway/worker split
- [x] Add gateway and session layers to the Architectural Layers table
- [x] Note that the existing data flow description applies to both `--mode=full` and `--mode=worker` (the voice pipeline is identical)
- [x] Add brief description of the gRPC control plane (session start/stop/heartbeat) vs the voice data plane (direct Discord connection from worker)

**Reference:** `docs/design/01-architecture.md:1-80`

### 2.3 docs/design/09-roadmap.md

**Current state:** Phases 1-5 marked complete. Phase 6 "Production Hardening
and Observability" marked "Next up" but many items are now implemented. Phases
7-8 also partially complete.

- [x] Phase 6: Mark completed items with checkmarks:
  - [x] OpenTelemetry integration (traces, metrics)
  - [x] Prometheus metrics endpoint
  - [x] Structured logging with slog
  - [x] Health endpoints (`/healthz`, `/readyz`)
  - [x] Circuit breakers (per-provider)
  - [x] Config hot-reload
  - [x] Provider failover (secondary provider auto-switch) — still TODO
  - [x] S2S -> cascaded fallback — still TODO
  - [x] Memory layer isolation / graceful degradation — still TODO
  - [x] Context window management / summarization
- [x] Phase 7: Mark completed items:
  - [x] Discord slash commands (full set implemented)
  - [x] Entity management commands
  - [x] Campaign management commands
  - [x] Session dashboard embed
  - [x] Voice commands
  - [x] Companion Web UI — still stretch/TODO
  - [x] Closed alpha program — still TODO
- [x] Phase 8: Mark completed items:
  - [x] Helm chart (Kubernetes deployment)
  - [x] Container images (multi-arch Docker)
  - [x] Performance optimization from alpha data — still TODO
  - [x] WebRTC production-ready — still TODO
  - [x] Game system expansion — still TODO
- [x] Update Phase 6 status from "Next up" to partially complete
- [x] Add note about production scaling plan (`docs/plans/2026-03-05-...`) covering the multi-tenant architecture that was implemented across all phases

**Reference:** `docs/design/09-roadmap.md:277-398`

---

## Phase 3: Secondary Updates

### 3.1 docs/configuration.md

- [x] Add section for the `--mode` CLI flag (not a YAML field but critical for operation)
- [x] Document gateway-mode environment variables (`GLYPHOXA_ADMIN_KEY`, `GLYPHOXA_GRPC_ADDR`, etc.)
- [x] Document worker-mode environment variables (`GLYPHOXA_GATEWAY_ADDR`, `GLYPHOXA_MCP_GATEWAY_URL`)
- [x] Note that per-tenant configuration is managed via the gateway admin API (link to `docs/multi-tenant.md`)

**Reference:** `docs/configuration.md:1-50`, `cmd/glyphoxa/main.go`

### 3.2 docs/observability.md

- [x] Add "Multi-Tenant Labels" section documenting that metrics and traces include `tenant_id` and `campaign_id` attributes when running in gateway/worker mode
- [x] Add example PromQL queries with `by (tenant_id)` filtering
- [x] Add note about Grafana dashboard `$tenant_id` template variable
- [x] Document the `/metrics` endpoint path and port (observe port, default 9090)

**Reference:** `internal/observe/metrics.go`, `internal/observe/trace.go`

### 3.3 docs/README.md (docs index)

- [x] Add `docs/multi-tenant.md` to the Operations section of the Documentation Index table
- [x] Update `docs/deployment.md` description from "Docker Compose, building from source, GPU setup, production checklist" to include "Kubernetes / Helm"
- [x] Update `docs/observability.md` description to mention "per-tenant metrics"

**Reference:** `docs/README.md:1-84`

### 3.4 docs/getting-started.md

- [x] Add brief note in "Build & Run" section that `--mode=full` is the default and is the right choice for single-process / self-hosted deployments
- [x] Add "Next Steps" callout at the end pointing to `docs/deployment.md` for Kubernetes and `docs/multi-tenant.md` for SaaS operation

**Reference:** `docs/getting-started.md`

### 3.5 docs/design/08-open-questions.md

- [x] Review all open questions and mark any resolved by Phases 0-5 (admin API auth model, migration tooling, session state management, etc.)

**Reference:** `docs/design/08-open-questions.md`

### 3.6 TODOS.md

- [x] Review items and mark/remove any completed by Phases 0-5

**Reference:** `TODOS.md`

---

## Acceptance Criteria

### Content Completeness

- [x] A new developer reading README -> architecture -> getting-started can understand both single-process and distributed deployment models
- [x] A SaaS operator reading deployment -> multi-tenant can deploy a gateway+worker cluster from scratch using Helm
- [x] A contributor reading CLAUDE.md has an accurate package map of all subsystems
- [x] The roadmap accurately reflects what is implemented vs planned
- [x] All documentation index pages link to `docs/multi-tenant.md`

### Quality Gates

- [x] No broken internal links between documents
- [x] Architecture diagrams are consistent between README, docs/architecture.md, and docs/design/01-architecture.md
- [x] `--mode=full` is clearly documented as the default with no breaking changes for existing users
- [x] `make check` passes (no code changes, but verify nothing was accidentally modified)

---

## References

### Key Source Files

- Entry point with mode flag: `cmd/glyphoxa/main.go`
- Gateway admin API: `internal/gateway/admin.go`
- Bot manager: `internal/gateway/botmanager.go`
- Session orchestrator: `internal/gateway/sessionorch/orchestrator.go`
- Usage/quota: `internal/gateway/usage/quota_guard.go`
- gRPC transport: `internal/gateway/grpctransport/client.go`, `server.go`
- Session runtime: `internal/session/runtime.go`
- Worker handler: `internal/session/worker_handler.go`
- Observability: `internal/observe/metrics.go`, `trace.go`, `middleware.go`
- Health: `internal/health/health.go`
- Circuit breaker: `internal/resilience/circuitbreaker.go`
- Helm chart: `deploy/helm/glyphoxa/`
- Proto definitions: `proto/glyphoxa/v1/`

### Production Scaling Plan

- `docs/plans/2026-03-05-feat-production-scaling-multi-tenant-deployment-plan.md`
