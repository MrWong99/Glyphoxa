# TODOS — Codebase Audit (2026-03-02)

Audit comparing implementation against `docs/design/` specifications.
Runtime bugs from live testing session (2026-03-03, config `v1.yaml`).

---

## HIGH — Runtime bugs (from live session 2026-03-03)

### ~~10. TTS sample rate mismatch causes pitched-up NPC voices~~ ✅ Fixed (721f7d3)

**`internal/app/app.go:438-443`, `internal/engine/cascade/cascade.go:159`**

`buildEngine()` creates a cascade engine without passing `WithTTSFormat()`.
The cascade engine defaults to `ttsSampleRate = 22050`. But ElevenLabs TTS
actually outputs at 16000 Hz (correctly detected by `ttsFormatFromConfig()`
at line 482, but only used for the agent loader, not the main engine).

The Discord send path sees the audio tagged as 22050 Hz mono and resamples to
48000 Hz stereo using the wrong ratio (22050/48000 instead of 16000/48000).
This stretches/compresses samples incorrectly, producing audible pitch shift.

Log evidence: `audio format mismatch: converting from="22050Hz mono" to="48000Hz stereo"`

**Fix**: Pass `cascade.WithTTSFormat(sr, ch)` from `ttsFormatFromConfig()` into
the `cascade.New()` call in `buildEngine()`. Requires threading the TTS provider
config entry through.

### ~~11. LLM transcript correction is overly aggressive / runs unconstrained~~ ✅ Fixed (1cdbcfb)

**`internal/transcript/corrector.go:129-131`, `internal/transcript/llmcorrect/`**

The LLM correction always runs when there is no per-word confidence data
(`len(t.Words) == 0`). Even when per-word data IS present, the LLM ignores its
own "be conservative" system prompt and aggressively replaces arbitrary words
with NPC names, destroying the original meaning:

- `"Wir sind heute in einer Story, die befindet sich."` →
  `"Wir sind heute in Hildegard die Kräuterfrau befindet sich."` —
  replaced `"einer Story, die"` with an NPC name. The original was correct;
  the user was describing the setting, not addressing an NPC. This forced the
  NPC to respond out of context.
- `"Ist die des."` → `"Hildegard die Kräuterfrau"` — entire utterance replaced.
- `"Wenn ich über die Pflanzen am Talgrund rede"` →
  `"Wenn Hildegard die Kräuterfrau Pflanzen am Talgrund rede"` — replaced
  `"ich über die"`.

The verification layer (`llmcorrect/verify.go`) validates that claimed
corrections exist in the token diff but does not validate plausibility (i.e.,
whether the original text actually sounds like an NPC name).

**Fix options**:
- Add edit-distance or phonetic-similarity threshold in the verifier to reject
  corrections where the original bears no resemblance to any known entity name.
- Skip LLM correction entirely when per-word confidence is unavailable (rather
  than treating "no data" as "all low confidence").
- Consider making LLM correction opt-in via config.

### 12. ElevenLabs STT session close always times out (goroutine lifecycle bug)

**`pkg/provider/stt/elevenlabs/elevenlabs.go:268-286`**

`Close()` signals `s.done`, then waits up to 5 s for `writeLoop` and `readLoop`
to exit. But `readLoop` blocks on `s.conn.Read(ctx)` and has no `select` on
`s.done`. The websocket close at line 283 (which would unblock `readLoop`)
only happens _after_ the wait times out. So the timeout fires on every single
session close — it is not a transient failure.

Log evidence: `elevenlabs: close timed out waiting for goroutines` appears after
every speech segment without exception.

**Fix**: Close the websocket connection (or cancel a derived context) _before_
`wg.Wait()` so `readLoop`'s `conn.Read` returns an error and the goroutine
exits promptly. Ensure `writeLoop` sends its final commit first (e.g., wait on a
`writeLoop`-specific done signal before closing the socket).

### ~~13. Knowledge graph `Neighbors()` recursive CTE fails on PostgreSQL~~ ✅ Fixed

**`pkg/memory/postgres/knowledge_graph.go:293-331`**

The bidirectional traversal query uses two `UNION ALL` branches that both
reference `reachable`. PostgreSQL parses `A UNION ALL B UNION ALL C` as
`(A UNION ALL B) UNION ALL C`, making `B` part of the "non-recursive term" of
the outer union — which then contains a recursive self-reference, violating
SQL:1999 rules (SQLSTATE 42P19).

Log evidence: `pre-fetch: retrieve: neighbors lookup failed … ERROR: recursive
reference to query "reachable" must not appear within its non-recursive term`

This breaks knowledge graph neighbor lookups during hot-context assembly,
degrading NPC context quality.

**Fix**: Merged the two recursive legs into a single `SELECT` using
`OR` on the join condition and a `CASE` expression to pick the opposite end
of the edge. Also fixed the same pattern in `FindPath()`.

