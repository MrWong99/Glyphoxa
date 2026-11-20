# Postgres migration tooling: goose with embedded SQL migrations

Schema migrations use **goose** (`github.com/pressly/goose/v3`) with plain SQL migration files embedded into the binary via `embed.FS`. Versioning is a **sequential, zero-padded integer** scheme (`00001_init.sql`, `00002_add_kg_edges.sql`, …) living in `internal/storage/migrations/`. Migrations are applied through goose's library API from inside the single binary — never a separate migration toolchain.

## What this decides

- **Tool:** goose v3.
- **Versioning:** sequential integer prefixes, one concern per file, up + down in the same file via `-- +goose Up` / `-- +goose Down`.
- **Location:** `internal/storage/migrations/*.sql`, embedded with `//go:embed`.
- **Running across modes (ADR-0005):** a `glyphoxa migrate` subcommand (`up`/`down`/`status`/`version`); plus auto-apply at startup in `all` Mode only. `web` and `voice` Modes never auto-migrate — they assume the schema is current.

## Why goose, not the alternatives

The discriminating constraint comes from decisions already made, not from taste. ADR-0008 (Postgres KG with `tsvector` fulltext) and ADR-0011 (pgvector + a **partial HNSW index** on non-null embeddings, plus `CREATE EXTENSION vector`) require raw Postgres DDL that ORM- and declarative-schema tools do not model cleanly:

- **Atlas (declarative) / Ent** — fight custom index types (HNSW), partial-index predicates, and extension lifecycle. The schema would constantly drift from what these tools think they can express, and we'd be writing escape-hatch raw SQL anyway. Rejected: they add an abstraction that our own schema decisions defeat.
- **golang-migrate** — applies our raw DDL fine and **takes a Postgres advisory lock by default** (a real point in its favour given multi-instance startup, see below). Rejected on Go-native ergonomics: it is CLI/driver-shaped, its library API is thinner, it has no first-class Go-function migrations, and its "dirty version" failure state is a recurring operational wart. goose's typed-error model and `Provider` API read better from inside our binary.
- **sqlc + plain SQL** — not actually a migration tool. sqlc is query codegen that *consumes* an existing schema; it still needs goose or golang-migrate to *apply* migrations. Whether v2 adopts sqlc/pgx for the query layer is a task #5 (schema impl) decision and is deliberately left open here.

goose wins on fit with the methodology: a single Go dependency, plain reviewable SQL files (small diffs — Methodology notes), embedding into the one binary we already ship (consistent with the embedded Silero model and buf-generated code), and an escape hatch to Go-function migrations (`*.go` in the same dir) for the rare data backfill that SQL can't express — notably the embedding-model backfill ADR-0011 anticipates.

## Concurrency across `all`/`web`/`voice`

Multiple instances can start at once (a scaled `web` + several `voice` Instances). goose does **not** lock by default — concurrent `Up` calls would race. We therefore construct the provider with a session locker:

```go
// Deterministic int64 lock ID derived from a fixed constant so every
// instance contends on the same Postgres advisory lock.
const migrationsLockID int64 = 0x6778_6d69_6772 // "gxmigr"

locker, _ := lock.NewPostgresSessionLocker(lock.WithLockID(migrationsLockID))
provider, _ := goose.NewProvider(goose.DialectPostgres, db, migrationsFS,
    goose.WithSessionLocker(locker))
```

This acquires a Postgres advisory lock for the duration of the migrate run, so simultaneous starts serialize safely. This reuses the same Postgres coordination substrate ADR-0005 already established for `voice_sessions` claiming — no new infrastructure.

Operationally:

- **`all` Mode (self-host default):** runs `migrate up` (locked) at startup before serving. Zero-ceremony for the single-box deployment that is the v1.0 target.
- **`web` / `voice` Modes (scale path):** do **not** auto-migrate. The deploy runs `glyphoxa migrate up` once as a pre-deploy job; the serving instances assume a current schema and fail fast (via goose `status`/version check) if it is behind. This keeps schema changes a deliberate, observable deploy step rather than an emergent race between N booting processes.

**Why:** the whole DB layer is greenfield — v1 shipped no migrations and no real persistence at all (one of its concrete failures, ADR-0007), so there is nothing to port and no reason to inherit a heavier tool. goose gives plain SQL we can review line-by-line, runs from the single binary the modes already share, and locks correctly when configured — at the cost of one mandatory configuration step (the session locker) that this ADR makes non-optional.

## Amendment: out-of-order migrations are applied, and "current" means none pending (2026-11-20)

Migration numbers are chosen by whoever opens the PR, and parallel PRs merge in any order — so a database can already be at 00058 when 00056 first reaches it. Two defaults made that dangerous. goose refused the lower-numbered migration outright ("found missing migrations"), bricking `migrate up`. And `EnsureCurrent` compared only the highest applied version against the highest embedded one, so a `web`/`voice` process (which never auto-migrates) booted as "current" against a schema that was missing a migration.

**Decided:** the provider is built with `goose.WithAllowOutofOrder(true)`, so a not-yet-applied lower-numbered migration is simply applied; and `EnsureCurrent` fails when *any* embedded migration is unapplied, naming them. The cost is a discipline that already held in practice: a migration must not depend on the relative order of migrations from other, concurrently open PRs. `migrate down` still rolls back the highest applied version.
