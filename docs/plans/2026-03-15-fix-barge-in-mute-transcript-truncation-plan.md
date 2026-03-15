---
title: "fix: Barge-in, mute interruption, and transcript truncation"
type: fix
status: completed
date: 2026-03-15
---

# fix: Barge-in, Mute Interruption, and Transcript Truncation

## Overview

Three related bugs share a root cause — the mixer has no feedback path to upstream components when playback is interrupted:

1. **Barge-in doesn't stop NPCs reliably** — `audio_pipeline.go:208` calls `mixer.Interrupt(PlayerBargeIn)` but never `mixer.BargeIn(speakerID)`, so the `OnBargeIn` callback never fires.
2. **Muting doesn't stop current speech** — `MuteAgent`/`MuteAll` only set a boolean flag; they don't interrupt the mixer.
3. **Transcript records full text** — the cascade engine emits the complete LLM response regardless of whether audio was cut short. The agent also records `resp.Text` in `a.messages` before audio even starts playing.

Brainstorm: `docs/brainstorms/2026-03-15-barge-in-fix-brainstorm.md`

## Problem Statement

When a player talks over an NPC or a GM mutes an NPC mid-sentence, the NPC should stop talking immediately and the transcript should reflect only what was actually heard. Currently none of this works — audio may continue playing, and transcripts always contain the full generated text.

## Proposed Solution

A coordinated fix across four layers: mixer interface, audio segment metadata, cascade engine feedback, and mute command wiring.

### Key Architecture Decision: Engine-Side Text Tracking

The SpecFlow analysis identified a critical mismatch: the cascade engine produces **one `AudioSegment` per NPC turn** — all sentences stream through a single `Audio <-chan []byte` channel. The mixer has no concept of sentence boundaries in the audio stream.

**Chosen approach:** Track committed text in the cascade engine, not the mixer. The engine already knows sentence boundaries (it sends sentences to TTS one at a time via `textCh`). On interrupt, the engine snapshots its committed text and appends `...`.

This avoids refactoring the TTS streaming pipeline (one stream per turn) into multiple streams per sentence, which would be a much larger change for marginal benefit.

Data flow on interrupt:

```
Player speaks → VAD → mixer.BargeIn(speakerID)
                         ├── stops audio (closes cancelPlaying)
                         ├── clears queue (drains queued segments)
                         └── fires OnBargeIn callback
                              └── signals cascade engine via cancel channel
                                   └── engine snapshots committedText
                                   └── emits truncated transcript: committedText + "..."
                                   └── agent updates a.messages with truncated text
```

### Scoping Decision: S2S Engine

The S2S engine (`internal/engine/s2s/`) has a separate interrupt path via `SessionHandle.Interrupt()`. Fixing it requires different plumbing (the S2S provider handles barge-in natively). **Deferred to a follow-up issue** to keep this change focused on the cascade path, which is the primary engine used in production.

## Technical Approach

### Phase 1: Mixer Interface — Add `BargeIn` and NPC-Scoped Interrupt

**Problem:** `BargeIn(speakerID)` exists on `PriorityMixer` but not on the `audio.Mixer` interface. Also, `Interrupt(DMOverride)` is NPC-agnostic — muting NPC-A while NPC-B is speaking would stop NPC-B.

**Changes:**

#### `pkg/audio/mixer.go`

- Add `BargeIn(speakerID string)` to the `Mixer` interface. Semantics: interrupt current + clear queue + fire `OnBargeIn` callback.
- Add `InterruptNPC(npcID string, reason InterruptReason)` to the `Mixer` interface. Semantics: only interrupt if the currently playing segment's `NPCID` matches; remove queued segments with matching `NPCID`. No-op if a different NPC is playing.

```go
type Mixer interface {
    Enqueue(segment *AudioSegment, priority int)
    Interrupt(reason InterruptReason)
    BargeIn(speakerID string)                          // NEW
    InterruptNPC(npcID string, reason InterruptReason)  // NEW
    OnBargeIn(handler func(speakerID string))
    SetGap(d time.Duration)
}
```

#### `pkg/audio/mixer/mixer.go`

- `InterruptNPC`: check `m.playing.NPCID == npcID` before interrupting. Remove matching entries from queue.

#### `pkg/audio/mock/mock.go`

- Add `BargeIn` and `InterruptNPC` to the mock with call recording.

#### `internal/app/audio_pipeline.go`

- Line 208: change `p.mixer.Interrupt(audio.PlayerBargeIn)` → `p.mixer.BargeIn(speakerID)`.

**Tests:**

