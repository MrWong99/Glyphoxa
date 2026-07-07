# Per-session spend meter: ownership, price map, and cap mechanics

Implementing #130 (E6, under the ADR-0004 amendment) required deciding where the per-session spend accumulator lives, where prices come from, and the precise soft/hard-cap semantics. The operator delegated these decisions to the implementation run (2026-07-07); this ADR records them.

## What this decides

- **`internal/spend.Meter` implements `observe.UsageSink`** (the three usage methods from ADR-0045) and is teed into the session's recorder via `observe.TeeUsage(base, sink)` at `session.Manager.Start` — zero new plumbing through the voice pipeline; the meter rides the existing recorder config copy. No caps configured → no meter, no tee, no gate: byte-for-byte today's behavior.
- **The session manager owns cap consequences.** The meter takes `onSoft`/`onHard` callbacks (each fires once, outside the meter mutex, never blocking). Soft: publish `SpendCapReached{soft}`; the orchestrator `TurnGate` (`AllowTurn() bool`, wired as a replier pre-check beside the mute check) refuses *new* turns — in-flight turns complete, transcription continues. Hard: publish `SpendCapReached{hard}` and cancel the session context on a fresh goroutine (avoids lock-order deadlocks, #211 pattern); the row closes via `CloseVoiceSession` (ADR-0043) with status **`ended`** — a deliberate policy stop, not a failure — and `end_reason` prefix `spend_cap_hard`.
- **Prices are code constants** (`internal/spend/prices.go`), keyed `(component, provider, model)`, each entry carrying a source comment and date, all surfaced numbers labelled *estimates*. Unknown key → conservative documented default plus a warn-once log. A DB/config price surface is deferred until someone actually needs to edit prices without a deploy.
- **Caps are per-Tenant nullable columns** (`spend_cap_soft_usd`, `spend_cap_hard_usd`, migration; either alone valid, both ⇒ hard ≥ soft, enforced at the RPC). New `GetSpendCaps`/`SetSpendCaps` RPCs; `GetSessionResponse` gains `spend_cap_state` and `estimated_spend_usd`. Caps snapshot at session start; edits apply to the next session.
- **Refused turns are observable:** `TurnEnded` reason `spend_cap` maps to `TurnOutcome(abandoned, spend_cap)`; the relay forwards `SpendCapReached` as a `spendcap` SSE frame for the Session screen.
- **Mutex, not atomics**, guards the accumulator: float math plus threshold-callback dispatch under one small lock beats a lock-free scheme nobody can review.

## Considered and rejected

- **Prometheus per-session labels for spend** — forbidden by ADR-0032's cardinality bound; the accumulator is session-local state, not a metric.
- **Gating inside the address detector or segmenter** — would silence transcription; the AC requires transcription to continue under a soft cap.
- **Failed-status rows on hard cap** — the session did exactly what it was configured to do; `failed` is reserved for faults (ADR-0043).

## Relationship to other ADRs

ADR-0004 (amendment is the spec this implements), ADR-0045 (usage capture points), ADR-0043 (close seam + end_reason prefixes), ADR-0032 (no session labels), ADR-0020/0014/0039 (event + SSE + screen surface).