### 14. ElevenLabs STT emits duplicate final transcripts

**`pkg/provider/stt/elevenlabs/elevenlabs.go:389`**

`parseResponse` handles both `"committed_transcript"` and
`"committed_transcript_with_timestamps"` as `IsFinal = true`. If ElevenLabs
sends both message types for the same commit (which it does), the same
transcript is emitted twice on the `finals` channel. Downstream processing
(correction + routing) runs twice for the identical utterance.

Log evidence: identical transcript `"Ist die des."` corrected and routed twice
at 00:39:35.646 and 00:39:35.836.

**Fix**: Track the last committed text (or a sequence ID) and deduplicate, or
only handle one of the two message types.

---

## MEDIUM — Runtime bugs (from live session 2026-03-03)

### 15. Orchestrator routing fails for most utterances after mute/unmute cycle

**`internal/agent/orchestrator/address.go`, `orchestrator.go`**

After `/npc muteall` + `/npc unmuteall`, no NPCs respond. The mute state itself
is cleared correctly (`UnmuteAll` sets `entry.muted = false`), but the routing
chain (explicit name match → DM override → last-speaker → single-NPC fallback)
fails because:

1. The single-NPC fallback only fires when exactly 1 NPC is unmuted; with 3
   unmuted NPCs it is skipped.
2. Last-speaker continuation requires a previous successful route, which may
   not exist after a mute cycle resets conversational flow.
3. Explicit name matching via `strings.Contains` on the transcript fails when
   the transcript has been corrupted by LLM correction (see #11) or when the
   user simply doesn't say an NPC name.

The net effect is that with 3 NPCs and no explicit name in the utterance,
routing returns `"orchestrator: no target NPC identified"` every time.

**Fix**: Consider a more robust fallback — e.g., use the LLM orchestrator to
infer intent, or allow routing to the most contextually relevant NPC based on
recent conversation history rather than requiring an explicit name.

---

## LOW

### 1. `sentence_cascade` engine is a forward declaration only

**`internal/config/config.go`, `internal/app/app.go:431`**

`EngineSentenceCascade` passes config validation but is handled identically to
`EngineCascaded` — both LLM slots receive the same provider. The dual-model
sentence cascade described in `05-sentence-cascade.md` is not implemented.

### 2. Orchestrator `lastSpeaker` updated before success

**`internal/agent/orchestrator/orchestrator.go:123`**

`o.lastSpeaker = targetID` is set before `InjectContext` at line 144. If injection
fails, `Route` returns `(nil, err)` but `lastSpeaker` already points to the failed
target, corrupting conversational continuity state.

**Fix**: Move the `lastSpeaker` assignment after the `InjectContext` call succeeds.

### 3. Cascade sentence boundary: dead code in helper functions

**`internal/engine/cascade/cascade.go:689-694, 681`**

`isDecimalDot` requires `s[i+1]` to be a digit, but its caller already verified
`s[i+1]` is whitespace — so it always returns false. Similarly, `isEllipsisDot`'s
trailing-dot branch checks `s[i+1] == '.'` but that position is always whitespace.
Both are effectively dead code (the outer guard still protects correctly, so no
functional bugs). Also, the abbreviation allow-list is missing common
TTRPG/military titles (`Adm`, `Capt`, `Pvt`, etc.).

### 4. TTS `MeasureLatency()` specified in design but not implemented

**`pkg/provider/tts/provider.go`**

Design doc `02-providers.md` specifies `MeasureLatency(voice VoiceProfile) LatencyReport`
on the TTS provider interface. This method does not exist in the codebase.

### 5. Audio `OutputStream` per-NPC named output not in interface

**`pkg/audio/platform.go`**

Design specifies `OutputStream(voiceID string)` for per-NPC named output streams.
The actual interface has `OutputStream() chan<- AudioFrame` with no `voiceID`
parameter — a single mixed output channel.

### 6. Missing provider implementations from design

Per `02-providers.md` and `07-technology.md`:

- **AssemblyAI** STT provider — not implemented (only Deepgram + Whisper).
- **Cartesia** TTS provider — not implemented (only ElevenLabs + Coqui).
- **Voyage AI** embeddings provider — not implemented (only OpenAI + Ollama).

### 7. Missing built-in tool servers from design

Per `04-mcp-tools.md`:

- `image-gen`, `web-search`, `music-ambiance`, `session-manager` — not implemented
  (only dice-roller, file-io, memory tools, rules-lookup exist).

### 8. WebRTC is a stub

**`pkg/audio/webrtc/`**

`PeerTransport` has only a `mockTransport` with stub SDP. No real pion/webrtc
integration. Signaling server returns mock SDPs. Documented as "alpha".

### 9. Feedback store is file-based placeholder

**`internal/feedback/store.go`**

`FileStore` appends JSON-lines to a local file. Doc comment flags this for
PostgreSQL replacement.
