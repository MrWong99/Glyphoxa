# TODOS — Glyphoxa Code Audit

Audit date: 2026-03-02
Scope: voice receiver → memory → mixer → audio output, checked against `docs/design/`.

---

## 1. [CRITICAL] SessionManager never records transcripts to memory

**Files:** `internal/app/session_manager.go`, `internal/app/app.go`

The standalone `App` path (`internal/app/app.go:438`) calls `recordTranscripts()` to drain each engine's `Transcripts()` channel and write entries to the `SessionStore` via `WriteEntry`. The Discord-based `SessionManager` — which is the actual production entry point — **never wires this up**. The `Transcripts()` channel from every engine goes unread.

**Impact:** In Discord sessions, L1 session memory is never written. The hot-context assembler's `GetRecent()` always returns empty. Consolidation has nothing to consolidate. NPCs have no conversational memory.

**Fix:** After creating NPC agents in `SessionManager.Start()`, spawn a goroutine per agent that drains `agent.Engine().Transcripts()` and calls `sm.sessionStore.WriteEntry()`. Ensure these goroutines are tracked in the session's closer list so they're shut down on `Stop()`.

---

## 2. [CRITICAL] Cascade engine never emits transcript entries

**Files:** `internal/engine/cascade/cascade.go`

The cascade engine allocates a `transcriptCh` (line 150) and exposes it via `Transcripts()` (line 283), but **never writes to it**. No `Process()` path sends a `memory.TranscriptEntry` to the channel. Compare with the S2S engine (`internal/engine/s2s/engine.go:297-315`), which correctly forwards provider transcripts.