- `pkg/audio/mixer/mixer_test.go`: test `InterruptNPC` only interrupts matching NPC; test `BargeIn` fires handler + clears queue.

---

### Phase 2: Playback Completion Callback on AudioSegment

**Problem:** The mixer knows whether a segment finished naturally or was interrupted, but has no way to communicate this upstream.

**Changes:**

#### `pkg/audio/mixer.go` — `AudioSegment`

Add a completion callback:

```go
type AudioSegment struct {
    NPCID      string
    Audio      <-chan []byte
    SampleRate int
    Channels   int
    Priority   int
    streamErr  atomic.Pointer[error]

    // OnDone is called when the mixer finishes with this segment.
    // interrupted is true if playback was cut short (barge-in or mute),
    // false if the segment played to completion.
    // Called on the mixer's dispatch goroutine; must not block.
    // May be nil.
    OnDone func(interrupted bool)  // NEW
}
```

#### `pkg/audio/mixer/mixer.go`

- In `play()`: call `seg.OnDone(false)` on natural completion (channel closed), call `seg.OnDone(true)` on cancel/done.
- In `interruptLocked()`: for queued segments being drained, call `seg.OnDone(true)` before draining.
- Guard all calls with nil check.

**Tests:**

- `mixer_test.go`: verify `OnDone(false)` on natural playback, `OnDone(true)` on interrupt, `OnDone(true)` on queued segment drain.

---

### Phase 3: Cascade Engine — Deferred Transcript with Truncation

**Problem:** The cascade engine emits the full generated text to the transcript channel unconditionally, and the agent records `resp.Text` in `a.messages` before audio plays.

**Changes:**

#### `internal/engine/engine.go` — `Response`

Add a field for the engine to communicate the final (possibly truncated) text:

```go
type Response struct {
    Text       string
    Audio      <-chan []byte
    SampleRate int
    Channels   int
    ToolCalls  []llm.ToolCall
    streamErr  atomic.Pointer[error]

    // FinalText is closed when the definitive response text is available.
    // Read FinalTextValue after <-FinalText returns.
    // If playback completes naturally, FinalTextValue == Text.
    // If interrupted, FinalTextValue is the truncated text with "..." suffix.
    FinalText      chan struct{}  // NEW
    FinalTextValue string        // NEW — read after FinalText closes
}
```

#### `internal/engine/cascade/cascade.go`

- Track committed text atomically. Each time a sentence is sent to `textCh` (TTS input), append it to a `committedText` accumulator.
- Create a cancel channel per Process call. Set `seg.OnDone` to signal this channel.
- **Single-model path:** Set `OnDone` on the AudioSegment. On natural completion, set `FinalTextValue = Text` and close `FinalText`. On interrupt, set `FinalTextValue = committedText + "..."` and close `FinalText`.
- **Dual-model path:** Same pattern. The background goroutine tracks `committedText` as it sends sentences to `textCh`. The `OnDone` callback snapshots `committedText`.
- **Transcript emission:** Move transcript emission to happen after `OnDone` fires (either path). Emit `FinalTextValue` instead of the full generated text.

#### `internal/agent/npc.go`

- **Defer message history recording.** Instead of appending `resp.Text` immediately at line 304, launch a goroutine that waits on `<-resp.FinalText` and then appends `resp.FinalTextValue`.
- This goroutine must hold `a.mu` briefly to append — use a targeted lock acquisition, not holding the lock across the wait.
- The goroutine must also respect context cancellation to avoid leaking.

**Tests:**

- `cascade_test.go`: test that transcript emits truncated text when `OnDone(true)` fires mid-generation. Test that transcript emits full text on natural completion. Test committed text tracking across sentence boundaries.
- `npc_test.go`: test that `a.messages` contains truncated text after interrupt.

---

### Phase 4: Mute Commands Interrupt Audio

**Problem:** `MuteAgent`/`MuteAll` only set flags; they don't stop current speech.

**Changes:**

#### `internal/agent/orchestrator/orchestrator.go`

Add a mixer reference to the orchestrator:

```go
type Orchestrator struct {
    // ... existing fields ...
    mixer audio.Mixer  // NEW — may be nil (text-only mode)
}
```

- New option: `WithMixer(m audio.Mixer) Option`.
- `MuteAgent(id)`: after setting `entry.muted = true`, call `o.mixer.InterruptNPC(id, audio.DMOverride)` if mixer is non-nil.
- `MuteAll()`: after setting all flags, call `o.mixer.Interrupt(audio.DMOverride)` if mixer is non-nil.

#### Wiring — `internal/app/` or `internal/session/`

