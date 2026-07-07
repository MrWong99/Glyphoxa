# Blob storage seam: Postgres bytea behind a Store interface in v1

No blob/object storage exists today — Postgres holds text and vectors, and the only binary-over-API precedent is the inline PreviewVoice WAV. Consumers are queuing up: Epic 8's Highlight clips and generated images (hard requirement), ADR-0011's deferred audio extracts, and large export bundles. Decided with the operator 2026-07-07 (#283); this ADR settles storage once so Epic 8's persistence slice asks no architecture questions.

## What this decides

- **A seam interface in a new `internal/blob` package**: `Put(ctx, key, contentType string, r io.Reader, size int64) error`, `Get(ctx, key) (io.ReadCloser, Meta, error)`, `Delete(ctx, key) error`. Signatures are streaming even where a backend buffers, so backends can change without touching callers.
- **Keys are tenant-scoped paths**: `t/<tenant_id>/<owner-kind>/<owner-id>/<name>` (e.g. `t/…/highlight/…/clip.opus`). The tenant prefix is mandatory; the storage layer never accepts a key without one.
- **The v1 backend is Postgres bytea** (`blob` table: key PK, tenant_id FK, content_type, size, bytes, created_at). Rationale: the Helm shape runs voice and web as **separate Deployments with no shared filesystem** (RWX PVCs are not assumed), but every ADR-0034 deployment shape already shares Postgres — so a Voice Instance writes and a Web Instance serves with zero new configuration, in compose, systemd, and Helm alike. v1 consumers are small (1–2 Opus clips plus images per session).
- **A per-blob size cap, enforced at `Put`** (code constant, 32 MiB to start). The cap is what keeps a bytea backend honest; anything that needs more (video) forces the backend conversation instead of silently bloating the DB.
- **S3-compatible storage is the documented growth path**, added behind the same seam when video or scale demands it. A local-filesystem backend is explicitly **not** built — it serves no deployment shape the project actually has.
- **Lifecycle: deletion goes through the seam, not FK cascade.** When an owning row (Highlight, Voice Session, Campaign) is deleted, the storage layer calls `Delete` for its blobs as part of the same operation. Deliberate: FK `ON DELETE CASCADE` on the blob table would only ever work for the Postgres backend; a hook survives the backend swap. Retention semantics for tape/candidate/Highlight blobs are ADR-0051's.

## Considered and rejected

- **Local filesystem as the self-host default** — the Helm split shape can't reach it without RWX volumes; two of three deployment shapes would need different plumbing than the third.
- **Filesystem + S3 dual backends now** — builds and tests two backends before the first consumer exists; MinIO would become a de-facto self-host dependency.
- **Bytea columns directly on owning tables** (no seam) — every future consumer re-decides storage, and the S3 migration path would touch every owner.

## Relationship to other ADRs

ADR-0034 (deployment shapes the backend must serve), ADR-0039 (web/voice Mode split that rules filesystem out), ADR-0051 (retention/deletion semantics for Epic 8 blobs), ADR-0011 (deferred audio extracts become a future consumer).
