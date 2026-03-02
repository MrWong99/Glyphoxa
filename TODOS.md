# TODOS — Glyphoxa Code Audit

Audit date: 2026-03-02
Scope: voice receiver → memory → mixer → audio output, checked against `docs/design/`.

---

## 1. ~~[MEDIUM] No format negotiation between TTS and mixer~~ DONE

**Files:** `internal/agent/npc.go`, `pkg/audio/mixer/mixer.go`, `pkg/audio/discord/connection.go`

The mixer accepts `AudioSegment` values with arbitrary `SampleRate` and `Channels`, passes them as `AudioFrame` to the output callback, and the Discord `sendLoop` converts them to 48kHz stereo via `FormatConverter`. However, the `FormatConverter` uses `sync.Once` for warning logs, meaning after the first mismatch warning it goes silent for all subsequent format mismatches from different NPCs.

In a session with multiple NPCs at different TTS sample rates (e.g., 22050Hz for Coqui, 16000Hz for ElevenLabs), only the first NPC's mismatch is logged. If one NPC's format causes issues, debugging the second NPC's format problems requires removing the `sync.Once`.

**Fix:** Replaced `sync.Once` with `sync.Map` keyed by `(rate, channels)` pair — now logs once per unique source format.

---

## 2. ~~[MEDIUM] STT keyword boosting not propagated to active sessions~~ DONE

**Files:** `internal/app/session_manager.go:304-306`

`PropagateEntity()` includes a comment: _"STT keyword boosting and phonetic index are logged but not yet wired through agents; providers that support mid-session keyword updates will be integrated in a future release."_ Entity names added mid-session are persisted to the knowledge graph but never reach the STT provider's keyword hint list.

**Impact:** STT accuracy for newly introduced entity names (NPCs joining mid-session, new locations discovered) doesn't benefit from keyword boosting. Only entities known at session start are boosted via the transcript correction pipeline.

**Fix:** `PropagateEntity()` now rebuilds the keyword list from graph entities and calls `audioPipeline.UpdateKeywords()` so new STT sessions pick up the updated keywords.

---

## 3. ~~[MEDIUM] Cascade engine doesn't handle tool call results in the audio stream~~ DONE

**Files:** `internal/engine/cascade/cascade.go`

The cascade engine passes tools to the **strong model** (`buildStrongPrompt` includes `tools`), and `OnToolCall` is stored, but the `forwardSentences()` helper only processes `chunk.Text` — it never checks for or handles tool call chunks. If the strong model invokes a tool, the tool call response never reaches the TTS text channel, and the tool result is never fed back to the model.

**Impact:** MCP tool calls from cascade-mode NPCs silently fail. The strong model might request a tool, but the response is dropped, leading to incomplete or hallucinated continuations.

**Fix:** In `forwardSentences()`, detect tool-call chunks from the LLM stream. When a tool call is detected, invoke the registered `toolHandler`, feed the result back to the strong model as a follow-up message, and resume forwarding sentences to TTS.

---

## 4. ~~[LOW] Sentence boundary detection is simplistic~~ DONE

**Files:** `internal/engine/cascade/cascade.go` — `firstSentenceBoundary()`

The cascade engine splits sentences at `.`, `!`, or `?` followed by whitespace. This breaks on:
- Abbreviations: "Dr. Smith" → split after "Dr."
- Decimal numbers: "The scroll costs 2.5 gold" → split after "2."
- Ellipses: "But then..." → split after first "."
- Quoted speech: `"Run!" she shouted.` → split after "Run!"

**Impact:** The cascade's opener sentence may be cut short at an abbreviation, producing an awkward TTS playback and a confusing forced prefix for the strong model.

**Fix:** Use a heuristic that skips single-letter + period patterns (abbreviations), checks for digit contexts (decimals), and handles ellipses. Or use a lightweight sentence tokeniser library.

---

## 5. ~~[LOW] Hot-context assembler doesn't include pre-fetched cold-layer results~~ DONE

**Files:** `internal/hotctx/assembler.go`, `internal/hotctx/prefetch.go`

The `HotContext` struct has a `PreFetchResults` field for cold-layer (GraphRAG) results, and a `PreFetcher` exists in `internal/hotctx/prefetch.go`. But `Assembler.Assemble()` never calls the prefetcher or populates `PreFetchResults`. The prefetcher is standalone and not integrated into the assembly pipeline.