**Impact:** Even if transcript recording is wired up (see TODO #1), cascade-mode NPCs produce zero transcript entries. Memory, consolidation, and hot context all remain empty for cascade NPCs.

**Fix:** In `Process()`, after the fast model returns and again after the strong model completes, write `TranscriptEntry` values (with speaker, text, timestamp) to `e.transcriptCh`. The opener text should be combined with the strong-model continuation into a single entry to avoid duplicates.

---

## 3. [HIGH] Consolidator uses noop summariser — no real summarisation

**Files:** `internal/app/session_manager.go:166-169`

The `SessionManager` creates the consolidator with `&noopSummariser{}` which returns empty strings. The design doc (`docs/design/03-memory.md`) specifies L1→L2 chunking and L2→L3 entity extraction during consolidation. Currently the consolidator just re-writes raw entries without any summarisation, embedding, or entity extraction.

**Impact:** Conversation history is never compressed. The context window fills with raw transcript entries instead of summaries. No automatic entity extraction or semantic indexing occurs.

**Fix:** Replace `noopSummariser` with `session.NewLLMSummariser(llmProvider)` using the configured LLM. Long-term, wire in the full L1→L2→L3 pipeline (chunking + embedding + entity extraction) as described in the design.

---

## 4. [HIGH] Transcript correction pipeline created but never used

**Files:** `internal/app/app.go:69,160`

The `App` creates a `transcript.Pipeline` at startup (`a.pipeline = transcript.NewPipeline()`) but with no options — both the phonetic matcher and LLM corrector are `nil`. More importantly, the pipeline is never called anywhere. The audio pipeline routes raw STT output directly to the orchestrator without correction.

The design doc specifies a two-stage correction: phonetic matching for fantasy names, then LLM-based correction for low-confidence words.

**Impact:** Fantasy names (NPCs, locations, spells) are consistently mangled by STT. "Eldrinax" might arrive as "Elder Nax" with no correction.

**Fix:** Wire `CorrectionPipeline` into `audioPipeline.collectAndRoute()` between the STT final and orchestrator routing. Pass entity names from the knowledge graph as the dictionary for phonetic matching. Configure the LLM corrector when an LLM provider is available.

---

## 5. [HIGH] Semantic index (L2) is never populated or queried

**Files:** `pkg/memory/store.go` (interface), `internal/mcp/tools/memorytool/memorytool.go:210`

The `SemanticIndex` interface is fully defined with `IndexChunk()` and `Search()`, and the memory tool function signature accepts it (`NewTools(sessions, _ memory.SemanticIndex, graph)`), but the parameter is **ignored** (underscore). No code path ever calls `IndexChunk()` to populate the vector store, and no code path calls `SemanticIndex.Search()`.

**Impact:** The entire L2 layer (embedding-based similarity search) is dead code. NPCs cannot recall semantically relevant past conversations beyond the L1 recency window. The `GraphRAGQuerier.QueryWithEmbedding()` path is also unreachable since no embeddings are ever stored.

**Fix:** During consolidation (or as a post-write hook on SessionStore), chunk transcript entries, embed them via the configured embedding provider, and call `IndexChunk()`. Wire the semantic index into the memory MCP tools and/or hot-context prefetcher.

---

## 6. [HIGH] GraphRAG query paths are not wired into any production code

**Files:** `pkg/memory/store.go`, `pkg/memory/postgres/knowledge_graph.go`

The `GraphRAGQuerier` interface defines `QueryWithContext()` (FTS-based) and `QueryWithEmbedding()` (vector-based). The Postgres implementation exists, but neither method is called from the hot-context assembler, the MCP memory tools, or the consolidator. The design doc (`docs/design/10-knowledge-graph.md`) describes GraphRAG as the primary retrieval mechanism for NPCs.

**Impact:** NPCs cannot retrieve context-augmented knowledge graph information during conversations. The knowledge graph is only used for identity snapshots and scene context, not for semantic retrieval.

**Fix:** Integrate `GraphRAGQuerier.QueryWithContext()` (or `QueryWithEmbedding()` when embeddings are available) into the hot-context prefetcher (`internal/hotctx/prefetch.go`) as a cold-layer retrieval step. Also wire it into the MCP memory tool's search handler.

---

## 7. [MEDIUM] No format negotiation between TTS and mixer

**Files:** `internal/agent/npc.go`, `pkg/audio/mixer/mixer.go`, `pkg/audio/discord/connection.go`

The mixer accepts `AudioSegment` values with arbitrary `SampleRate` and `Channels`, passes them as `AudioFrame` to the output callback, and the Discord `sendLoop` converts them to 48kHz stereo via `FormatConverter`. However, the `FormatConverter` uses `sync.Once` for warning logs, meaning after the first mismatch warning it goes silent for all subsequent format mismatches from different NPCs.

In a session with multiple NPCs at different TTS sample rates (e.g., 22050Hz for Coqui, 16000Hz for ElevenLabs), only the first NPC's mismatch is logged. If one NPC's format causes issues, debugging the second NPC's format problems requires removing the `sync.Once`.

**Fix:** Consider per-format-pair warnings or a debug-level log on every conversion.

---

## 8. [MEDIUM] MCP memory tool ignores SemanticIndex parameter

**Files:** `internal/mcp/tools/memorytool/memorytool.go:210`

```go
func NewTools(sessions memory.SessionStore, _ memory.SemanticIndex, graph memory.KnowledgeGraph) []tools.Tool {
```

The `SemanticIndex` parameter is discarded. The memory search tool only uses `SessionStore.Search()` (keyword/FTS) and `KnowledgeGraph` methods. NPCs with MCP tool access cannot perform semantic searches over past conversations.

**Impact:** The `search_memory` tool is limited to keyword matching. An NPC trying to recall "what did the player say about the cursed artifact" can only do exact keyword search, not semantic similarity.

**Fix:** Accept and use the `SemanticIndex` in the search tool handler. When an embedding provider is available, embed the query and call `SemanticIndex.Search()` alongside the FTS path, then merge/rank results.

---

## 9. [MEDIUM] STT keyword boosting not propagated to active sessions

**Files:** `internal/app/session_manager.go:304-306`

`PropagateEntity()` includes a comment: _"STT keyword boosting and phonetic index are logged but not yet wired through agents; providers that support mid-session keyword updates will be integrated in a future release."_ Entity names added mid-session are persisted to the knowledge graph but never reach the STT provider's keyword hint list.

**Impact:** STT accuracy for newly introduced entity names (NPCs joining mid-session, new locations discovered) doesn't benefit from keyword boosting. Only entities known at session start could potentially be boosted (if boosting were wired at all — see TODO #4).

**Fix:** When `PropagateEntity()` adds an entity, iterate active STT sessions and call a keyword boost API (provider-dependent). At minimum, update the phonetic matcher's dictionary so the correction pipeline (once wired) can fix these names.

---

## 10. [MEDIUM] Orchestrator address detection uses basic keyword matching

**Files:** `internal/agent/orchestrator/address.go`

The address detector determines which NPC a player is speaking to by checking if the transcript contains the NPC's name (case-insensitive substring match). The design doc (`docs/design/06-npc-agents.md`) describes a more sophisticated approach: pronoun resolution, gaze direction (when available), conversation history context, and LLM-assisted disambiguation.

**Impact:** "Hey, tell me about the quest" when two NPCs are present defaults to the first NPC in the list. Players must explicitly say NPC names. Pronoun references ("ask her") and contextual addressing ("what about that thing you mentioned?") don't route correctly.

**Fix:** Implement at least conversation-history-based routing: if the player was just talking to NPC A, route ambiguous follow-ups to NPC A. For higher accuracy, add an LLM-based address classifier as described in the design.

---

## 11. [MEDIUM] Cascade engine doesn't handle tool call results in the audio stream

**Files:** `internal/engine/cascade/cascade.go`

The cascade engine passes tools to the **strong model** (`buildStrongPrompt` includes `tools`), and `OnToolCall` is stored, but the `forwardSentences()` helper only processes `chunk.Text` — it never checks for or handles tool call chunks. If the strong model invokes a tool, the tool call response never reaches the TTS text channel, and the tool result is never fed back to the model.

**Impact:** MCP tool calls from cascade-mode NPCs silently fail. The strong model might request a tool, but the response is dropped, leading to incomplete or hallucinated continuations.

**Fix:** In `forwardSentences()`, detect tool-call chunks from the LLM stream. When a tool call is detected, invoke the registered `toolHandler`, feed the result back to the strong model as a follow-up message, and resume forwarding sentences to TTS.

---

## 12. [MEDIUM] App.recordTranscripts races on context cancellation

**Files:** `internal/app/app.go:449-466`

The `recordTranscripts` goroutine uses a `select` on `ctx.Done()` and the `Transcripts()` channel. When the context is cancelled, the goroutine exits immediately — but the engine may still have buffered transcript entries in its channel. These are lost.

**Impact:** The last few transcript entries before shutdown are silently dropped. Over long sessions this is minor, but for short sessions it could mean significant context loss.

**Fix:** After `ctx.Done()` fires, drain remaining entries from the channel before returning. The engine's `Close()` already waits for background goroutines and then closes the channel, so a simple `for entry := range ch` after context cancellation would catch stragglers.

---

## 13. [LOW] Sentence boundary detection is simplistic

**Files:** `internal/engine/cascade/cascade.go` — `firstSentenceBoundary()`

The cascade engine splits sentences at `.`, `!`, or `?` followed by whitespace. This breaks on:
- Abbreviations: "Dr. Smith" → split after "Dr."
- Decimal numbers: "The scroll costs 2.5 gold" → split after "2."
- Ellipses: "But then..." → split after first "."
- Quoted speech: `"Run!" she shouted.` → split after "Run!"

**Impact:** The cascade's opener sentence may be cut short at an abbreviation, producing an awkward TTS playback and a confusing forced prefix for the strong model.

**Fix:** Use a heuristic that skips single-letter + period patterns (abbreviations), checks for digit contexts (decimals), and handles ellipses. Or use a lightweight sentence tokeniser library.

---

## 14. [LOW] Hot-context assembler doesn't include pre-fetched cold-layer results

**Files:** `internal/hotctx/assembler.go`, `internal/hotctx/prefetch.go`

The `HotContext` struct has a `PreFetchResults` field for cold-layer (GraphRAG) results, and a `PreFetcher` exists in `internal/hotctx/prefetch.go`. But `Assembler.Assemble()` never calls the prefetcher or populates `PreFetchResults`. The prefetcher is standalone and not integrated into the assembly pipeline.

**Impact:** The cold layer (semantic retrieval from past sessions) is never injected into NPC prompts. NPCs can only reference the L1 recency window and L3 identity/scene data — not semantically relevant past conversations.

**Fix:** Add the `PreFetcher` as an optional dependency of `Assembler`. When present, run it as a fourth concurrent goroutine in `Assemble()` and populate `PreFetchResults`. Then include those results in `FormatSystemPrompt()`.

---

## 15. [LOW] `FormatSystemPrompt` doesn't include PreFetchResults

**Files:** `internal/hotctx/assembler.go`

Even if `PreFetchResults` were populated (see TODO #14), the `FormatSystemPrompt` function doesn't format them into the system prompt string. The function only handles identity, recent transcript, and scene context.

**Fix:** Add a section to `FormatSystemPrompt` that formats `PreFetchResults` (entity name + content + relevance score) into the system prompt when present.

---

## 16. [LOW] Entity upsert at startup doesn't create relationships

**Files:** `internal/app/app.go` (entity registration), `internal/entity/store.go`

NPCs are registered as knowledge graph entities at startup, but only as bare nodes (entity + attributes). No relationships (LOCATED_AT, KNOWS, QUEST_GIVER, etc.) are created from the NPC config. The design doc shows NPCs connected to locations, factions, and other entities via typed edges.

**Impact:** Scene context building (`buildSceneContext`) finds no LOCATED_AT relationships, so NPCs have no location awareness. Quest tracking and faction membership are empty.

**Fix:** Extend the campaign config / entity YAML to include relationship definitions. During startup entity registration, also call `graph.AddRelationship()` for each configured relationship.

---

## 17. [LOW] `SpeakText` doesn't write transcript entries

**Files:** `internal/agent/npc.go` — `SpeakText()`

`HandleUtterance` records the exchange in `a.messages` (in-memory history) and the engine emits transcript entries. `SpeakText` records to `a.messages` but **doesn't write to the engine's transcript channel** or the session store. DM-puppet speech (narration, direct NPC speech via commands) is lost from the persistent transcript.

**Impact:** DM-initiated NPC speech doesn't appear in session history. Consolidation, summarisation, and future context retrieval miss these entries.

**Fix:** After TTS synthesis, write a `TranscriptEntry` to the session store (requires passing the store or a write callback to the agent). Alternatively, add a method on the engine to emit a synthetic transcript entry.

---

## 18. [LOW] `wg.Go` (Go 1.25+) used without Go version gate

**Files:** `internal/engine/cascade/cascade.go:221`, `internal/engine/s2s/engine.go:240`, `internal/app/audio_pipeline.go:132`

`sync.WaitGroup.Go()` was added in Go 1.25. The `go.mod` specifies Go 1.26 so this is currently fine, but it's a portability note: anyone trying to build with Go < 1.25 will get a compile error with no clear message about the minimum version requirement.

**Impact:** Minor — just a compatibility note.

---

## Summary

| # | Severity | Area | Issue |
|---|----------|------|-------|
| 1 | CRITICAL | Memory | SessionManager doesn't record transcripts |
| 2 | CRITICAL | Engine | Cascade engine never emits transcript entries |
| 3 | HIGH | Memory | Consolidator uses noop summariser |
| 4 | HIGH | Pipeline | Transcript correction pipeline unused |
| 5 | HIGH | Memory | Semantic index (L2) never populated or queried |
| 6 | HIGH | Memory | GraphRAG query paths not wired |
| 7 | MEDIUM | Audio | FormatConverter warning suppression across NPCs |
| 8 | MEDIUM | MCP | Memory tool ignores SemanticIndex |
| 9 | MEDIUM | Pipeline | STT keyword boosting not propagated mid-session |
| 10 | MEDIUM | Agent | Address detection is basic keyword matching |
| 11 | MEDIUM | Engine | Cascade engine doesn't handle tool calls |
| 12 | MEDIUM | Memory | Transcript recording races on shutdown |
| 13 | LOW | Engine | Sentence boundary detection too simplistic |
| 14 | LOW | Context | Prefetcher not integrated into assembler |
| 15 | LOW | Context | FormatSystemPrompt ignores PreFetchResults |
| 16 | LOW | Entity | Startup entity registration creates no relationships |
| 17 | LOW | Agent | SpeakText doesn't write transcript entries |
| 18 | LOW | Build | wg.Go requires Go 1.25+ (currently fine) |
