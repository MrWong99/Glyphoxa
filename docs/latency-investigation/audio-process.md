# Latency investigation — audio process / concept (the 20 s)

**Author:** audio-process (Agent Team `glyphoxa-latency`)
**Branch:** `lat/audio-process`
**Evidence:** `glyphoxa-livetest-20260610.log`, `glyphoxa-livetest-20260610-metrics.txt`
**Date:** 2026-06-10

---

## TL;DR

The 20 s the user lived through and the 2.3 s the metric reported are **not the
same turns**. The metric only records a turn that produced first audio; the turns
that made the user wait produced **no audio at all** because their TTS calls were
**cancelled or rejected**, so they emit zero `response_latency` samples *by
design*. The 20 s lives in those invisible, self-cancelled turns — not in slow
Gemini reasoning.

The single recorded turn ran in **1.599 s** — *faster* than even ADR-0035's
trivial single-call Gemini floor (~1.9 s). The turn that worked was fine.
The problem is the turns that never finished.

**Ranked root causes**

1. **`WithBargeIn(0)` + a single shared VAD session self-cancels the turn the user
   is waiting for** (the `context canceled` at 22:36:17). The user's *own
   continuing speech* — or a VAD split of one utterance — fires `VADSpeechStart`
   while Bart holds the floor, and a 0 ms confirm window yields instantly →
   the in-flight TTS POST is cancelled. **This is the 20 s.**
2. **Per-segment turns + `Floor.Take` supersession self-cancel** — independent of
   barge-in. Each STT segment opens a *new* `TurnID` and a *new* `Floor.Take`,
   which cancels the previous turn. One utterance VAD-split into two segments =
   the first turn cancelled by the second. The `vadMinSilenceFrames` 15→12 loosening
   makes mid-utterance splits *more* likely.
3. **Survivorship-biased SLO metric + near-empty logs** — the instrumentation
   *cannot see* (1) and (2). This is itself a primary finding: the 20 s was
   invisible, so it looked like 2.3 s.
4. **Baseline latency is mediocre (seconds, not 20 s): H2 multi-round Gemini with
   the `dice` tool granted.** A tool-call round streams *no* prose, so first audio
   waits for round-0 thinking + tool exec + round-1 thinking. Explains why even a
   *surviving* turn isn't snappy; does **not** explain 20 s.
5. **The `eleven_v3` empty-tag 400 (22:36:40)** — now fixed on this branch
   (`f744942`), but it was a third silent failure mode the same night.

---

## 1. Reconcile the trace — it already tells the whole story

Three reply attempts that night, laid against the metric series:

| wall-clock | event (log) | metric effect |
|---|---|---|
| ~22:36:17 | TTS **`context canceled`** (Synthesize POST aborted mid-flight) | **no sample** |
| ~22:36:38 | TTS success → first audio | `turns=1 sum=1.599 s` |
| ~22:36:40 | TTS **HTTP 400 empty-text** (`eleven_v3` tag-only sentence) | **no sample** |

Two of three attempts produced **zero audio** and therefore zero
`response_latency` samples. The user heard nothing across the cancellations and
waited; the metric faithfully recorded the *one* turn that squeaked through at
1.599 s. The headline "p50 2.3 s" is a survivor average over a sample of one.

The metric did not "miss" the 20 s turns. By construction
(`internal/observe/subscriber.go:33-37`) a turn that never emits `FirstAudio`
records no `response_latency`. That comment calls it "the correct *cancelled = no
audible response* behaviour" — correct for a *barge-in* (the user chose to cut
Bart), **wrong** as the sole record when the cancel is the system silencing
*itself*. Same event, opposite meaning, and the metric can't tell them apart.

---

## 2. Root cause #1 — `WithBargeIn(0)` self-cancels the turn (the 20 s)

### The wiring

`internal/wirenpc/wirenpc.go:429` wires `orchestrator.WithBargeIn(0)` — a **zero**
confirm window. In `pkg/voice/orchestrator/barge.go:67-71`:

```go
if b.confirm <= 0 {
    b.fire(bus)   // yield the floor the instant speech_start fires
    return
}
```