- Pass the mixer when constructing the orchestrator (wherever `orchestrator.New()` is called).

**Tests:**

- `orchestrator_test.go`: test `MuteAgent` calls `InterruptNPC` on the mock mixer. Test `MuteAll` calls `Interrupt(DMOverride)`. Test nil mixer is safe.

---

### Phase 5: Barge-In Cancels In-Flight Generation

**Problem:** When barge-in fires, the cascade engine's background goroutine (strong model + TTS) keeps running, wasting compute.

**Changes:**

#### `internal/agent/npc.go`

- Store a `context.CancelFunc` for the in-flight Process call.
- Register the mixer's `OnBargeIn` handler to cancel this context.
- On barge-in: cancel the generation context → strong model stream closes → TTS stops → audio channel closes.

```go
type liveAgent struct {
    // ... existing fields ...
    genCancel context.CancelFunc  // NEW — cancels in-flight generation
    genMu     sync.Mutex          // NEW — guards genCancel
}
```

- In `HandleUtterance`: create a child context with cancel, store cancel in `a.genCancel`.
- Register `mixer.OnBargeIn` in `NewAgent` to call `a.genCancel()`.
- Clear `genCancel` after Process completes naturally.

**Tests:**

- `npc_test.go`: test that barge-in cancels in-flight context.

## Acceptance Criteria

### Functional Requirements

- [x]Talking over an NPC (VAD fires) stops the NPC's audio immediately
- [x]`/npc mute <name>` stops that NPC's current speech immediately
- [x]`/npc muteall` stops all NPC speech immediately
- [x]Muting NPC-A while NPC-B is speaking does NOT interrupt NPC-B
- [x]Interrupted NPC transcript shows truncated text with `...` suffix
- [x]Naturally completed NPC speech transcript shows full text (no `...`)
- [x]Agent message history (`a.messages`) reflects truncated text after interrupt
- [x]Barge-in cancels in-flight LLM/TTS generation (no wasted compute)
- [x]Queued NPC segments that were never played are drained and their `OnDone(true)` fires

### Non-Functional Requirements

- [x]All new code has `t.Parallel()` on tests and subtests
- [x]Race detector passes: `go test -race -count=1 ./...`
- [x]No locks held during blocking I/O (especially the `OnDone` callback)
- [x]Compile-time interface assertions for updated `Mixer` interface
- [x]Mock implementations updated for new interface methods

### Quality Gates

- [x]`make check` passes (fmt + vet + test)
- [x]`make lint` passes
- [x]Existing mixer tests still pass
- [x]Existing cascade engine tests still pass
- [x]Existing orchestrator tests still pass

## Dependencies & Prerequisites

- No external dependencies. All changes are internal to the codebase.
- The `audio.Mixer` interface change affects: `pkg/audio/mixer/mixer.go`, `pkg/audio/mock/mock.go`, and any test doubles.

## Risk Analysis & Mitigation

| Risk | Impact | Mitigation |
|------|--------|------------|
| `OnDone` callback blocks mixer dispatch goroutine | Audio playback stalls | Document "must not block" contract; keep callback lightweight (signal a channel) |
| Race between natural completion and interrupt | Double `OnDone` call | Use `sync.Once` wrapper in mixer's play loop |
| Agent `a.messages` append races with HandleUtterance | Corrupted history | Use targeted mutex acquisition in the deferred goroutine |
| Deferred transcript emission leaks goroutine | Resource leak | Respect context cancellation in the wait goroutine |
| `InterruptNPC` iterates queue under lock | Latency spike on large queues | Queue is typically small (<16 segments); acceptable |

## References & Research

### Internal References

- Brainstorm: `docs/brainstorms/2026-03-15-barge-in-fix-brainstorm.md`
- Mixer interface: `pkg/audio/mixer.go:91-119`
- PriorityMixer: `pkg/audio/mixer/mixer.go` (BargeIn at line 176, Interrupt at line 151)
- Audio pipeline barge-in: `internal/app/audio_pipeline.go:208`
- Cascade transcript emission: `internal/engine/cascade/cascade.go:287-291`
- Agent message recording: `internal/agent/npc.go:302-312`
- Mute commands: `internal/discord/commands/npc.go:136-258`
- Orchestrator mute: `internal/agent/orchestrator/orchestrator.go:175-343`

### Follow-Up Work

- S2S engine barge-in: wire `SessionHandle.Interrupt()` on barge-in, implement transcript truncation for S2S providers
- Queued-but-never-played NPC responses: decide whether to mark as "cancelled" in transcript or silently drop
