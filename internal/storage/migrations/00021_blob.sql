-- +goose Up

-- blob is the v1 object-storage backend (ADR-0048): binary payloads (Highlight
-- clips, generated images, future audio extracts / export bundles) as bytea in
-- the shared Postgres, behind the internal/blob Store seam. Every ADR-0034
-- deployment shape already shares Postgres, so a Voice Instance writes and a Web
-- Instance serves with no new configuration; the 32 MiB Put cap (a code
-- constant) keeps a bytea backend honest.
--
-- key is the tenant-scoped path t/<tenant_id>/<owner-kind>/<owner-id>/<name>;
-- tenant_id is a plain REFERENCES tenant(id) — DELIBERATELY no owner FKs and no
-- ON DELETE CASCADE. Deletion goes through the seam (blob.Delete), not FK
-- cascade, because a cascade would only ever work for the Postgres backend; a
-- hook survives the S3 backend swap (ADR-0048 lifecycle).
CREATE TABLE blob (
    key          text PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenant (id),
    content_type text NOT NULL,
    size         bigint NOT NULL,
    bytes        bytea NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX blob_tenant_key_idx ON blob (tenant_id, key);

-- +goose Down

DROP TABLE IF EXISTS blob;
