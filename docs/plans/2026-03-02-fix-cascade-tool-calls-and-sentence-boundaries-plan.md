---
title: "fix: Cascade engine tool call handling and sentence boundary detection"
type: fix
status: completed
date: 2026-03-02
todos: 3, 4
---

# fix: Cascade engine tool call handling and sentence boundary detection

## Overview

Two issues in the cascade engine (`internal/engine/cascade/cascade.go`) prevent MCP tool calls from working and cause awkward TTS splits on abbreviations/decimals. Both live in the same file and affect the same streaming pipeline.

- **TODO #3**: `forwardSentences()` only processes `chunk.Text` — tool call chunks from the strong model are silently dropped. The registered `toolHandler` is never invoked.
- **TODO #4**: `firstSentenceBoundary()` splits at `.!?` + whitespace, breaking on abbreviations ("Dr. Smith"), decimals ("2.5 gold"), and ellipses ("But then...").

## Problem Statement

**Tool calls**: The strong model receives tools via `buildStrongPrompt()`, and `OnToolCall(handler)` stores `e.toolHandler`, but the `forwardSentences()` helper (lines 451-506) never inspects `chunk.ToolCalls` or reacts to `FinishReason == "tool_calls"`. When the strong model invokes a tool, the tool call is dropped, the tool result is never fed back, and the NPC produces an incomplete or hallucinated continuation.

**Sentence boundaries**: `firstSentenceBoundary()` (lines 508-522) uses a naive `.!?` + whitespace check. This affects both `collectFirstSentence()` (opener detection) and `forwardSentences()` (TTS chunking). False positives on abbreviations cause the opener to be cut short ("Dr." instead of "Dr. Smith greets you warmly."), producing awkward TTS playback and a confusing forced prefix for the strong model.

## Proposed Solution

### Part 1: Tool call handling in the dual-model path

Replace the single `forwardSentences()` call in the background goroutine (line 258) with a tool-call-aware loop that:

1. Forwards text chunks to TTS (existing behavior).
2. Accumulates `ToolCalls` across chunks (streaming providers split arguments across chunks).
3. When `FinishReason == "tool_calls"`: flushes buffered text to TTS, invokes `e.toolHandler` for each accumulated tool call, constructs follow-up messages with tool results, and re-calls `strongLLM.StreamCompletion()`.
4. Loops with both an **iteration cap** (default 5, configurable via `WithMaxToolIterations`) and a **wall-clock deadline** derived from context.

### Part 2: Improved sentence boundary heuristic

Replace `firstSentenceBoundary()` with a smarter heuristic that skips:
- Common abbreviations (pattern-based, not a fixed list)
- Decimal numbers (digit.digit)
- Ellipses (ASCII `...` and Unicode `\u2026`)

## Technical Approach

### 1. Tool call accumulation

Add an `accumulateToolCalls` helper that merges `ToolCall` fragments across streaming chunks:

```go
// accumulateToolCalls.go (or inline in cascade.go)

// accumulateToolCalls merges incremental ToolCall chunks into complete calls.
// Streaming providers (OpenAI) send partial Arguments strings across chunks
// with the same index position. This function concatenates Arguments by index.
func accumulateToolCalls(existing []llm.ToolCall, incoming []llm.ToolCall) []llm.ToolCall {
    for i, tc := range incoming {
        if i < len(existing) {
            // Merge: concatenate Arguments, prefer non-empty ID/Name.
            if tc.ID != "" {
                existing[i].ID = tc.ID
            }
            if tc.Name != "" {
                existing[i].Name = tc.Name
            }
            existing[i].Arguments += tc.Arguments
        } else {
            existing = append(existing, tc)
        }
    }
    return existing
}
```

### 2. Modified `forwardSentences` → `forwardStrongModel`

Replace the single `forwardSentences()` call with a new `forwardStrongModel()` method that wraps the tool-call loop:

