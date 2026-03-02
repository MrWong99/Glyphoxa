---
title: "Wire Semantic Index (L2) and GraphRAG Query Paths"
type: fix
status: active
date: 2026-03-02
todos: [5, 6, 8, 14, 15]
prerequisites: [1, 2]
---

# Wire Semantic Index (L2) and GraphRAG Query Paths

## Overview

The Postgres implementations for SemanticIndex (L2 vector search) and GraphRAGQuerier (graph-augmented retrieval) are fully built and tested, but no production code path calls them. This plan wires both subsystems into the consolidation pipeline, hot-context assembler, and MCP memory tools. Resolves TODOS #5, #6, #8, #14, and #15.

## Problem Statement

NPCs currently have no semantic search capability and no graph-augmented retrieval. The entire L2 layer (embedding-based similarity search) is dead code. The `GraphRAGQuerier.QueryWithContext()` and `QueryWithEmbedding()` methods exist but are never called. The MCP `search_facts` tool is limited to FTS keyword matching. The PreFetcher does basic entity lookups only, and its results are never injected into NPC prompts.

## Prerequisites

**TODOS #1 and #2 must be fixed first.** L2 indexing depends on transcript entries flowing into L1 via `SessionStore.WriteEntry`. Without #1 (SessionManager wiring) and #2 (cascade engine transcript emission), the consolidator has nothing to chunk and embed. The code in this plan can be written and tested independently with mocks, but will produce no data in production until #1 and #2 are resolved.

## Proposed Solution

Four data flows to wire:

1. **Write path**: Consolidator -> chunk transcript entries -> embed via provider -> `SemanticIndex.IndexChunk()`
2. **Hot-context read path**: PreFetcher runs GraphRAG queries -> Assembler 4th goroutine -> FormatSystemPrompt renders results
3. **MCP tool read path**: `search_facts` upgraded with semantic search; new `search_graph` tool for entity-anchored GraphRAG queries
4. **Registration**: Memory tools registered with MCP host via `RegisterBuiltin()`

## Technical Approach

### Architecture

```
                    ┌─────────────────────────────────────────────┐
                    │              WRITE PATH                      │
                    │                                              │
  TranscriptEntry   │  Consolidator ─► Chunker ─► EmbedBatch()   │
  ──────────────►   │       │                         │            │
  (L1 SessionStore) │       │                         ▼            │
                    │       │              SemanticIndex.IndexChunk │
                    │       ▼                      (L2)            │
                    │  SessionStore.WriteEntry                     │
                    │       (L1, existing)                         │
                    └─────────────────────────────────────────────┘

                    ┌─────────────────────────────────────────────┐
                    │           HOT-CONTEXT READ PATH              │
                    │                                              │
                    │  Assembler.Assemble() runs 4 goroutines:     │
                    │    1. IdentitySnapshot (existing)            │
                    │    2. GetRecent transcript (existing)        │
                    │    3. buildSceneContext (existing)            │
                    │    4. PreFetcher.Retrieve() ─► GraphRAG      │
                    │                                ↓             │
                    │  FormatSystemPrompt() renders all 4 sections │
                    └─────────────────────────────────────────────┘

                    ┌─────────────────────────────────────────────┐
                    │           MCP TOOL READ PATH                 │
                    │                                              │
                    │  search_facts: embed query ─► L2 Search()   │
                    │                  + L1 FTS (existing)         │
                    │                  merged/ranked results        │
                    │                                              │
                    │  search_graph: QueryWithContext() or          │
                    │                QueryWithEmbedding()           │
                    │                scoped to NPC's visible graph  │
                    └─────────────────────────────────────────────┘
```

### Key Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Chunking strategy | One `Chunk` per `TranscriptEntry` | Simplest; maps cleanly to existing data model; avoids premature splitting complexity |
| Chunk.EntityID | `entry.NPCID` for NPC entries, empty for player | Enables NPC-scoped retrieval without full entity extraction |
| Chunk.ID | UUID | Matches `memory.Chunk.ID` type (string); deterministic hashing deferred |
| PreFetcher error isolation | Separate goroutine outside errgroup, own 40ms timeout | PreFetcher failure must not abort identity/transcript/scene fetches |
| PreFetcher query method | `QueryWithContext()` (FTS-based), NOT embedding in real-time path | Embedding calls add network latency; violates <50ms budget |
| Graph scope construction | `graph.Neighbors(npcID)` entity IDs | Scopes retrieval to NPC's known subgraph |
| Batch size limit | 512 texts per `EmbedBatch()` call | Stays within API limits; sequential batches with backoff |
| MCP tool strategy | Upgrade `search_facts` + add `search_graph` | Clear separation: content-level vs entity-anchored retrieval |
| Degradation when embeddings nil | Skip L2 entirely; FTS fallback everywhere | All code paths check `embedProvider != nil` before vector operations |

