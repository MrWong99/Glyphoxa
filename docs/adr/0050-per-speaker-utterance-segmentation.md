# Per-speaker utterance segmentation: N Speaker Lanes, SpeakerID on events

ADR-0019/ADR-0039 deferred speaker attribution; Epic 4 needs it. Discord already separates speakers on the wire (`voice.Frame.UserID` via SSRC; per-speaker Opus decoders in `pkg/voice/wire/codec`) — the open question was how utterance segmentation attributes each STT final to a speaker. Decided with the operator 2026-07-07 (#275): **one lane per active speaker**.

## What this decides

- **N-lane segmentation.** Each active speaker gets a **Speaker Lane**: a Silero VAD session fed by that speaker's already-separated frame stream, emitting per-speaker utterance windows. Lane lifecycle: created on first frame from a `Frame.UserID`, reaped on channel leave or idle timeout. The mixed-lane pipeline disappears for humans; Agent playback is untouched (outbound path).
- **The cost model that makes this cheap**: batch STT (today's default) stays a **shared worker pool** — lanes emit utterance windows into it, so STT cost per utterance is unchanged vs single-lane; only Silero state multiplies (per-lane CPU, small). Streaming STT (flag-gated, ADR-0042) opens a per-lane connection **only while that lane is speaking**, with a concurrency cap — concurrent sockets equal concurrent speakers, not channel size.
- **`SpeakerID` (Discord snowflake string, empty = unattributed) is added additively** to `STTPartial`, `STTFinal`, and `VADSpeechStart`, per ADR-0039's stated seam. **`BargeDetected` also gains `SpeakerID`** — lanes know who barged; cheap now, needed by Epic 8's detector and future floor rules.
- **Cross-talk and Soft-overlap**: overlapping speech transcribes correctly on each speaker's own lane; a Soft-overlap backchannel becomes a short, correctly-attributed line on its own lane. This is the whole point — attribution errors would bake permanently into Transcript Lines and immutable Chunks.
- **The silence clock (#91/#147) stays speaker-agnostic**: session silence = no lane speaking (OR over lane activity). No per-speaker endpointing changes.
- **SLO posture (ADR-0033)**: per-utterance latency is unchanged (same STT calls); the added cost is per-lane VAD compute and, in streaming mode, capped concurrent connections. No SLO relaxation.
- **GM identity for downstream consumers stays operator-allowlist membership** (ADR-0010/0041). There is no per-session GM binding in the schema; consumers must not invent one — a GM-allowlisted `SpeakerID` routes to the KindGM lane (#281). *Amended by ADR-0055 (2026-07-18): with self-signup, GM identity becomes tenant-bound (the Tenant's operator/Member binding) instead of env-allowlist membership, at every consumer of this clause; the no-per-session-GM-binding rule stands unchanged.*

## Considered and rejected

- **Single-lane dominant-speaker attribution** (attribute each utterance to the dominant `Frame.UserID` in its VAD window) — cheap, but mis-attributes exactly the moments a table cares about (cross-talk, excited overlap, backchannels), and those errors persist in immutable Chunks and embedded text forever.
- **Single-lane now, N-lane later** — ships poisoned history plus a second migration of the hot path.
- **Always-open per-speaker streaming connections** — pays provider-connection cost for silent participants; per-speech-burst sockets with a cap achieve the same attribution.

## Relationship to other ADRs

Amends the ADR-0019/ADR-0039 single-lane deferral (amendment notes added there). ADR-0042 (streaming STT the lanes ride), ADR-0033 (SLOs held), ADR-0010/0041 (GM identity), ADR-0040/0011 (the persisted grains attribution feeds, via #278/#281), ADR-0051 (per-speaker consent exclusion depends on lanes).