**Impact:** The cold layer (semantic retrieval from past sessions) is never injected into NPC prompts. NPCs can only reference the L1 recency window and L3 identity/scene data — not semantically relevant past conversations.

**Fix:** Add the `PreFetcher` as an optional dependency of `Assembler`. When present, run it as a fourth concurrent goroutine in `Assemble()` and populate `PreFetchResults`. Then include those results in `FormatSystemPrompt()`.

---

## 6. ~~[LOW] `FormatSystemPrompt` doesn't include PreFetchResults~~ DONE

**Files:** `internal/hotctx/assembler.go`

Even if `PreFetchResults` were populated (see TODO #14), the `FormatSystemPrompt` function doesn't format them into the system prompt string. The function only handles identity, recent transcript, and scene context.

**Fix:** Add a section to `FormatSystemPrompt` that formats `PreFetchResults` (entity name + content + relevance score) into the system prompt when present.

---

## 7. ~~[LOW] Entity upsert at startup doesn't create relationships~~ DONE

**Files:** `internal/app/app.go` (entity registration), `internal/entity/store.go`

NPCs are registered as knowledge graph entities at startup, but only as bare nodes (entity + attributes). No relationships (LOCATED_AT, KNOWS, QUEST_GIVER, etc.) are created from the NPC config. The design doc shows NPCs connected to locations, factions, and other entities via typed edges.

**Impact:** Scene context building (`buildSceneContext`) finds no LOCATED_AT relationships, so NPCs have no location awareness. Quest tracking and faction membership are empty.

**Fix:** Added `Relationships []RelationshipConfig` to `NPCConfig`. `registerNPCEntities()` now creates relationships (with bidirectional support and name-based target resolution) in a second pass after all entities exist.

---

## 8. ~~[LOW] `SpeakText` doesn't write transcript entries~~ DONE

**Files:** `internal/agent/npc.go` — `SpeakText()`

`HandleUtterance` records the exchange in `a.messages` (in-memory history) and the engine emits transcript entries. `SpeakText` records to `a.messages` but **doesn't write to the engine's transcript channel** or the session store. DM-puppet speech (narration, direct NPC speech via commands) is lost from the persistent transcript.

**Impact:** DM-initiated NPC speech doesn't appear in session history. Consolidation, summarisation, and future context retrieval miss these entries.

**Fix:** After TTS synthesis, write a `TranscriptEntry` to the session store (requires passing the store or a write callback to the agent). Alternatively, add a method on the engine to emit a synthetic transcript entry.

---

## 9. ~~[LOW] `wg.Go` (Go 1.25+) used without Go version gate~~ NOT AN ISSUE

**Files:** `internal/engine/cascade/cascade.go:221`, `internal/engine/s2s/engine.go:240`, `internal/app/audio_pipeline.go:132`

`sync.WaitGroup.Go()` was added in Go 1.25. The `go.mod` specifies Go 1.26 so this is currently fine, but it's a portability note: anyone trying to build with Go < 1.25 will get a compile error with no clear message about the minimum version requirement.

**Impact:** Non-issue — `go.mod` requires Go 1.26, which satisfies the Go 1.25+ requirement.

---

## Summary

| # | Severity | Area | Issue |
|---|----------|------|-------|
| 1 | ~~MEDIUM~~ | ~~Audio~~ | ~~FormatConverter warning suppression across NPCs~~ |
| 2 | ~~MEDIUM~~ | ~~Pipeline~~ | ~~STT keyword boosting not propagated mid-session~~ |
| 3 | ~~MEDIUM~~ | ~~Engine~~ | ~~Cascade engine doesn't handle tool calls~~ |
| 4 | ~~LOW~~ | ~~Engine~~ | ~~Sentence boundary detection too simplistic~~ |
| 5 | ~~LOW~~ | ~~Context~~ | ~~Prefetcher not integrated into assembler~~ |
| 6 | ~~LOW~~ | ~~Context~~ | ~~FormatSystemPrompt ignores PreFetchResults~~ |
| 7 | ~~LOW~~ | ~~Entity~~ | ~~Startup entity registration creates no relationships~~ |
| 8 | ~~LOW~~ | ~~Agent~~ | ~~SpeakText doesn't write transcript entries~~ |
| 9 | ~~LOW~~ | ~~Build~~ | ~~wg.Go requires Go 1.25+ (non-issue, go.mod requires 1.26)~~ |