`fire` → `Floor.Yield()` → cancels the per-turn context
(`pkg/voice/orchestrator/floor.go:63-73`). That context threads through TTS
synthesis (`orchestrator/reactor.go:354` → `TTS.Dispatch` → ElevenLabs
`Synthesize`), so cancelling it aborts the in-flight HTTP POST — **exactly** the
`elevenlabs.Synthesize: ... context canceled` in the 22:36:17 log line.

### Why it fires on the user's *own* voice

ADR-0027 and the `barge.go:23-33` doc assert "an Agent's own TTS never triggers a
barge: only inbound participant audio is VAD'd, so every speech_start here is a
human's." **True but insufficient.** The barge fires on a *human's* speech_start —
and the human is *still talking*. The real sequence in a live channel:

1. User says "Bart, what's a room cost, and have you seen Gandalf?"
2. VAD ends the first speech segment at an internal pause → STT → AddressRouted →
   Bart's turn **takes the floor** (`reactor.go:313`).
3. User continues ("…and have you seen Gandalf?") → fresh **`VADSpeechStart`** —
   floor is active → 0 ms window → **instant `Yield()`** → Bart's turn cancelled,
   TTS POST aborted. No audio.
4. Repeat for every continuation / restart. The user perceives a long silence.
   Eventually a real gap lets one turn run uninterrupted → the 1.599 s survivor.

The guard `if !b.floor.Active()` (`barge.go:64`) only checks *whether* Bart holds
the floor — not *who* is speaking or *whether it's the same utterance that
triggered him*. The single shared VAD session (one session over all participants'
interleaved frames — `barge.go:26-33`) cannot attribute speech to a speaker, so it
**cannot distinguish "a third party interrupted Bart" from "the addressing user is
still finishing their own sentence."** With a 0 ms window the latter always wins.

