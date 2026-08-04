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

## Amendment: format v2 — the world epic #533 added (2026-08-04, #547)

Every new table in an epic is a place where "we exported the campaign" quietly stops being true, and a silent omission in a backup is worse than a missing feature: it is discovered only when someone restores. Format **v2** closes that for #533.

**What v2 adds.** Aspects and Tags on a Node; Note and Disposition on an Edge; the `maps` section (each Map carrying its Pins nested, since a Pin has no meaning apart from its Map); the `boards` section; and `appearances` inside each history Session.

**v1 still imports.** Every addition is `omitempty`, so a v1 bundle IS a valid v2 bundle with those sections absent — `CheckVersion` accepts the range `[MinSupportedVersion, FormatVersion]` rather than an exact match. A backup format that refuses last year's backup is not a backup format.

**Map images are opt-in, separately from history.** `?include_images=true` (and `--include-images` on the CLI) embeds the bytes as base64. Off by default because the blob cap is 32 MiB *per image* and base64 inflates by a third — a campaign with a handful of scans would turn a setup export into something nobody can mail. Without the flag a Map still round-trips completely: name, nesting, anchor, privacy, and every Pin. Losing the picture is not losing the map. The flag is deliberately separate from `include_history`: a GM may want their maps in a share bundle without the transcripts, or the transcripts without the megabytes.

**Appearances follow the history flag**, because an Appearance is a record of what was *said*, not part of the campaign's setup — and it is derived, so a destination that re-indexes gets them back anyway. An appearance whose Node ref does not resolve is dropped rather than fatal, for the same reason.

**Import invariants the new sections needed:**

- **Maps import in two passes.** `parent_map_id` is self-referential with a composite FK, and bundle order is the source's `ListMaps` order — alphabetical by name. A single pass would be refused outright the first time a child sorted before its parent, so "The Vault inside Saltmarsh" would import as two top-level maps or not at all. Pass 1 creates every map flat; pass 2 applies the nesting; pins come last, since they need both a map and a node.
- **A fresh blob key per imported map.** The source key names the *source* tenant (`t/<tenant>/map/…`), so reusing it would let one tenant's import overwrite another tenant's picture.
- **Blob writes are outside the transaction and are swept by hand.** `blob.NewPostgres` runs on its own pool, so a `Put` is *not* rolled back when the import transaction is. Import tracks every key it writes and deletes them when the transaction fails; without that, a failed import would strand bytes with no row naming them and no sweep that would ever find them. This is the one place the all-or-nothing property is enforced by code rather than by the database.
- **Unknown refs stay fatal.** A map anchored to a node not in the bundle, a pin on an unknown entry, a board entry that resolves to nothing — each fails the whole import, the same all-or-nothing discipline v1 applies to edge endpoints.

**Secrets exclusion is unaffected.** None of the new tables carry credentials, and the bundle structs remain the allowlist: the exporter still populates fields explicitly and never reflects over tables. The one non-SQL read the export seam gained (`ReadMapImage`) resolves a key the row already stores, through the ADR-0048 seam, and lives on the adapter rather than on `*storage.Store` — storage deliberately knows nothing about blobs.

**Size, and the bound that actually binds.** The import cap is `MaxImportBytes` (192 MiB compressed), deliberately larger than `blob.MaxSize`. `blob.MaxSize` caps ONE image; an images-included bundle carries several, base64-inflated by a third and only partly recovered by gzip. Capping the whole upload at the per-image limit made an export this build can *produce* impossible to import through the only import path — a backup that cannot be restored, which is the one failure mode a backup format may not have. A setup-only bundle stays kilobytes; the flag is where the size goes.

**The orphan sweep runs detached.** The most likely way an images-included import fails is the client disconnecting or timing out mid-transaction — which cancels the request context. A sweep inheriting it would refuse every `Delete` and strand exactly the bytes it exists to clean up, so it runs on `context.WithoutCancel` with its own short budget.

**Known and accepted:** a hand-edited bundle can describe a parent cycle (A inside B, B inside A). `UpdateMap` rejects only a direct self-parent and the schema permits cycles, so such a bundle imports into a campaign with no root map and a truncated breadcrumb. The same state is reachable through the product UI, so this is not a bundle-specific hole; it is recorded here rather than silently tolerated.
