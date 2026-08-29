# Idle Close: a Voice Instance ends the Voice Sessions nothing is using

The first production deployment on glyphoxa.com left a Voice Session running for a full day. The Rollover Tape was armed, and **nobody was ever in the voice channel with the Bot.** Nothing in the system could end it: a Voice Session ends only when a GM presses End, when the process shuts down, when the gateway fails fatally, or when the ADR-0046 hard spend cap trips — and an empty channel triggers none of those. A session with no audio spends almost nothing, so the cost gate never fires; it just holds resources.

What it held for 24 hours: a Discord voice connection with its DAVE/MLS state, a claim-plane slot and its heartbeat, a 120-second tape ring plus every buffered Opus payload for each consented Speaker Lane, a tape-consent poller re-reading the durable rows every 5 seconds (~17 000 queries), a Silero VAD session per lane, and — with streaming STT — a realtime websocket against a metered provider. None of it is bounded by a silent room.

## What this decides

- **A Voice Session that has processed no audio for the Idle Close Window is closed by the Voice Instance hosting it.** Default 15 minutes, config-tunable, **on by default** — a deployment that configures nothing is protected.
- **Audio means audio the session actually processed**: an inbound room-audio frame, or the Bot being asked to speak (a GM `/say`, a voiced recap, a Highlight replay — the three ways the Bot talks with nobody talking to it). Discord's Opus *silence* frames do not count: they are the signal that a speaker STOPPED.
- **The signal is a counter, not a timestamp.** The audio loop does one atomic increment per frame; the watchdog samples the counter on its own cadence and reads the clock there. Nothing on the hot path takes a lock, allocates, or reads a clock.
- **A Voice Session past its reconnect-cycle ceiling is closed as churning.** Default 200 cycles, on by default. This is the one *per-session*, *exact* resource measurement available (see below).
- **Two opt-in process ceilings — heap footprint and goroutine count — shed the single least-recently-active Voice Session on the instance,** re-checking on the next sweep. Both **off by default**.
- **An Idle Close is a policy stop, not a fault.** The row closes `ended` with an `end_reason` (ADR-0043 prefixes `idle_no_audio`, `reconnect_churn`, `resource_ceiling`), exactly as ADR-0046's hard cap does. `failed` stays reserved for faults.
- **Only the hosting Voice Instance closes its own sessions.** The decision is made and executed inside the process holding the DAVE/MLS session, through the same `endReasonOverride` + `cancel()` mechanism the hard cap uses. No sweeper reaches across the fleet (ADR-0006, ADR-0057 (e)).
- **A policy `end_reason` now also reaches the claim plane.** `finishSelfExit` previously copied `end_reason` onto `voice_session_intents.last_error` only for `failed` rows, so a hard cap — and now an Idle Close — was indistinguishable there from a GM pressing End.

## Why the resource guard is shaped this way

"Close it when it leaks too many resources" is easy to say and hard to measure honestly. What is actually available:

- **Per-session heap or goroutines: not measurable.** Go attributes no memory to a logical owner. A per-session number would be invented.
- **Live Speaker Lanes, tape ring bytes, open STT websockets, FDs: not measured.** No counter exists for any of them; each needs new plumbing through `internal/tape` and `pkg/voice/orchestrator`.
- **The Prometheus instruments: not readable per session.** ADR-0032 forbids `tenant_id`/`session_id` labels precisely so cardinality stays bounded.
- **Session age: not a leak signal.** An all-day campaign is legitimate. Guarding on it would punish the users we built this for.
- **Reconnect-cycle count: per-session, monotonic, exact, one atomic increment.** Every cycle rebuilds the per-cycle world — the voice connection, the codec, each provider adapter with its own `http.Transport` (no `IdleConnTimeout` is set anywhere, and `CloseIdleConnections` is never called), a Silero session per lane, and with streaming STT a fresh realtime websocket — and not all of it comes back. A session that has cycled hundreds of times has leaked in proportion. This is the guard that ships on by default.

The two **process** ceilings are the deliberate exception to "per-session only", and their limit is stated plainly rather than hidden: they read the whole process, so with `MaxSessions > 1` one Tenant's pressure can cost another Tenant their session. That is why they are opt-in and why the victim is the *quietest* session rather than an arbitrary one. The alternative when a pod reaches its memory limit is an OOM kill that takes every session with it; shedding the least-used one first is strictly better, and it is a valve an operator sizes against their own pod limits.

## Considered and rejected

