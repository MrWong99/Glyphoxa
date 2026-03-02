# TODOS — Codebase Audit (2026-03-02)

Audit comparing implementation against `docs/design/` specifications.

---

## HIGH

### 1. ~~`collectAndRoute` goroutine can hang forever, deadlocking `Stop()`~~ FIXED

**`internal/app/audio_pipeline.go`**

Replaced `for t := range session.Finals()` with a `select` loop that also listens
on `ctx.Done()`. Added `TestCollectAndRoute_ContextCancellation` to verify the
goroutine exits promptly when context is cancelled even if `Finals()` never closes.

### 2. ~~Hot context assembler has no timeout — silently blows latency budget~~ FIXED

**`internal/hotctx/assembler.go`**

Added `assemblyTimeout` field (default 50ms) with `WithAssemblyTimeout` option.
`Assemble` now wraps the caller's context with `context.WithTimeout` before the
critical-path errgroup. The pre-fetcher retains its own independent 40ms timeout.
Added `TestAssemble_TimeoutFires` and `TestAssemble_CustomTimeoutSuccess`.

---

## MEDIUM

### 3. ~~`sttCfg` data race in audio pipeline~~ FIXED

**`internal/app/audio_pipeline.go`**

Snapshot `p.sttCfg` under `p.mu.Lock()` at `VADSpeechStart` with `slices.Clone()`
for the Keywords slice. Added `TestAudioPipeline_ConcurrentKeywordUpdate` to verify
no races under concurrent `UpdateKeywords` + `processParticipant`.

### 4. ~~Consolidator advances `lastIndex` despite L1 write failures~~ FIXED

**`internal/session/consolidator.go`**

Track `newLastIndex` separately — only advance past successfully written entries.
Break on first `WriteEntry` failure so subsequent entries are retried on the next
tick. Only successfully written entries are passed to `indexChunks`.
Added `TestConsolidate_WriteFailure` with partial failure, all-fail, and L2
consistency subtests.

### 5. ~~S2S engine silently drops context injection errors~~ FIXED

**`internal/engine/s2s/engine.go`**

- `SetTools` on reconnect: log `slog.Warn` (non-critical, don't propagate).
- `UpdateInstructions` in `Process`: propagate error (critical path).
- `InjectTextContext` in `Process`: propagate error (critical path).

Added `TestProcess_UpdateInstructionsError`, `TestProcess_InjectTextContextError`,
and `TestEnsureSession_SetToolsWarning`.

### 6. ~~`stt.ErrNotSupported` sentinel doesn't exist~~ FIXED

**`pkg/provider/stt/provider.go`**

Added exported `var ErrNotSupported`. Deepgram and Whisper providers wrap it via
`fmt.Errorf("provider: %w", stt.ErrNotSupported)`. Added `errors.Is` assertions
in deepgram, whisper, and native whisper tests.

### 7. ~~MCP host TOCTOU: server session can be closed mid-call~~ FIXED

**`internal/mcp/mcphost/host.go`**

Added `inflight sync.WaitGroup` to `serverConn` (now stored as `*serverConn`
pointer in the map). `executeMCPTool` calls `inflight.Add(1)` under RLock before
using the session, `Done()` after. `RegisterServer` and `Close` call
`inflight.Wait()` outside the lock before closing old sessions. Added
`TestCloseWaitsForInflight` and `TestConcurrentExecuteAndClose`.

### 8. ~~Session manager: `Disconnect()` before `cancel()` ordering bug~~ FIXED

**`internal/app/session_manager.go`**

Reordered `Stop()`: consolidate → `cancel()` → closers in reverse →
`recorderWG.Wait()` → `conn.Disconnect()` last. This ensures no participant-change
events race with background goroutines during teardown.

---

## LOW

### 9. `sentence_cascade` engine is a forward declaration only

**`internal/config/config.go`, `internal/app/app.go:431`**

`EngineSentenceCascade` passes config validation but is handled identically to
`EngineCascaded` — both LLM slots receive the same provider. The dual-model
sentence cascade described in `05-sentence-cascade.md` is not implemented.

### 10. Orchestrator `lastSpeaker` updated before success

**`internal/agent/orchestrator/orchestrator.go:123`**

`o.lastSpeaker = targetID` is set before `InjectContext` at line 144. If injection
fails, `Route` returns `(nil, err)` but `lastSpeaker` already points to the failed
target, corrupting conversational continuity state.

**Fix**: Move the `lastSpeaker` assignment after the `InjectContext` call succeeds.

### 11. Cascade sentence boundary: dead code in helper functions

**`internal/engine/cascade/cascade.go:689-694, 681`**

`isDecimalDot` requires `s[i+1]` to be a digit, but its caller already verified
`s[i+1]` is whitespace — so it always returns false. Similarly, `isEllipsisDot`'s
trailing-dot branch checks `s[i+1] == '.'` but that position is always whitespace.
Both are effectively dead code (the outer guard still protects correctly, so no
functional bugs). Also, the abbreviation allow-list is missing common
TTRPG/military titles (`Adm`, `Capt`, `Pvt`, etc.).

### 12. TTS `MeasureLatency()` specified in design but not implemented

**`pkg/provider/tts/provider.go`**

Design doc `02-providers.md` specifies `MeasureLatency(voice VoiceProfile) LatencyReport`
on the TTS provider interface. This method does not exist in the codebase.

### 13. Audio `OutputStream` per-NPC named output not in interface

**`pkg/audio/platform.go`**

Design specifies `OutputStream(voiceID string)` for per-NPC named output streams.
The actual interface has `OutputStream() chan<- AudioFrame` with no `voiceID`
parameter — a single mixed output channel.

### 14. Missing provider implementations from design

Per `02-providers.md` and `07-technology.md`:

- **AssemblyAI** STT provider — not implemented (only Deepgram + Whisper).
- **Cartesia** TTS provider — not implemented (only ElevenLabs + Coqui).
- **Voyage AI** embeddings provider — not implemented (only OpenAI + Ollama).

### 15. Missing built-in tool servers from design

Per `04-mcp-tools.md`:

- `image-gen`, `web-search`, `music-ambiance`, `session-manager` — not implemented
  (only dice-roller, file-io, memory tools, rules-lookup exist).

### 16. WebRTC is a stub

**`pkg/audio/webrtc/`**

`PeerTransport` has only a `mockTransport` with stub SDP. No real pion/webrtc
integration. Signaling server returns mock SDPs. Documented as "alpha".

### 17. Feedback store is file-based placeholder

**`internal/feedback/store.go`**

`FileStore` appends JSON-lines to a local file. Doc comment flags this for
PostgreSQL replacement.