```go
// internal/engine/cascade/cascade.go

const defaultMaxToolIters = 5

// forwardStrongModel runs the strong model, forwarding text to TTS and handling
// tool calls in a loop. Returns the full concatenation of all text emitted.
func (e *Engine) forwardStrongModel(
    ctx context.Context,
    strongReq llm.CompletionRequest,
    textCh chan<- string,
    resp *engine.Response,
) string {
    var fullText strings.Builder

    for iter := range e.maxToolIters {
        ch, err := e.strongLLM.StreamCompletion(ctx, strongReq)
        if err != nil {
            resp.SetStreamErr(fmt.Errorf("cascade: strong model stream (iter %d): %w", iter, err))
            return fullText.String()
        }

        text, toolCalls, finished := e.forwardSentencesWithTools(ctx, ch, textCh, resp)
        fullText.WriteString(text)

        if finished || len(toolCalls) == 0 {
            // Normal completion or FinishReason != "tool_calls".
            return fullText.String()
        }

        // ── Execute tool calls ────────────────────────────────────────────
        e.mu.Lock()
        handler := e.toolHandler
        e.mu.Unlock()

        if handler == nil {
            slog.Warn("cascade: tool calls requested but no handler registered",
                "tool_count", len(toolCalls))
            return fullText.String()
        }

        // Build the assistant message that requested tools (required by LLM APIs).
        assistantMsg := llm.Message{
            Role:      "assistant",
            Content:   text,
            ToolCalls: toolCalls,
        }

        // Execute each tool call and build result messages.
        var toolResults []llm.Message
        for _, tc := range toolCalls {
            result, err := handler(tc.Name, tc.Arguments)
            if err != nil {
                // Feed error back to LLM so NPC can adapt.
                slog.Debug("cascade: tool call failed",
                    "tool", tc.Name, "error", err)
                result = fmt.Sprintf("Tool error: %s", err.Error())
            }
            toolResults = append(toolResults, llm.Message{
                Role:       "tool",
                Content:    result,
                ToolCallID: tc.ID,
            })
        }

        // Rebuild the request with tool results appended.
        strongReq.Messages = append(strongReq.Messages, assistantMsg)
        strongReq.Messages = append(strongReq.Messages, toolResults...)
    }

    slog.Warn("cascade: tool call iteration cap reached", "max", e.maxToolIters)
    return fullText.String()
}
```

### 3. Modified `forwardSentences` → `forwardSentencesWithTools`

Extend the existing `forwardSentences` to also accumulate and return tool calls:

```go
// forwardSentencesWithTools is like forwardSentences but also accumulates
// tool calls across chunks. Returns:
//   - text: full concatenation of all text chunks
//   - toolCalls: accumulated tool calls (empty if FinishReason != "tool_calls")
//   - finished: true if stream completed normally (no tool calls pending)
func (e *Engine) forwardSentencesWithTools(
    ctx context.Context,
    ch <-chan llm.Chunk,
    textCh chan<- string,
    resp *engine.Response,
) (text string, toolCalls []llm.ToolCall, finished bool) {
    var buf, collected strings.Builder
    var accum []llm.ToolCall

    for {
        select {
        case <-ctx.Done():
            return collected.String(), nil, true
        case chunk, ok := <-ch:
            if !ok {
                if buf.Len() > 0 {
                    select {
                    case textCh <- buf.String():
                    case <-ctx.Done():
                    }
                }
                return collected.String(), nil, true
            }

            // Accumulate tool calls across chunks.
            if len(chunk.ToolCalls) > 0 {
                accum = accumulateToolCalls(accum, chunk.ToolCalls)
            }

            // Forward text as before.
            if chunk.Text != "" {
                buf.WriteString(chunk.Text)
                collected.WriteString(chunk.Text)
            }

            // Eagerly flush complete sentences.
            for {
                s := buf.String()
                idx := firstSentenceBoundary(s)
                if idx < 0 {
                    break
                }
                sentence := s[:idx+1]
                rest := s[idx+1:]
                buf.Reset()
                buf.WriteString(strings.TrimLeft(rest, " \t\n\r"))
                select {
                case textCh <- sentence:
                case <-ctx.Done():
                    return collected.String(), nil, true
                }
            }

            // Terminal chunk.
            if chunk.FinishReason != "" {
                // Flush remaining text.
                if buf.Len() > 0 {
                    select {
                    case textCh <- buf.String():
                    case <-ctx.Done():
                    }
                }

                if chunk.FinishReason == "tool_calls" && len(accum) > 0 {
                    return collected.String(), accum, false
                }
                return collected.String(), nil, true
            }
        }
    }
}
```

### 4. Update the background goroutine in `Process()`

Replace lines 238-279 (the `e.wg.Go(func() { ... })` block in the dual-model path):