### Implementation Phases

#### Phase 1: Foundation — Store L2 reference and wire options

**Files:**
- `internal/app/app.go` — Add `semantic memory.SemanticIndex` field to `App` struct; add `WithSemanticIndex` option; store `store.L2()` in `initMemory`
- `internal/app/session_manager.go` — Add `semantic memory.SemanticIndex` field; accept via constructor/options

**Tasks:**
- [x] Add `semantic memory.SemanticIndex` field to `App` struct (`app.go:61`)
- [x] Add `WithSemanticIndex(s memory.SemanticIndex) Option` functional option (`app.go` near line 102)
- [x] Set `a.semantic = store.L2()` in `initMemory` (`app.go:209`, alongside L1 and graph)
- [x] Add `semantic memory.SemanticIndex` field to `SessionManager` struct (`session_manager.go:76`)
- [x] Thread `semantic` from App to SessionManager in `initSessions` or constructor
- [x] Add unit test for `WithSemanticIndex` option

**Acceptance criteria:** `App.semantic` is non-nil after `initMemory` when Postgres is configured. Mock-injectable in tests.

---

#### Phase 2: Chunking module

**Files:**
- `internal/session/chunker.go` (new)
- `internal/session/chunker_test.go` (new)

**Tasks:**
- [x] Create `Chunker` with `ChunkEntries(sessionID string, entries []memory.TranscriptEntry) []memory.Chunk`
- [x] Map each `TranscriptEntry` to one `memory.Chunk`: `ID` = UUID, `SessionID` = sessionID, `Content` = entry.Text, `SpeakerID` = entry.SpeakerID, `EntityID` = entry.NPCID (empty for player), `Timestamp` = entry.Timestamp
- [x] Skip entries with empty `Text` (silence markers, etc.)
- [x] Table-driven tests with `t.Parallel()` covering: single entry, multiple entries, empty text filtering, NPC vs player EntityID assignment

**Acceptance criteria:** `ChunkEntries` produces correct `[]memory.Chunk` with all fields populated. No embedding yet — that happens in Phase 3.

---

#### Phase 3: Consolidator L2 indexing (write path)

**Files:**
- `internal/session/consolidator.go` — Add `SemanticIndex`, `embeddings.Provider`, and `Chunker` to `ConsolidatorConfig`; add embedding step after L1 write
- `internal/session/consolidator_test.go` — Test L2 indexing path

**Tasks:**
- [x] Add `SemanticIndex memory.SemanticIndex`, `EmbedProvider embeddings.Provider` fields to `ConsolidatorConfig` (`consolidator.go:37-50`)
- [x] After existing L1 write in `consolidate()`, if both `SemanticIndex` and `EmbedProvider` are non-nil:
  1. Call `Chunker.ChunkEntries()` on the new entries
  2. Extract `Content` strings from chunks
  3. Call `EmbedProvider.EmbedBatch()` in batches of 512; assign resulting vectors to `chunk.Embedding`
  4. Call `SemanticIndex.IndexChunk()` for each chunk
- [x] If `EmbedProvider` is nil, skip L2 indexing entirely (graceful degradation)
- [x] If `EmbedBatch()` fails, log at Error level but do NOT block L1 write; advance `lastIndex` normally (L2 indexing is best-effort)
- [x] Thread `SemanticIndex` and `EmbedProvider` from `SessionManager` to consolidator config (`session_manager.go`)
- [x] Tests: mock embeddings provider + mock semantic index; verify `IndexChunk` called with correct embeddings; verify graceful degradation when provider is nil

**Acceptance criteria:** Consolidation cycle chunks+embeds+indexes new entries to L2. Nil provider skips L2 cleanly. Embedding failures don't block L1.

---

#### Phase 4: Enhance PreFetcher with GraphRAG

**Files:**
- `internal/hotctx/prefetch.go` — Add `GraphRAGQuerier` support via type assertion; add `Retrieve()` method returning `[]memory.ContextResult`
- `internal/hotctx/prefetch_test.go` — Test with `mock.GraphRAGQuerier`

**Tasks:**
- [ ] Add a `Retrieve(ctx context.Context, npcID string, transcript string) []memory.ContextResult` method to `PreFetcher`
- [ ] Inside `Retrieve()`:
  1. Type-assert `graph` to `memory.GraphRAGQuerier`; if fails, return nil (graceful degradation)
  2. Build `graphScope`: call `graph.Neighbors(npcID)` (or `VisibleSubgraph`) to get entity IDs within NPC's reach
  3. Call `ragQuerier.QueryWithContext(ctx, transcript, graphScope)` — FTS-based, no embedding needed
  4. Return results capped at 5 entries
