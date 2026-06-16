# Barge-in: per-participant confirm window cancels the whole turn

A human **Barge-in** is a policy layer over the existing VAD events, not new VAD machinery. It subscribes to the orchestrator's `vad.speech_start`/`vad.speech_end` (republished from the per-participant Silero session, ADR-0019) and yields the floor when a human reclaims it while an Agent is speaking. Humans always have the upper hand: NPCs are set dressing for a game humans play with each other, so they never steal or hold the floor against a person.

## Trigger — the Agent must be audibly speaking, then a confirm window

Yielding has **two** gates, in order.

**Gate 1 — the Agent is audibly speaking.** A barge can only fire once the held turn has put its first Opus packet on the wire (`voice.first_opus`); a turn that merely *holds the floor* — the pre-audio phase where it is still assembling Hot Context and waiting on the LLM — is **not** yet speaking and cannot be barged. The floor is taken at `address.routed`, seconds before any sound, so "holds the floor" ≠ "is speaking." Gating on floor-held instead of audibly-speaking was a real self-cancel bug: under the single shared VAD session (below) the addressing user's *own* continued speech — or a VAD over-split of one utterance into a second segment — fires a fresh `speech_start` while the turn is still thinking, which yielded the floor and cancelled the turn before it ever made a sound. The result was an NPC that produced **no audible response at all** (`turn.ended reason=barge`, `no_audio=true`, TTS `context canceled`). The same-utterance *coalesce* window guards only the `Floor.Take` (supersession) path; the barge path needs its own gate, and "is the Agent audible on the wire" is it. This mirrors the ADR's own rule that an Agent's own TTS cannot trigger a barge: before `first_opus` the human has heard nothing, so their speech is their own utterance, not a reaction to the Agent.

**Gate 2 — confirm window.** Utterance *capture* already fires fast (Silero `minSpeechFrames`, ~90ms at 30ms frames). Floor *yielding* uses a second, slower knob: a **barge-in confirm window** (default ~250ms of continuous voiced speech, measured from `speech_start`) applied once Gate 1 holds. Speech that ends before the window is **soft-overlap** (a backchannel — "mhm", "yeah", a cough); it emits an observability event but does **not** cancel the Agent. Speech that crosses the window emits `barge.detected` and cancels.

- **Per-Agent sensitivity.** The confirm window is a per-Agent tunable with a deployment default. Polarity is intuitive: a *longer* window = harder to interrupt (the loud roughian talks over you); a *shorter* window = yields at the first "um" (the shy fairy). It lives alongside the Agent's other per-persona knobs (Persona, Voice, `AddressOnly`).
- **Per-participant, never aggregate.** Each Discord user has an independent VAD session (per-user Opus streams). The window is measured per participant; a barge-in fires when *one* participant's continuous speech crosses it. Parallel backchannels from several people never sum into a false trigger. This also yields the correct `interrupted_by_user_id` for free.
- **Any human may barge in** — barge-in is a floor-control signal independent of Address Detection, not restricted to the addressed speaker or the GM. An Agent's own TTS cannot trigger it (Agents are not VAD'd as participants).

## Cancel mechanics — hard cut at the forward boundary

On `barge.detected` the Agent's turn is torn down immediately, reusing ADR-0012's commit rule unchanged (delivered = last opus frame **forwarded** to Discord):

1. Stop forwarding opus frames at once — no buffer drain, no finishing the current sentence. The earlier the cut, the more human it feels.
2. Sentences whose last frame was forwarded before the cut commit (ADR-0012); the in-flight sentence and any pre-rendered, not-yet-forwarded audio are discarded (not delivered → not committed). Upstream TTS stream and the still-running LLM generation are cancelled.
3. Stamp `was_interrupted=true`, `interrupted_by_user_id` = the participant whose `speech_start` opened the window. Zero delivered → not logged (ADR-0012).

A few-frame cosmetic fade-out (to avoid a click) is deferred polish; it does not touch commit semantics.

## Soft-overlap is still transcribed

The confirm window gates **floor-yielding only**, not transcription. A sub-threshold backchannel runs the normal pipeline (STT → Address Detection, which names no Agent → no-target → committed as the participant's utterance). A "yeah!" in the Transcript is genuine context for the NPC's next turn. No special-case suppression in the transcript path.

## Recovery — none; emergent from the normal loop

A cancelled Agent stays silent. There is **no auto-resume and no re-attempt machinery.** What happens next is just the pipeline: the human's interruption is its own utterance → Address Detection → maybe routes back to that Agent, maybe elsewhere, maybe no-target. If it re-addresses the Agent, it generates fresh, with the interruption (and its own partial delivered sentences) now in Hot Context — so "as I was saying…" is *emergent if the persona warrants it*, never scripted. Auto-resume is rejected: a barge-in is usually a redirect, and resuming replays a thought generated against now-stale context.

## Ensemble Turns are one floor-holding unit

A barge-in tears down the **entire** Ensemble Turn (ADR-0025), not just the Lead. Whether it lands during Lead playback, in the gap before the Reaction, or during Reaction playback, the same rule applies: whatever is forwarding hard-cuts, whatever is queued (a pre-rendered Reaction) is discarded. Delivered sentences from either the Lead or the Reaction commit per ADR-0012; the rest is dropped. Letting a queued Reaction fire after the human spoke is rejected — it would respond to a Lead the human just cut off, oblivious to the interruption, which is the exact floor-stealing behaviour barge-in exists to prevent. The orchestrator treats an Ensemble Turn as one turn even though it commits up to two utterances.

## Deferred (v1.5+)

- **Interrupted-Agent priority bias.** Address Detection (ADR-0024) gives a recently-interrupted Agent a small priority multiplier in the last-speaker-continuation stage, capturing the social fact that we cut someone off but still expect them to finish.
- **Cosmetic fade-out** on hard cut (above).

## Considered options

- **Fire-fast at the 90ms capture onset** — rejected; every backchannel would kill the Agent mid-sentence. The second (confirm) knob exists precisely to separate capture from floor-yielding.
- **Finish the current sentence before yielding** — rejected; robotic, and defeats the point of barge-in.
- **Preserve / re-evaluate the queued Reaction after a barge-in** — rejected; its benefit (awareness of the interruption) is already delivered by tearing down the turn and letting the normal Hot Context loop regenerate if re-addressed.
- **Aggregate room-energy trigger** — rejected; per-participant streams make summing false-positives unnecessary and give `interrupted_by_user_id` for free.