```go
e.wg.Go(func() {
    defer close(textCh)

    // Deliver the opener to TTS immediately.
    select {
    case textCh <- opener:
    case <-ctx.Done():
        return
    }

    // Run the strong model with tool-call loop.
    continuation := e.forwardStrongModel(ctx, strongReq, textCh, resp)
    fullText := opener
    if continuation != "" {
        fullText = opener + " " + continuation
    }

    // Emit transcript entries (unchanged).
    if playerMsg, ok := lastUserMessage(prompt.Messages); ok {
        e.emitTranscript(memory.TranscriptEntry{
            SpeakerID:   playerMsg.Name,
            SpeakerName: playerMsg.Name,
            Text:        playerMsg.Content,
            Timestamp:   time.Now(),
        })
    }
    e.emitTranscript(memory.TranscriptEntry{
        Text:      strings.TrimSpace(fullText),
        Timestamp: time.Now(),
    })
})
```

### 5. Improved `firstSentenceBoundary()`

Replace the current implementation with a heuristic that handles common false positives:

```go
// firstSentenceBoundary returns the index of the first sentence-ending
// punctuation (.!?) followed by whitespace, skipping common false positives:
//   - Abbreviations: single uppercase letter + period ("A. Smith")
//   - Common title abbreviations: "Dr.", "Mr.", "Mrs.", "Ms.", "St.", etc.
//   - Decimal numbers: digit + period + digit ("2.5 gold")
//   - Ellipses: "..." or Unicode "\u2026"
func firstSentenceBoundary(s string) int {
    for i := 0; i < len(s)-1; i++ {
        switch s[i] {
        case '.':
            if !isWhitespace(s[i+1]) {
                continue
            }
            if isEllipsis(s, i) {
                continue
            }
            if isDecimalDot(s, i) {
                continue
            }
            if isAbbreviation(s, i) {
                continue
            }
            return i
        case '!', '?':
            if !isWhitespace(s[i+1]) {
                continue
            }
            return i
        case '\u2026': // Unicode ellipsis — skip entirely.
            continue
        }
    }
    return -1
}

func isWhitespace(b byte) bool {
    return b == ' ' || b == '\n' || b == '\r' || b == '\t'
}

// isEllipsis checks if the period at index i is part of "..." (ASCII ellipsis).
func isEllipsis(s string, i int) bool {
    // Check for ".." preceding or following.
    if i >= 1 && s[i-1] == '.' {
        return true
    }
    if i+1 < len(s) && s[i+1] == '.' {
        return true
    }
    return false
}

// isDecimalDot checks if the period at index i is between digits (e.g., "2.5").
func isDecimalDot(s string, i int) bool {
    if i == 0 || i+1 >= len(s) {
        return false
    }
    return s[i-1] >= '0' && s[i-1] <= '9' && s[i+1] >= '0' && s[i+1] <= '9'
}

// isAbbreviation checks if the period at index i follows a likely abbreviation:
//   - Single uppercase letter: "A. Smith", "J. R. R. Tolkien"
//   - Common short abbreviations: "Dr.", "Mr.", "Mrs.", "Ms.", "St.", "Jr.",
//     "Sr.", "vs.", "Lt.", "Cpt.", "No."
func isAbbreviation(s string, i int) bool {
    if i == 0 {
        return false
    }

    // Single uppercase letter: "A."
    if i == 1 || (i >= 2 && (s[i-2] == ' ' || s[i-2] == '\n')) {
        c := s[i-1]
        if c >= 'A' && c <= 'Z' {
            return true
        }
    }

    // Common abbreviations: scan backwards from i to find the word start,
    // then check against the known set.
    wordStart := i - 1
    for wordStart > 0 && s[wordStart-1] != ' ' && s[wordStart-1] != '\n' {
        wordStart--
    }
    word := strings.ToLower(s[wordStart:i])

    switch word {
    case "dr", "mr", "mrs", "ms", "st", "jr", "sr", "vs",
        "lt", "cpt", "cmdr", "prof", "sgt", "gen", "col", "no":
        return true
    }
    return false
}
```

### 6. New functional options

```go
// cascade.go — options

// WithMaxToolIterations sets the maximum number of tool-call loop iterations
// before the cascade gives up and flushes. Defaults to 5.
func WithMaxToolIterations(n int) EngineOption {
    return func(e *Engine) { e.maxToolIters = n }
}
```

Add `maxToolIters int` to the `Engine` struct, default to `defaultMaxToolIters` in `New()`.

## Acceptance Criteria

### Functional Requirements