- [ ] Do NOT call `QueryWithEmbedding` in the hot path — embedding adds network latency violating <50ms budget
- [ ] Tests: mock GraphRAGQuerier returning canned results; test type assertion fallback with plain KnowledgeGraph mock; test scope construction

**Acceptance criteria:** `Retrieve()` returns GraphRAG context results scoped to NPC's visible graph. Degrades to nil when graph doesn't implement `GraphRAGQuerier`.

---

#### Phase 5: Wire PreFetcher into Assembler

**Files:**
- `internal/hotctx/assembler.go` — Add `WithPreFetcher` option; run PreFetcher as isolated 4th goroutine in `Assemble()`
- `internal/hotctx/formatter.go` — Add `## Relevant Knowledge` section for `PreFetchResults`
- `internal/hotctx/assembler_test.go` — Test with and without PreFetcher

**Tasks:**
- [ ] Add `preFetcher *PreFetcher` field to `Assembler` struct
- [ ] Add `WithPreFetcher(pf *PreFetcher) Option` functional option
- [ ] In `Assemble()`, AFTER the errgroup block (not inside it), run PreFetcher with its own 40ms timeout:
  ```go
  if a.preFetcher != nil {
      pfCtx, pfCancel := context.WithTimeout(ctx, 40*time.Millisecond)
      defer pfCancel()
      hctx.PreFetchResults = a.preFetcher.Retrieve(pfCtx, npcID, lastTranscript)
  }
  ```
  Run concurrently with the errgroup (both start together, errgroup results are critical, PreFetcher is best-effort)
- [ ] In `FormatSystemPrompt` (`formatter.go`), add rendering after scene context and before transcript:
  ```
  ## Relevant Knowledge
  - **EntityName**: Content snippet...
  ```
  Cap at 5 results, truncate each Content to 500 chars. Skip section if `PreFetchResults` is nil/empty.
- [ ] Tests: assembler with mock PreFetcher; assembler without PreFetcher (backward compat); formatter with and without PreFetchResults
- [ ] Wire PreFetcher creation in `App.initHotContext` and `SessionManager.Start`, passing `a.graph`

**Acceptance criteria:** Hot-context assembly includes GraphRAG results when PreFetcher is configured. PreFetcher timeout/failure does not block or abort identity/transcript/scene assembly. NPC prompt includes `## Relevant Knowledge` section when results exist.

---

#### Phase 6: Wire MCP memory tools

**Files:**
- `internal/mcp/tools/memorytool/memorytool.go` — Accept and use `SemanticIndex` and `embeddings.Provider`; add `search_graph` tool
- `internal/mcp/tools/memorytool/memorytool_test.go` — Test semantic search and GraphRAG paths
- `internal/app/app.go` — Register built-in memory tools with MCP host

**Tasks:**
- [ ] Change `NewTools` signature to accept all dependencies:
  ```go
  func NewTools(sessions memory.SessionStore, index memory.SemanticIndex, graph memory.KnowledgeGraph, embedProvider embeddings.Provider) []tools.Tool
  ```
- [ ] In `search_facts` handler: if `embedProvider != nil && index != nil`, embed query via `embedProvider.Embed()`, call `index.Search()`, merge with existing FTS results from `sessions.Search()`. Rank by combining FTS relevance and vector distance. Fall back to FTS-only when embedding unavailable.
- [ ] Add new `search_graph` tool:
  - Parameters: `query` (string), `scope` (optional string[] of entity IDs)
  - Handler: type-assert `graph` to `GraphRAGQuerier`; if embeddings available, call `QueryWithEmbedding()`; else call `QueryWithContext()`. If graph is not `GraphRAGQuerier`, return error message.
  - Tier: STANDARD (est. 200-800ms)
- [ ] Register memory tools in `initMCP` (`app.go`):
  ```go
  memTools := memorytool.NewTools(a.sessions, a.semantic, a.graph, a.providers.Embeddings)
  for _, t := range memTools {
      a.mcpHost.RegisterBuiltin(t)  // type-assert to *mcphost.Host if needed
  }
  ```
- [ ] Tests: semantic search with mock embeddings + mock index; FTS fallback when embeddings nil; search_graph with mock GraphRAGQuerier; search_graph degradation with plain KnowledgeGraph

