# Streaming STT (Scribe v2 Realtime, manual commit) + speculative memory recall

Deciding how NPC memory retrieval (#122) threads into the latency-sensitive turn path surfaced the real bottleneck: the batch STT POST (`stt.Recognizer.Transcribe`, one `/v1/speech-to-text` call per endpointed utterance) is the ~1.5s that dominates the ~1.7s p95 response latency, and it leaves no text to speculate on until `STTFinal`. We pull **streaming STT into scope** rather than optimizing around it.

## What this decides

- **Provider: ElevenLabs Scribe v2 Realtime**, as a websocket sibling of the existing `scribe_v2` batch adapter. Stays inside the ADR-0039 MVP provider matrix and reuses the saved ElevenLabs BYOK key. Deepgram (named for STT in ADR-0004) stays a possible later addition behind the same interface; the batch adapter remains as fallback.
- **Local VAD stays the endpointing authority; the stream uses `commit_strategy: "manual"`.** Voiced audio (VAD-gated, with pre-roll) streams over a persistent per-Voice-Session websocket; `partial_transcript` messages flow during speech; the local VAD endpoint (incl. the #91 silence-clock) sends the manual commit, and `committed_transcript` becomes `STTFinal`. Provider-side VAD endpointing was rejected: barge-in's Confirm Window (ADR-0027) needs a local, network-independent VAD anyway, and two competing VADs create turn-boundary disagreement bugs; VAD-gating also keeps streamed-audio cost near batch parity. (This is the same division of labor Pipecat uses with this API.)
- **New `STTPartial` event** in the `voiceevent` taxonomy (ADR-0020): the mutable interim text of the in-progress utterance. Consumers must treat it as replaceable; only `STTFinal` reaches Address Detection and the Transcript.
- **Turn semantics are untouched.** Barge-in, Soft-overlap, Ensemble Turns, and deliver-then-commit (ADR-0012/0026/0027) key off local VAD and `STTFinal` exactly as today. What is rebuilt is the transcription transport: the serial POST worker becomes a stream manager with keepalive, bounded reconnect, and automatic fallback to the batch adapter when the stream is down.
- **NPC memory recall (#122) is speculative over partials, with a bounded-sync fallback.** During speech, stabilized partials are embedded and ANN retrieval (per ADR-0011's two modes) runs ahead of the turn; at `STTFinal`, if the final text matches the speculated query (normalized comparison), the prefetched chunks inject into the Hot Context memory slot at zero added latency; on mismatch — or whenever no partials are available (batch fallback, stream down, feature unconfigured) — retrieval runs inline under a hard budget (~250ms inside the turn ctx) and degrades to no-memory on timeout, with a Prometheus counter for skips and speculation misses.

## Expected effect

Endpoint→final drops from one full batch POST (~1.5s) to a manual-commit finalization (~100–300ms); response latency should fall from ~1.7s toward ~0.5s + LLM TTFT, and memory recall rides along at effectively zero marginal latency in the speculative path.

## Considered options

- **Bounded-sync retrieval only, keep batch STT** — rejected by the operator: it optimizes around the actual bottleneck. Kept as the fallback path, so the recall feature works with any batch-only STT provider.
- **Prefetch retrieval keyed on the previous turn** — rejected: the query vector misses the current utterance, which carries the retrieval key ("do you remember the ruby dagger?").
- **Provider-side VAD endpointing** — rejected, see above.
- **Deepgram as the streaming provider** — deferred: adds a second provider relationship (BYOK key, config UI, health) for maturity we don't yet know we need.

## Relationship to other ADRs

- **ADR-0011** — retrieval modes (NPC-knowledge vs world context) and async embeddings are consumed unchanged; this ADR only decides *when* retrieval runs.
- **ADR-0019/0026/0027** — endpointing authority, bus wiring, and barge-in semantics are deliberately preserved.
- **ADR-0020** — taxonomy gains `STTPartial`.
- **ADR-0021** — the websocket adapter needs a deterministic fake/replay harness like the batch cassettes before it gates CI.
- **ADR-0039** — MVP provider matrix unchanged (still Groq + ElevenLabs).