- [x] When the strong model requests tool calls, the registered `toolHandler` is invoked for each call
- [x] Tool results are fed back to the strong model as follow-up messages with correct `role: "tool"` and `ToolCallID`
- [x] The strong model's post-tool continuation is forwarded to TTS as sentence-level chunks
- [x] Multi-turn tool loops work (model requests tool A, gets result, requests tool B, gets result, then generates text)
- [x] Tool loop terminates after `maxToolIters` iterations
- [x] If `toolHandler` is nil when tool calls arrive, a warning is logged and text is flushed (no panic)
- [x] If `toolHandler` returns an error, the error message is fed back to the LLM as a tool result
- [x] `firstSentenceBoundary` does not split on "Dr. Smith", "2.5 gold", "But then...", or single-letter abbreviations
- [x] `firstSentenceBoundary` correctly splits on "Hello. How are you" and "Stop! Who goes there?"
- [x] Unicode ellipsis (`\u2026`) is not treated as a sentence boundary

### Non-Functional Requirements

- [x] No new external dependencies (heuristic only, no NLP library)
- [x] `firstSentenceBoundary` remains O(n) single-pass (no regex)
- [x] Tool call accumulation handles both single-chunk (Anthropic) and multi-chunk (OpenAI) streaming formats
- [x] All new code has `t.Parallel()` table-driven tests
- [x] Context cancellation cleanly exits the tool loop

### Quality Gates

- [x] `make check` passes (fmt + vet + test with -race)
- [x] Existing cascade tests continue to pass unchanged
- [x] New tests cover: tool-call single iteration, multi-iteration, nil handler, handler error, iteration cap, sentence boundary edge cases

## Dependencies & Prerequisites

- **LLM mock enhancement**: The existing mock (`pkg/provider/llm/mock/mock.go`) serves the same `StreamChunks` on every `StreamCompletion` call. Testing the tool loop requires different responses per call. Add a `StreamChunksSequence [][]llm.Chunk` field that pops the next entry per call, falling back to `StreamChunks` when exhausted.

## Implementation Phases

### Phase 1: Sentence boundary fix (TODO #4)

Lowest risk, no interface changes. Implement the improved `firstSentenceBoundary()` with helpers, add table-driven tests.

**Files:**
- `internal/engine/cascade/cascade.go` — replace `firstSentenceBoundary()`
- `internal/engine/cascade/cascade_test.go` — add sentence boundary test cases

### Phase 2: LLM mock enhancement

Add `StreamChunksSequence` support to the mock so the tool-call tests can configure different responses per `StreamCompletion` call.

**Files:**
- `pkg/provider/llm/mock/mock.go` — add `StreamChunksSequence` field

### Phase 3: Tool call handling (TODO #3)

Implement `accumulateToolCalls`, `forwardSentencesWithTools`, `forwardStrongModel`, update the `Process()` background goroutine, add `WithMaxToolIterations` option.

**Files:**
- `internal/engine/cascade/cascade.go` — new methods, modified `Process()`, new option
- `internal/engine/cascade/cascade_test.go` — tool call tests

## Known Limitations

- **No filler audio during tool execution**: When the strong model calls a tool, there's a silence gap between the pre-tool text and post-tool continuation. The opener audio fills part of this gap, but slow tools (>1s) will produce a noticeable pause. This is accepted as a v1 limitation.
- **Quoted speech splitting**: `"Halt!" she shouted.` still splits at `"Halt!"` — this is acceptable for TTS pacing and matches the cascade's intent of short, eagerly-flushed sentences.
- **English-centric abbreviations**: The abbreviation list covers common English and military titles. Fantasy-language abbreviations are not handled but can be added to the switch case.
- **`Response.ToolCalls` stays nil**: The cascade engine handles tools internally, so callers inspecting `resp.ToolCalls` will see nil. This matches the S2S engine's behavior.

## References

### Internal References

- `internal/engine/cascade/cascade.go` — all changes
- `internal/engine/cascade/cascade_test.go` — all new tests
- `pkg/provider/llm/provider.go:67-80` — `Chunk` type with `ToolCalls` field
- `pkg/provider/llm/types.go:21-31` — `ToolCall` type
- `internal/engine/engine.go:156-164` — `OnToolCall` interface contract
- `internal/mcp/bridge/bridge.go:98-111` — reference tool handler implementation
- `internal/agent/npc.go:157-171` — how `toolHandler` is registered by agents
- `pkg/provider/llm/anyllm/anyllm.go:193-199` — how anyllm accumulates tool calls from backends

### Related TODOs

- TODOS.md #3 (MEDIUM): Cascade engine doesn't handle tool call results
- TODOS.md #4 (LOW): Sentence boundary detection is simplistic
