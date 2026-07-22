# Transcript lines: per-line persistence for Session replay

The Session screen (ADR-0039) renders a live transcript and, on Stop, a
last-session summary (`{endedAt, duration, lineCount}`); reconnect/reload must
replay the history. Until now the relay (ADR-0014 Hop-B) held lines only in an
in-process ring scoped to the live session, and `voice_sessions.line_count` was
always 0. This ADR records how transcript Lines are persisted (#74).

## What this decides

- **A new `transcript_line` table at the LINE grain, distinct from the chunk
  grain (ADR-0011).** One row per rendered transcript Line — a single human
  utterance or a coalesced Agent reply — keyed `UNIQUE (voice_session_id,
  line_id)` with `seq` (the relay's monotonic `Frame.Seq`) as the replay ordering
  key. This is a DIFFERENT concern from `transcript_chunk` (3–6 utterances,
  embedded for ANN retrieval / Hot Context, ADR-0011): the chunk grain serves
  NPC knowledge retrieval, the line grain serves Session replay. The two are
  separate records of the same speech and do not share rows. `line_count` =
  `COUNT(*)` of these rows, so rows == distinct lines and the summary count
  matches the persisted history (the `00006` comment's intent made real).

- **Incremental async UPSERT, not flush-on-stop.** The relay tees each emitted
  Line into a non-blocking buffered queue drained by ONE writer goroutine that
  UPSERTs it. The bus delivers `project` synchronously and must not block, so the
  tee drops + logs on overflow rather than ever calling the DB inline. An Agent
  reply coalesces across its sentences under one stable `line_id`, so the write
  is an UPSERT (`ON CONFLICT DO UPDATE`). Chosen over flush-on-stop for
  crash-durability: a session that dies mid-run still has most of its lines on
  disk. On Stop the Manager calls `Finalize`, which sends a flush barrier through
  the SAME queue (FIFO guarantees every prior line landed) and returns the
  authoritative `COUNT(*)`, recorded by `EndVoiceSession` before the row ends.

- **History-on-reload via the DB-backed REST snapshot.** `GET /api/v1/sessions/{id}`
  (`relay.ServeSnapshot`) returns the in-memory coalesced lines for the live
  active session (unchanged) and, for any other (ended) session, replays the
  persisted lines from `transcript_line` ordered by `seq` with status `idle`. The
  web `useSessionEvents` hook fetches this snapshot for any session that exists
  (live or ended) and only opens the SSE tail while active, so a reload replays
  the persisted transcript even after the in-memory ring is gone.

- **No proto change.** `VoiceSession{started_at, ended_at, line_count}` already
  carries the summary; the only backend gap was making `line_count` real. The
  summary is "returned by the snapshot RPC" (`SessionService.GetSession`) once
  the count is populated; the web computes duration client-side.

## Why

The two grains answer two questions and must not be conflated: "what was said,
in order, for the operator to read back" (line) vs "what topic/knowledge to
retrieve for an NPC" (chunk, embedded). Forcing one table to serve both would
either bloat the retrieval index with per-utterance noise or lose the per-line
ordering the screen needs. The incremental async UPSERT keeps the synchronous bus
non-blocking (the relay's load-bearing contract) while buying crash-durability a
flush-on-stop cannot; the flush barrier makes the Stop count authoritative
without a second source of truth. The DB-backed snapshot reuses the existing
Hop-B REST endpoint (ADR-0014) rather than adding a new history API.

## Considered options

- **Reuse `transcript_chunk` for replay** — rejected: the chunk grain is 3–6
  utterances embedded for retrieval (ADR-0011); it has neither per-line ordering
  nor a 1:1 mapping to rendered lines, so `line_count` could not match rows.
- **Flush all lines on Stop** — rejected: a crashed/killed session loses its
  whole transcript; the incremental writer bounds loss to whatever was still
  queued.
- **Synchronous DB write inside `project`/`emitLine`** — rejected: the bus
  delivers synchronously and must not block; a slow DB would stall the voice
  pipeline.
- **In-memory ring only (status quo)** — rejected: it cannot survive a reload or
  process restart, failing the reconnect-replay acceptance.

## Relationship to other ADRs

- **ADR-0011 (transcript chunks)** — the line grain is explicitly distinct from
  the chunk retrieval/embedding grain; ADR-0039 left "whether the per-line view
  needs a finer table than the chunk grain" to the implementing slice — this is
  that decision.
- **ADR-0014 (gRPC bus + SSE)** — persistence tees off the Hop-B relay's
  projection; the DB-backed snapshot extends the existing REST snapshot endpoint.
- **ADR-0039 (single-operator web tier)** — supplies the Session screen, the
  anonymous human lane, and the `kind ∈ {gm,player,npc,butler}` taxonomy the
  persisted line mirrors; the in-proc `SessionManager` finalizes the relay on
  Stop.

---

**Amendment (2026-07-22, #437 — the LINE grain conforms to ADR-0012's
delivered-only invariant):** a `transcript_line` row may only outlive its
Voice Session if its speech was actually delivered. The Relay persists
optimistically at `TTSInvoked` (unchanged — the live feed's latency contract
stays), but reconciles on TTS start-error and turn-end: a line whose turn
delivered **zero** sentences is deleted from `transcript_line` through the
same single-writer queue, so replay never shows text the room never heard.
The live SSE feed may therefore transiently show a line that a later reload
drops — accepted, and correct: it was never speech. Failure evidence belongs
to the Turn's terminal reason and logs, not the Transcript. This closes the
undocumented divergence with the Chunker (which already purged undelivered
sentences) and keeps the Transcript doctrine singular across both grains:
**the Transcript records delivered speech, at sentence grain** (sentence-grain
rounding on barge per #401 stays accepted).
