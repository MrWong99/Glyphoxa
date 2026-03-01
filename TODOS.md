# Audio Input Pipeline — Issues from First Run

Findings from `bin/last_run.log` (2026-03-02 session).

---

## 1. NPC entities missing from knowledge graph (blocking — NPCs never respond)

Every `HandleUtterance` fails with:
```
agent: assemble hot context: hot context: identity snapshot for "npc-2-Schwester Anselma":
  knowledge graph: identity snapshot: entity "npc-2-Schwester Anselma" not found
```

**Root cause:** NPC agents are created with IDs like `npc-2-Schwester Anselma`, but nothing
registers them as `memory.Entity` records in the knowledge graph. The hot context assembler
(`internal/hotctx/assembler.go`) calls `graph.IdentitySnapshot(npcID)` which queries the
`entities` table in Postgres — the row doesn't exist.

**Fix needed:** After loading agents (in both `App.initAgents` and `SessionManager.loadAgents`),
create a `memory.Entity` for each NPC with matching ID/Name/Type and call `graph.AddEntity()`.

---

## 2. Speaker ID is SSRC, not Discord user ID

The pipeline worker is started with `user_id=7012` — that's the RTP SSRC, not a Discord snowflake.

**Root cause:** `Connection.recvLoop()` (`pkg/audio/discord/connection.go:164`) uses
`strconv.FormatUint(uint64(ssrc), 10)` as the participant key for both `InputStreams` and the
`EventJoin` callback. Meanwhile `handleVoiceStateUpdate` emits events with the real Discord
`vsu.UserID`. These two identity systems never converge:

- `InputStreams()` map is keyed by SSRC string → pipeline workers get SSRC as speaker ID
- `handleVoiceStateUpdate` emits join/leave with Discord user ID → no matching input stream
  exists for that ID, so the pipeline logs "join event but no input stream" (or worse, silently
  starts a dead worker)

**Impact:** Orchestrator address detection, puppet mode, utterance buffer attribution, and
transcript logging all use a meaningless numeric SSRC instead of a real user identity.

**Fix needed:** The Discord connection needs to resolve SSRC → Discord user ID (the `ssrcUser`
map exists but is populated with `ssrcStr` instead of actual user IDs). Alternatively, correlate
`VoiceStateUpdate` events with SSRCs via the Discord voice state speaking events.

---

## 3. Opus decode error flood (no rate limiting)

~50 `"discord: opus decode: invalid packet"` warnings in <100ms at 00:18:19, flooding the log.

**Root cause:** After the `/session stop` command is issued (permissions check at 00:18:08), the
Discord voice gateway likely sends transition/keepalive packets that don't decode cleanly. Every
single bad packet logs a WARN with no deduplication or rate limiting.

**Fix needed:** Rate-limit or deduplicate the decode error log in `recvLoop`. For example, log
the first error, then only log a summary count every N seconds or on loop exit (the
`framesDropped` counter pattern already exists but isn't used for decode errors).

---

## 4. Whisper transcription latency (~4–5s, exceeds 2s hard limit)

| Speech end | Transcript arrives | Latency |
|---|---|---|
| 00:17:18.960 | 00:17:23.402 | **4.4s** |
| 00:17:43.939 | 00:17:48.240 | **4.3s** |
| 00:18:00.281 | 00:18:04.492 | **4.2s** |

Whisper logs `no GPU found` — running large-v3-turbo on CPU. The architecture target is <1.2s
mouth-to-ear, 2.0s hard limit.

**Impact:** Even if the knowledge graph issue were fixed, the response would take 4+ seconds just
for transcription, plus LLM + TTS time on top.

**Fix needed:** Either enable GPU acceleration, use a smaller model (e.g., base/small), or switch
to a streaming STT provider (Deepgram) for real-time use.

---

## 5. VAD false triggers / premature cutoffs

Several issues with the energy VAD:

- **Zero-duration speech segments:** Lines 136–142 show speech start and end at the *same
  timestamp* (00:18:04.492). The energy VAD fires on transient noise, opens an STT session, and
  immediately ends it.
- **Mid-word cutoffs:** Transcripts like `"Und"` and `"der..."` are fragments of longer
  utterances. The VAD `silence_threshold=0.35` is ending speech too eagerly.
- **Cascading triggers:** After a ~4s whisper processing pause, buffered frames arrive in a burst,
  causing rapid speech-start/speech-end oscillation.

**Fix needed:** Tune VAD parameters (higher silence threshold, minimum speech duration before
opening STT, hangover time after speech end). Consider using Silero VAD instead of energy-based
for better accuracy.

---

## 6. Frame drops (8.6%)

```
discord: recvLoop progress packetsReceived=1000 framesDecoded=1000 framesDropped=86
```

86 out of 1000 frames dropped because the per-participant input channel (buffer=64) was full.

**Root cause:** The pipeline consumer (VAD + STT) can't keep up during CPU-heavy whisper
transcription. When whisper blocks the CPU for ~4s, incoming frames back up and overflow the
64-frame buffer.

**Impact:** Dropped frames create audio gaps that can confuse both VAD (false silence detection)
and STT (garbled transcription).

**Fix needed:** Increase channel buffer, or ensure the pipeline consumer never blocks (VAD is
synchronous and fast, so the bottleneck is likely upstream CPU contention with whisper).

---

## 7. Confidence always 0

All transcripts report `confidence=0`. The whisper-native provider doesn't populate the
`Confidence` field in `stt.Transcript`.

**Impact:** Low priority — doesn't affect functionality. But downstream features that might
use confidence for filtering (e.g., ignoring low-confidence noise transcripts) won't work.

---

## 8. Post-disconnect stale transcript processing

```
00:18:25.453 audio pipeline: transcript speaker=7012 text="Mordriggert's T."
00:18:25.453 audio pipeline: route transcript speaker=7012 err="orchestrator: context canceled"
```

Whisper finishes transcribing audio that was captured before disconnect, but the session context
is already cancelled by the time the transcript is ready.

**Impact:** Minor — the error is logged and handled gracefully. But it means the last utterance
before disconnect is always lost.

**Fix needed:** Consider a short grace period before cancelling the pipeline context, to allow
in-flight transcriptions to complete and route.

---

## Summary

| # | Issue | Severity | Blocks NPC response? |
|---|---|---|---|
| 1 | NPC entities not in knowledge graph | **Critical** | Yes |
| 2 | Speaker ID is SSRC not user ID | **High** | No (routing works by name mention) |
| 3 | Opus decode error log flood | Medium | No |
| 4 | Whisper latency 4–5s on CPU | **High** | No (but unusable UX) |
| 5 | VAD false triggers / premature cutoffs | **High** | Partially (fragments) |
| 6 | Frame drops 8.6% | Medium | No |
| 7 | Confidence always 0 | Low | No |
| 8 | Post-disconnect stale transcripts | Low | No |
