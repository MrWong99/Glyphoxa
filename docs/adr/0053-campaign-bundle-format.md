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
- **A fresh blob key per imported map that HAS one.** The source key names the *source* tenant (`t/<tenant>/map/…`), so reusing it would let one tenant's import overwrite another tenant's picture. A map the bundle carried no bytes for gets no key at all — see the amendment below.
- **Blob writes are outside the transaction and are swept by hand.** `blob.NewPostgres` runs on its own pool, so a `Put` is *not* rolled back when the import transaction is. Import tracks every key it writes and deletes them when the transaction fails; without that, a failed import would strand bytes with no row naming them and no sweep that would ever find them. This is the one place the all-or-nothing property is enforced by code rather than by the database.
- **Unknown refs stay fatal.** A map anchored to a node not in the bundle, a pin on an unknown entry, a board entry that resolves to nothing — each fails the whole import, the same all-or-nothing discipline v1 applies to edge endpoints.

**Secrets exclusion is unaffected.** None of the new tables carry credentials, and the bundle structs remain the allowlist: the exporter still populates fields explicitly and never reflects over tables. The one non-SQL read the export seam gained (`ReadMapImage`) resolves a key the row already stores, through the ADR-0048 seam, and lives on the adapter rather than on `*storage.Store` — storage deliberately knows nothing about blobs.

**Size, and the bound that actually binds.** The import cap is `MaxImportBytes` (192 MiB compressed), deliberately larger than `blob.MaxSize`. `blob.MaxSize` caps ONE image; an images-included bundle carries several, base64-inflated by a third and only partly recovered by gzip. Capping the whole upload at the per-image limit made an export this build can *produce* impossible to import through the only import path — a backup that cannot be restored, which is the one failure mode a backup format may not have. A setup-only bundle stays kilobytes; the flag is where the size goes.

**The orphan sweep runs detached.** The most likely way an images-included import fails is the client disconnecting or timing out mid-transaction — which cancels the request context. A sweep inheriting it would refuse every `Delete` and strand exactly the bytes it exists to clean up, so it runs on `context.WithoutCancel` with its own short budget.

**Known and accepted:** a hand-edited bundle can describe a parent cycle (A inside B, B inside A). `UpdateMap` rejects only a direct self-parent and the schema permits cycles, so such a bundle imports into a campaign with no root map and a truncated breadcrumb. The same state is reachable through the product UI, so this is not a bundle-specific hole; it is recorded here rather than silently tolerated.

## Amendment: the restore path has to be able to restore (2026-08-04)

Two ways the v2 image work left a bundle that could be produced but not honestly restored.

**Every composition that imports must carry the blob seam.** `glyphoxa seed -bundle` built its `bundle.PGStore` with a nil `Blobs` field, so an `-include-images` bundle failed at the first map with "no blob store configured" and rolled the whole import back. The flag's own output was importable only through the web UI — and the CLI is the natural place to restore a backup. It is now wired the same way the web path and `export -include-images` already were, over the same pool the rows go to. The transaction rolled back cleanly throughout, so this was a capability gap and never corruption; the rule it produces is that a *blob key is not the only thing an import needs* — a composition that can write rows but not bytes cannot restore a bundle, and should be wired rather than allowed to fail late.

**The blob key is a claim, so it is minted only when there are bytes.** An imageless import used to mint a key anyway, "so the row's shape is identical either way". The row then named bytes nobody wrote: `GET /api/v1/maps/{id}/image` 404s forever and the Maps tab drew the browser's broken-image glyph, with nothing to distinguish a lost picture from one that was simply never in the backup. The stated justification — that a later re-upload would land where the row already points — was not true: `ReplaceMapImage` mints its OWN key and repoints the row, deliberately, so the old bytes stay readable until the row moves. An imageless import now leaves `blob_key` empty, which is the state the rest of the system already expected from a keyless map (`ListCampaignMapBlobKeys` skips `blob_key = ''`).

