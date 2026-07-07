# Background work: one minimal DB-backed job runner, not N bespoke loops

The only async worker today is `internal/embedworker` — a single-purpose in-process poll loop (claim-oldest-NULL, no retry bookkeeping, no dead-letter state). Three planned features raised the question again: recap generation (Epic 3), bundle import (Epic 6), and Highlight media enrichment (Epic 8). Decided with the operator 2026-07-07 (#284): settle async infrastructure once.

## What this decides

- **A minimal generic job runner**: one `job` table + one small package with a per-kind handler registry. Schema sketch:
  `job(id uuid PK, kind text, payload jsonb, status text CHECK IN ('pending','running','done','dead'), attempts int, max_attempts int, run_after timestamptz, leased_until timestamptz, last_error text, created_at, updated_at)`.
- **Claim semantics**: workers claim with `FOR UPDATE SKIP LOCKED` (oldest runnable first: `pending` with `run_after <= now()`, or `running` with an expired lease). Safe across `all`/`web`/`voice` replicas by construction — Postgres is the coordinator, same spirit as the `voice_sessions` claim.
- **Restart survival**: jobs are rows; a crashed worker's lease expires and the job is reclaimed. Semantics are at-least-once — handlers must be idempotent or tolerate re-execution (enrichment is: regenerating an image for the same Highlight is harmless).
- **Failure policy**: bounded retries with exponential backoff via `run_after`; after `max_attempts` the job goes to `dead` with `last_error` kept — visible, never silently retried forever, never hammering a provider.
- **Consumer mapping** (the part each epic points at instead of debating):
  - **Recap (Epic 3): synchronous RPC** — the operator clicks and waits seconds; nothing is lost on failure that a second click doesn't fix.
  - **Bundle import (Epic 6): synchronous RPC** — operator-initiated, size-capped (ADR-0053); same argument.
  - **Highlight media enrichment (Epic 8): the job runner** — the only consumer with a real durability need; it runs post-promotion with nothing to re-trigger it manually.
- **embedworker stays as-is in v1.** Migrating it onto the runner is possible later and deliberately not required now — it works, and churning it buys nothing this wave.

## Considered and rejected

- **Blessing bespoke loops as the house pattern** — legitimate, but Epic 8 alone would copy claim/retry/dead-letter logic at least twice (image + later sound generation), and every copy is a place the bookkeeping can be subtly wrong.
- **An external queue library/system (river, asynq, NATS…)** — a dependency and operational surface for what is, in v1, one consumer with modest throughput. Postgres is already the coordination plane everywhere else.
- **Everything async** — recap and import would gain polling UX and latency for zero durability benefit.

## Relationship to other ADRs

ADR-0039 (replica modes the claim must be safe across), ADR-0043 (the "rows are the source of truth, sweep on boot" spirit), ADR-0048 (enrichment results land through the blob seam), ADR-0046 (enrichment spend is metered; see #311).
