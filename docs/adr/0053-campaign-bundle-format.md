# Campaign Bundle: versioned gzipped-JSON export with mandatory secrets exclusion

Epic 6's exporter, importer, and external-tool converters all implement one format. Decided with the operator 2026-07-07 (#287); the seven decision areas below are the format spec. The Go type skeleton (`internal/bundle`) lands with the exporter slice, hand-written from this ADR.

## What this decides

1. **Entity scope.** Core (always): the campaign row (name, System, Campaign Language), Agents (Persona, Voice JSONB **minus provider bindings**), Tool Grants, KG Nodes/Edges including the NPC-Node↔Agent link, and Characters (PCs, once #276 exists). **History (Voice Sessions + Transcript Lines/Chunks) is flag-gated, default off** — default export is "share/provision a campaign setup"; `--include-history` serves backup/migration. (Forward note, non-binding: a hosted offering may gate history export as a premium feature — the format must not preclude that split.)
2. **Secrets exclusion (mandatory).** `provider_config`, `deployment_config`, `users`/auth sessions, and credentials of any kind are never marshaled — the exporter builds the bundle from an explicit allowlist of fields, never by reflecting over tables. A test enforces the property **"no ciphertext/last4 bytes in any bundle"** against a seeded fixture.
3. **Embeddings: stripped.** Vectors are not exported; the destination's embedworker regenerates from `embedding NULL` after import. Simple, provider-safe (no model mismatch), costs one re-embed pass.
4. **ID semantics: always mint + remap.** The importer mints fresh UUIDs for every entity and remaps intra-bundle references. The same bundle imports twice as two independent campaigns; no collision semantics exist to define. Idempotent re-import/sync is explicitly a non-goal of v1.
5. **Butler merge rule.** ADR-0009's trigger auto-creates a Butler on campaign insert and a partial unique index forbids a second — the importer **UPDATEs the trigger-created Butler** from the exported one (Persona, Voice, Grants, name; renaming "Glyphoxa" is acceptable).
6. **Snowflake handling: verbatim.** Character `discord_user_id` and (when history is included) speaker/participant IDs are kept as exported. Cross-community imports rebind via the Players panel (#279) afterwards; an operator-supplied remap table is YAGNI until someone needs it.
7. **Packaging & transport.** A **gzipped JSON envelope**, single file (`<campaign>.glyphoxa.json.gz`), top-level `format_version` (integer, starts at 1), `exported_at`, and the campaign payload. Compatibility: import the same `format_version`; **refuse newer with a clear error**; older versions get explicit migration code or a refusal — never silent best-effort. Transport is **plain HTTP endpoints beside the SSE relay mount** (multipart upload for import, streamed download for export), operator-only auth posture (ADR-0041), request size cap aligned with ADR-0048's constants. **Import does not auto-activate the imported campaign** — the UI offers the switch.

## Considered and rejected

- **Proto-derived envelope over Connect** — bytes-over-Connect fights message-size limits and buys nothing for a file a human should be able to inspect; JSON keeps the "hand-write a tiny valid bundle" review property.
- **Carrying vectors with an embedding-model stamp** — import-on-model-match complexity for the price of one embedworker pass.
- **Preserving source UUIDs** — forces collision semantics and blocks import-as-copy, for a sync use case v1 doesn't have.
- **History always included** — bundles dominated by transcript bulk and third-party snowflakes by default.

## Relationship to other ADRs

ADR-0009 (Butler trigger the importer must merge with), ADR-0041 (operator-only transport auth), ADR-0011 (embedding regeneration path), ADR-0049 (import stays a synchronous RPC), ADR-0048 (size-cap constants), #276/#279 (Characters section and post-import rebinding), #289 (converters target this format).