Two consequences follow, and both are part of the decision:

- **The image mount 404s an empty key explicitly.** `blob.Get("")` is `ErrInvalidKey`, not `ErrNotFound`, so falling through to the seam would log an internal error and answer 500 for an ordinary, expected state.
- **Every other seam call that takes the row's key skips an empty one.** `DeleteMap` and `ReplaceMapImage`'s supersede-delete both pass `blob_key` straight to `blob.Delete`, and `Delete("")` is `ErrInvalidKey` — each would have warned about bytes it failed to reclaim that never existed, on the two most ordinary things a GM does to such a map. "Empty means no bytes" has to hold at every caller or it is not a state, just a bug that has not been reached yet.
- **`Map.has_image` is on the wire.** The client cannot infer "this map has no picture" from a failed image request without treating every transient failure as permanent, so the server says so and the Maps tab renders a "no image" surface — pins still drawn on it, since they are the part that survived. Every map created through the product has an image (`CreateMap` requires bytes, and a generated map applies through that same call), so an IMPORT is the only producer of this state — either from a bundle exported without `-include-images`, or from an images-included export whose source blob was already gone, which the exporter carries through as a map with no bytes.

**Known and accepted: rows minted before this fix keep their dangling keys.** Between #547 and this change, an imageless import through the web UI (where `Blobs` was wired) wrote maps whose `blob_key` names bytes nobody ever put there. Those rows still report `has_image = true` and still 404, so they still draw a broken image. No backfill ships: the window is about a day, the affected rows are indistinguishable from a genuine reconciliation gap without reading the blob table per row, and `ReplaceMapImage` repairs one the moment a GM re-uploads. It is recorded rather than silently tolerated.

## Amendment: format v3 — planning threads (2026-08-13, #592)

ADR-0062 made the Butler planning chat's threads full data citizens — persisted, cascade-deleted with the campaign — and named the debt that comes with that: a table a GM pours prep into that the bundle does not carry is an export blind spot discovered at restore time. Format **v3** pays it.

**What v3 adds.** The `planning_threads` section: each thread carrying its title and its Messages nested (a message has no meaning apart from its thread — the same rationale as a Map's Pins). Threads have **no ref key**: nothing in the bundle cross-references a thread, so there is nothing to remap and no id to mint a name for.

**Threads export unconditionally**, like Boards, not behind the history flag. A planning thread is the GM's *prep* content — prose only, since tool-call intermediates are deliberately never persisted (ADR-0062) — not a record of what was said at the table. The history flag exists to keep transcript bulk and third-party snowflakes out of a share bundle; a planning thread carries neither, and a "campaign setup" that omitted the GM's own planning notes would be the silent-omission genre this format exists to close.

**v1 and v2 bundles still import.** `planning_threads` is `omitempty`, so an older bundle IS a valid v3 bundle with the section absent — `CheckVersion` keeps accepting the range `[MinSupportedVersion, FormatVersion]`, the discipline the v2 bump established. A backup format that refuses last year's backup is not a backup format.

**Messages carry role + content only.** No ids, no timestamps: `seq` is **re-derived on import** by replaying the bundle's message order through `AppendPlanningMessage`, whose max+1 derivation reproduces a dense 1..n under the destination's own UNIQUE key. A role outside the two-value chat vocabulary (`user` / `assistant`) is a hard error that fails the whole import — the same all-or-nothing discipline as an unknown ref, and refusing it in the importer beats deferring the failure to the role CHECK constraint with a worse message.

**Chat Tool Grants are NOT exported.** ADR-0062's chat grants are disjoint `surface = 'chat'` rows beside the voice grants, seeded as trigger defaults rather than curated by the GM, and the bundle's grant paths (`ListToolGrants` on export, `CreateToolGrant` on import) read and write the voice surface only. The destination's own trigger seeds the chat defaults on campaign creation, so exporting them would only re-import what is already there — and a future where chat grants become GM-curated can add them as another omitempty section without breaking this one.