**Acceptance criteria:** `search_facts` uses vector search when embeddings available, falls back to FTS. `search_graph` performs graph-augmented retrieval. Memory tools are registered with MCP host at startup.

---

#### Phase 7: Integration test

**Files:**
- `internal/app/integration_test.go` or `internal/session/integration_test.go` (new, build-tagged)

**Tasks:**
- [ ] Write integration test exercising full round-trip: write TranscriptEntry to L1 -> trigger consolidation -> verify chunks in L2 via `SemanticIndex.Search()` -> verify GraphRAG `QueryWithContext()` returns relevant results
- [ ] Use mock embeddings provider (deterministic vectors) and in-memory or test-Postgres stores
- [ ] Verify graceful degradation: run same flow with nil embeddings provider, confirm L1 write succeeds and L2 is skipped

**Acceptance criteria:** Full pipeline from transcript write to semantic query works end-to-end. Degradation path verified.

## Graceful Degradation Matrix

| Condition | Write path (consolidator) | Hot-context (PreFetcher) | MCP tools |
|-----------|--------------------------|--------------------------|-----------|
| Embeddings provider nil | Skip L2 indexing; L1 only | N/A (uses FTS not embedding) | FTS fallback in search_facts; QueryWithContext in search_graph |
| SemanticIndex nil | Skip L2 indexing | N/A | FTS only in search_facts |
| Graph is not GraphRAGQuerier | N/A | Return nil (no GraphRAG results) | search_graph returns "not supported" |
| Embedding API error (transient) | Log error, skip L2 for this cycle, advance lastIndex | N/A | Return FTS-only results with warning |
| PreFetcher timeout (>40ms) | N/A | Return nil; log warn | N/A |
| Postgres unavailable | MemoryGuard handles (existing) | MemoryGuard handles | MemoryGuard handles |

## Dependencies & Risks

| Risk | Likelihood | Mitigation |
|------|-----------|------------|
| TODOS #1/#2 not fixed (no transcript entries flowing) | High — not yet in main | Plan can be coded/tested with mocks. Note prerequisite clearly. |
| Embedding API latency spikes during consolidation | Medium | Best-effort L2; failures don't block L1. Batch size capped at 512. |
| PreFetcher GraphRAG query exceeds 40ms on large datasets | Medium | Hard timeout + error isolation. GraphRAG results are optional enrichment. |
| Dimension mismatch if embedding model changes | Low | Document: changing models requires re-migration. Out of scope. |
| MCP host RegisterBuiltin not accessible via interface | Medium | Type-assert `a.mcpHost` to `*mcphost.Host`. May need interface extension. |

## Success Metrics

- [ ] L2 chunks table populated after consolidation cycle (verified via `SELECT count(*) FROM chunks`)
- [ ] `SemanticIndex.Search()` returns relevant results for query matching stored transcript content
- [ ] NPC prompt includes `## Relevant Knowledge` section with GraphRAG context when available
- [ ] `search_facts` MCP tool returns vector-ranked results alongside FTS results
- [ ] `search_graph` MCP tool returns entity-anchored results scoped to NPC's graph
- [ ] All degradation paths work: nil embeddings, nil semantic index, plain KnowledgeGraph (not GraphRAGQuerier)
- [ ] `make check` passes (fmt + vet + test with race detector)

## References

### Internal
- SemanticIndex interface: `pkg/memory/store.go:359-369`
- GraphRAGQuerier interface: `pkg/memory/store.go:463-489`
- Postgres SemanticIndex impl: `pkg/memory/postgres/semantic_index.go`
- Postgres GraphRAG impl: `pkg/memory/postgres/knowledge_graph.go:504-639`
- Postgres Store (L1/L2/L3 accessors): `pkg/memory/postgres/store.go:38-90`
- Embeddings provider interface: `pkg/provider/embeddings/provider.go:21-49`
- Consolidator: `internal/session/consolidator.go`
- PreFetcher: `internal/hotctx/prefetch.go`
- Assembler: `internal/hotctx/assembler.go`
- FormatSystemPrompt: `internal/hotctx/formatter.go:22-56`
- Memory tools: `internal/mcp/tools/memorytool/memorytool.go`
- MCP host RegisterBuiltin: `internal/mcp/mcphost/builtin.go:44-68`
- App struct and initMemory: `internal/app/app.go:54-216`
- SessionManager: `internal/app/session_manager.go:53-79`
- Mocks: `pkg/memory/mock/mock.go`, `pkg/provider/embeddings/mock/mock.go`

### Design Docs
- Memory design: `docs/design/03-memory.md`
- Knowledge graph design: `docs/design/10-knowledge-graph.md`
- MCP tools: `docs/mcp-tools.md`