This is the #1 hypothesis: a concrete mechanism, a matching log line, and a fix
already named in the code (`wirenpc.go:427-428`: "the ~250 ms confirm window is
the next tuning step").

---

## 3. Root cause #2 — per-segment turns + `Floor.Take` supersession

Independent of barge-in. `pkg/voice/orchestrator/stt.go:52-56` publishes **one
`STTFinal` per STT segment, each with a fresh `NewTurnID()`**. Each routed segment
opens a new turn, and `Floor.Take` (`floor.go:33-43`) **supersedes** any turn still
holding the floor — it cancels the prior turn's context.

So if one spoken utterance is VAD-split into two segments (two `STTFinal` → two
`AddressRouted` → two `Take`s), the **second segment's turn cancels the first
segment's turn mid-synthesis** — another `context canceled`, with no barge involved
at all. The `vadMinSilenceFrames` 15→12 change (`wirenpc.go:50-64`) explicitly
lowers the silence hangover; the file's own comment warns that values this low can
split "a single utterance at an internal pause." Lower hangover ⇒ more splits ⇒
more self-cancelling turn pairs.

Either #1 or #2 produces the same observable: a TTS `context canceled`, no audio,
no metric sample, a waiting user. They likely **both** fired that night.

---

## 4. Root cause #3 — the metric boundary and the empty logs (why it was invisible)

### The SLO is survivorship-biased

`response_latency = first FirstAudio − STTFinal.SpeechEndAt`, recorded only when a
`FirstAudio` lands (`subscriber.go:186-195`). A turn that is cancelled, errors in
TTS, or is abandoned **emits nothing**. The series therefore measures *only
successful turns* and is structurally blind to the exact failures that produce the
worst user-perceived latency. A p50 over survivors will always look healthy while
users rage at silence.

### The boundary is also the wrong *end* of the SLO

Even for a survivor, `FirstAudio` is stamped when the first chunk **crosses to the
playback pump** (`wire/tee.go:127-130`), *before* the `play <- chunk` send, codec
encode, Opus framing, Discord jitter buffer, and network tail. The metric measures
"audio handed to the pump," **not "audio audible to the user."** The pump's
real-time 20 ms pacing, the encode, and Discord's buffer all sit *outside* the
measured span. So the SLO boundary is wrong on **both** ends: it ignores
failed/cancelled turns (start side: no sample) and it stops short of audible
(end side: pre-pump). The "≤1.2 s p50 speech-end→first-audio" target
(`voicebench/report.go:35`) is measuring a span the user never experiences.

### The 1.599 < Gemini-floor anomaly

The one recorded turn (1.599 s) is *below* ADR-0035's trivial single-call ttft
(~1.9 s, p50). Two readings, both worth noting: (a) the in-pipeline warm path may
genuinely beat a cold raw call; or (b) `SpeechEndAt` is stamped at the *segment's*
speech-end, which for a split utterance is an *internal* pause — a late, drifting
span start that *undercounts* latency. Either way it reinforces: the SLO start
boundary is not trustworthy, and the numbers it does produce can't be read against
the budget.

### The logs show nothing

Post-Sprint-2 cleanup left INFO nearly empty (the live log is **4 lines** for an
entire session — two of them the cancel/400 WARNs). There is **no per-turn timing
trace**: no speech-end, no address-routed, no per-round LLM start/stop, no
TTSInvoked, no FirstAudio, no turn-cancelled reason. So neither the metric nor the
logs could explain the 20 s. **The thin logs are themselves a primary finding.**

---

## 5. Root cause #4 — baseline latency: H2 multi-round Gemini with `dice` granted

This explains why a *surviving* turn isn't snappy; it does **not** explain 20 s.

`wirenpc.go:395-402` grants the `dice` tool on every turn. The B1 streaming path
runs through `tool.Loop.RunStream` (`pkg/tool/loop.go:86-142`), which streams prose
from **every round in order** — but a round that ends in a tool call emits **no
prose** (`loop.go:73-77`: "the model emits the call with no prose preamble, so in
practice only the final answer's prose is spoken"). So when the model calls a tool:

```
round 0:  Gemini thinking + tool-call emission (no prose)  → dice executes
round 1:  Gemini thinking + final prose                    → first sentence → first audio
```

First audio waits for **round-0 thinking + round-0 generation + tool exec +
round-1 thinking + round-1 first sentence**. At `reasoning_effort:"low"` each round
carries a ~1.9–2.9 s floor (ADR-0035's own trivial-tier numbers), so a tool turn is
plausibly **4–6 s to first audio** before any TTS round-trip. ADR-0035 even names
the trade: `low` *adds* ~1 s to already-fast turns (it's a thinking *floor*, not
just a ceiling). That floor lands on top of the H2 round count.

**Is the cap effective?** ADR-0035's live A/B says yes for the *tail* (reasoning-
bait p95 9.3 s→5.5 s). But that A/B is a **raw single call** — one-line system
prompt, no history, no tools, no orchestrator (ADR-0035:22-23). It does **not**
measure the in-pipeline multi-round path. The cap flattens per-call thinking; it
does nothing about the *number* of rounds. **H1 (per-call thinking) is mitigated;
H2 (round count) is not, and the `dice` grant arms H2 on every turn.**

B1 sentence-streaming **is** genuinely end-to-end (`agent/agent.go:296-303` segments
deltas and dispatches per sentence; the tee publishes FirstAudio on the first chunk
— `tee.go:127-130`). It is **not** buffering the whole completion. So B1 works as
designed; its blind spot is precisely the tool-call round, which streams nothing.

---

## ADR / assumption challenges

- **ADR-0027 (barge-in confirm window).** The "Agent's own TTS never triggers a
  barge" claim is true but creates a false sense of safety. With **one shared VAD
  session** and `confirm=0`, the *addressing user's own continued speech* cancels
  the turn they triggered. The ADR's own multi-speaker caveat (`barge.go:28-33`)
  already says don't tune `confirm>0` without per-participant VAD — but `confirm=0`
  is *worse*, not safer: it removes the only debounce. **Recommendation: never ship
  `confirm=0` against a live mic.** A non-zero window (≥250 ms) is the minimum;
  ideally gate barge on speaker ≠ the turn's addresser once per-participant VAD
  (ADR-0019, deferred) lands.

- **ADR-0019 (single shared VAD session, per-participant deferred).** Deferring
  per-participant VAD is the upstream cause of both #1 and #2: without speaker
  attribution, the orchestrator can't tell "interruption" from "same speaker
  continuing," and can't coalesce one speaker's split segments into one turn.
  This deferral is now load-bearing for latency, not just for multi-speaker
  correctness. **Recommendation: promote it, or add a same-utterance guard.**

- **ADR-0012 / per-segment turn identity.** A fresh `TurnID` + `Floor.Take` per STT
  segment means the *unit of a turn is a VAD segment, not a user utterance*. That's
  fine when segments are utterances; it self-destructs when VAD over-splits. There
  is no "coalesce consecutive segments from the same speaker within N ms" step.
  **Recommendation: add an utterance-assembly / turn-debounce stage** so one
  utterance = one turn.

- **ADR-0032 (observability).** The metric set is the right *shape* but has a
  **survivorship hole**: it counts successes and omits failures. For a real-time
  product the failure/abandon rate is the headline operational signal, and it's
  absent. **Recommendation: add a turn-lifecycle counter** (below). Also reconsider
  the `response_latency` end boundary (pre-pump, not audible).

- **ADR-0035 (thinking cap).** Sound for H1/the tail; **does not address H2**
  (round count). The live A/B is a raw call, not the pipeline — it proves the
  mechanism, not the SLO. **Recommendation: drop the `dice` grant from the default
  voice NPC** (or gate it behind an address-detected dice intent) so the common
  conversational turn is single-round; and/or set `WithThinkingBudget(0)` for the
  reply round to kill the trivial-turn floor the ADR itself flags.

---

## Fix plan (ranked, smallest-blast-radius first)

**P0 — stop the self-cancel (the 20 s).**
1. Set `WithBargeIn(250ms)` (or larger) in `wirenpc.go:429`; **never `0`** live.
   This alone removes the dominant self-cancel: a user finishing their own sentence
   no longer sustains 250 ms of *new* speech past their own pause without it being a
   real interruption.
2. Add a **same-utterance / turn-debounce guard**: coalesce STT segments from the
   same speaker arriving within a short window into one turn, OR suppress barge for
   the first ~N ms after the floor is taken when no *other* speaker is detected.
   This closes root cause #2 (segment-split supersession) which `confirm>0` alone
   does not.

**P1 — make the 20 s visible next time (instrumentation).**
3. **Turn-lifecycle counter** `glyphoxa_voice_turn_total{outcome,reason}` where
   `outcome ∈ {first_audio, cancelled, tts_error, abandoned, max_rounds}` and
   `reason ∈ {barge, supersede, ctx_deadline, provider_4xx, provider_5xx}`. Emit it
   from the same `StageSubscriber` (it already sees STTFinal → no-FirstAudio turns;
   the Sweep at `subscriber.go:224` already finds abandoned turns — count them
   instead of silently reaping). This is the single change that would have shown the
   two failed attempts that night.
4. **Per-turn structured timing log** (one INFO line per turn at turn-end): `turn_id,
   speech_end, address_routed_at, llm_round_count, round_durations[], tts_invoked_at[],
   first_audio_at | cancel_reason`. Restores the per-turn timeline Sprint-2 cleanup
   removed, gated so it's one line/turn (not the old 741-call-site noise).
5. Add a **user-audible end boundary**: stamp a second timestamp when the first Opus
   frame is handed to `Session.Play` (post-encode), so the SLO can distinguish
   "handed to pump" from "on the wire." Keep both; the audible one is the real SLO.

**P1 — baseline latency.**
6. Drop the unconditional `dice` grant from the conversational NPC (or gate it on a
   detected dice intent) so the common turn is single-round → no empty tool-call
   round before first audio.
7. Pre-warm the ElevenLabs + Gemini connections at join time (a tiny no-op / cached
   handshake) so the first real turn doesn't pay cold-connection setup.

**P2 — already landed / verify.**
8. The empty-`eleven_v3`-tag 400 fix (`f744942`) is on this branch; confirm it's the
   live build. It removes the third silent-failure mode (22:36:40).

---

## Reproduction needed to *prove* #1/#2

A definitive root-cause needs a repro that drives the live pipeline with per-stage
timestamps and the **same-speaker-continues** condition the metric can't see.

**Harness design (no paid loop required to prove the cancel mechanism):**

- **Mechanism-level (free, keyless):** a local test that wires the real
  orchestrator + `WithBargeIn(0)` + a `Floor`, feeds **two `VADSpeechStart` events
  with the floor held** (simulating the same speaker continuing), and asserts the
  in-flight turn's context is cancelled and **no `FirstAudio`** is emitted. Same for
  two back-to-back `STTFinal` segments (asserting the second `Floor.Take`
  supersedes the first). This proves #1 and #2 deterministically, against the real
  code, **with zero API spend** — the cancel is pure orchestrator logic. Build this
  first; it converts the hypotheses into a passing/failing assertion.

- **Stage-timestamp repro (one paid live run, coordinated via team-lead):** run the
  live NPC with the P1 per-turn timing log enabled, drive real Gemini + ElevenLabs,
  and have a human deliberately (a) speak one long run-on utterance with internal
  pauses, and (b) speak a clean single utterance with a real gap after. The log will
  show segment-split turns cancelling each other in (a) and a clean survivor in (b),
  with real per-stage ms. This closes the loop on wall-clock and quantifies the H2
  baseline. **Do not run paid loops unsupervised — route the live run through
  team-lead.**

The free mechanism-level repro is the deliverable that *proves* the diagnosis; the
paid run only *quantifies* it.

---

## Coordination

Shared with teammate `code-quality`: the VAD-trigger / `Floor.Take` supersession
race and the `WithBargeIn(0)` self-cancel are squarely a code-level
concurrency/lifecycle issue as well as a concept one — they should confirm the
race at the code level (does a second `Take` reliably cancel the first mid-`Dispatch`,
and is there any window where the cancelled POST's goroutine leaks). The
instrumentation gaps (survivorship metric, empty logs) are the seam between our two
reports.

---

## Implemented (task #1) — and the residual it leaves

Three commits on `lat/audio-process` land the P0/P1 fixes above:

- **P0.1** — `WithBargeIn(0)` → `250ms` (`bargeConfirmWindow`), pinned non-zero
  by a test. Closes root cause #1.
- **P0.2** — a **floor coalesce window** (`NewFloorWithCoalesce` /
  `WithBargeInCoalesce`, 600 ms live): a `Floor.Take` landing within the window of
  the previous one **yields to** the in-flight turn instead of superseding it.
  Closes the root-cause-#2 self-cancel without restructuring per-segment turn
  identity (the floor-seam debounce, chosen over a Segmenter utterance-assembly
  stage which would tangle with the locked S2 turn-identity seam).
- **P1.3/P1.4** — `glyphoxa_voice_turn_total{outcome,reason}` (the survivorship
  counterpart) + `WithTurnLog` one-line-per-turn timing trace.

**Known residual — the coalesced segment's text is dropped.** The coalesce window
saves the *first* segment's turn from cancellation, but it does so by **yielding
the late segment** — that segment is never spoken, so when VAD over-splits
"*Bart, what's a room cost…*" / "*…and have you seen Gandalf?*", Bart answers only
the **first half**. The fix is deliberately *visible*, not silent: the yielded
segment emits `voiceevent.TurnEnded{Reason: supersede_coalesced, Text}`, which the
metrics subscriber records as a **distinct
`turn_total{outcome="yielded",reason="supersession_grace"}`** (not `abandoned`)
and logs at INFO **with the dropped transcript** (`yielded_text=…`). So the
over-split rate and the exact text lost are both measurable.

This is the data the *next* iteration needs to justify **real utterance
coalescing**: instead of dropping the late segment, assemble consecutive
same-speaker segments into one turn (or route the late segment's text into the
in-flight turn's Hot Context before its LLM call) so one utterance = one *complete*
turn. That belongs to the S2 Hot Context seam and per-participant VAD (ADR-0019),
not the floor — out of task #1's blast radius, tracked as a follow-up.

**Turn-end reasons are now precise (task #4).** The single `voiceevent.TurnEnded`
event carries a bounded `Reason` published by the seam that knows the cause —
`barge` (the `BargeIn`, via the `Floor` now returning the cut turn's `TurnID` from
`Yield`), `supersede_coalesced` (the `Replier`'s coalesced branch), and
`tts_error` / `provider_error` (the `Replier`'s dispatch / producer error paths
when the turn dies of its own error before audio). The subscriber maps these to
`turn_total{outcome,reason}`, so `no_first_audio` is now only the fallback for a
turn that vanished with **no** signal at all (TTL-reaped). A `TurnEnded` arriving
*after* first audio (a barge mid-playback) is a normal interruption and does not
re-count the turn (first-audio is terminal).