- **Watch voice-channel occupancy instead ("the Bot is alone").** More precise for the reported incident, and the gateway already carries the data (`IntentGuildVoiceStates`). Rejected as the *primary* signal: it needs a new per-cycle event listener and cache read on a path that has none today, it says nothing about a table that is present but has stopped producing audio, and it would not have caught the churn case at all. Worth revisiting as an additional fast-path trigger.
- **A maximum Voice Session lifetime.** Simple and bounds everything — and closes real all-day campaigns. ADR-0046's hard spend cap already ends genuinely-active runaway sessions on the axis that matters (cost), so a wall-clock cap adds a way to lose a session that is working correctly.
- **Closing on the existing `InboundFramesDropped` / `InboundUndecodableFrame` counters.** They are already per-session-copyable via the recorder tee. Rejected: a GC pause or a DAVE key roll produces a legitimate burst, and picking a non-arbitrary threshold needs production data nobody has yet.
- **Making the idle signal a timestamp stamped on the audio path.** One `time.Now()` per inbound packet (~50/s per speaker) buys nothing a counter does not: the watchdog needs "did this move since the last sweep", and it already holds a clock.
- **Keying idleness off the VAD, the Segmenter, or the lane sweep.** All three are driven by the synthesized silence clock, which ticks at ~31 Hz through a completely empty channel forever. A signal taken from any of them would never go stale — the feature would be dead in production while its tests passed.
- **Letting the audio loop end the session by returning.** `runWithReconnect` treats both an error and a clean `nil` from a cycle as a dropped connection and reconnects after backoff. A session "closed" that way rejoins the empty channel a second later.
- **Publishing an Idle Close on the voice event bus.** The relay already emits a terminal frame when a session ends, and an Idle Close is by construction a session with nobody watching. The `end_reason` on the row is the durable record; the watchdog logs the decision.
- **Per-Campaign or per-Tenant configuration.** This is a deployment-resource protection, the same class as `GLYPHOXA_MAX_VOICE_SESSIONS` — not a tenant preference like a spend cap, which is the tenant's own money.

## Configuration

| Env var | Default | Meaning |
|---|---|---|
| `GLYPHOXA_VOICE_IDLE_CLOSE_WINDOW` | `15m` | No-audio window. The literal `off` disables idle closing. |
| `GLYPHOXA_VOICE_IDLE_CLOSE_SWEEP` | `30s` | Watchdog cadence. |
| `GLYPHOXA_VOICE_MAX_CONNECT_CYCLES` | `200` | Reconnect-churn ceiling; `0` disables. |
| `GLYPHOXA_VOICE_HEAP_CEILING_MIB` | `0` (off) | Process heap footprint ceiling. |
| `GLYPHOXA_VOICE_GOROUTINE_CEILING` | `0` (off) | Process goroutine ceiling. |

Parsed in the composition root (`cmd/glyphoxa/boot.go`), like every other `GLYPHOXA_VOICE_*` knob. The duration knobs take a **word** (`off`) to disable rather than `0`: `envDuration`'s standing contract is that a blank, unparsable or non-positive value falls back to the default, which is right for a cadence and wrong for a protection — a typo must never silently switch a resource guard off. The integer ceilings honour an explicit `0` as "disabled", the `GLYPHOXA_STT_STREAM_MAX_LANES` convention.

## Known trade-off

A table that is present but produces no audio for the whole window — everyone muted through a long break, a stretch of play-by-text — **is** closed. That is the cost of the signal being audio rather than occupancy. 15 minutes is generous against real tables, the window is tunable per deployment, and a GM restarts with one command. Deployments whose groups take long silent breaks should raise it; the alternative rejected above (occupancy) is the natural follow-up if this proves too blunt.

## Relationship to other ADRs

ADR-0046 (the hard spend cap — the mechanism this copies verbatim: a fresh goroutine, `endReasonOverride` under the Manager lock, publish and cancel outside it, `ended` not `failed`), ADR-0043 (`end_reason` is a stable machine prefix plus prose — this adds three prefixes and, for the first time, propagates a policy reason onto the claim-plane row), ADR-0006 and ADR-0057 (e) (only the owning Voice Instance ends its own session; no cross-instance kill), ADR-0051 (an Idle Close is a Voice Session end like any other, so the tape is discarded wholesale and the Highlight-candidate purge horizon is scheduled by the same finalizers), ADR-0032 (the watchdog logs its decisions; it adds no new metric label), ADR-0050 (a Speaker Lane's idle reap retires ONE lane inside a still-live Voice Session — a different mechanism at a different grain, and the reason this is called Idle *Close*).

## Amendment: a fourth reason, `media_path_dead` (2026-08-29, #633)

The incident this watchdog existed for arrived wearing its clothes: a dead
Discord media path (no UDP keepalive in disgo — the transport went silently
deaf after ~13–15 outbound-quiet minutes) froze the activity marks, and Idle
Close reaped the live table 15 minutes later as `idle_no_audio`. ADR-0064 adds
the transport keepalive and a receive-side media watchdog; this amendment adds
the honest close reason. The watchdog flags the session handle
(`Session.MediaSuspect`) when it observes remote Speaking announcements with no
RTP arriving; an idle breach with the flag standing closes as
`media_path_dead: the Voice Session received no audio while participants kept speaking`.
Moving audio clears the flag on the next sweep, churn still outranks both, and
everything downstream (Manager, storage, RPC, claim plane, bundle) already
carries any reason string verbatim. Prod rows written before this amendment may
record dead-media closes as `idle_no_audio`.
