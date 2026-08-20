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

## Amendment (2026-07-09, #272 recap engine)

The cap mechanics above are **live-Voice-Session-only**. The Recap engine
(`internal/recap`, E3) is an *idle* off-session LLM call — it summarizes a past
session and does not belong to any running session's meter. Its metering posture
(gate #271):

- **Metered, never cap-gated.** Each recap builds its own caps-free
  `spend.NewMeter(spend.Caps{}, …)` teed alongside the production recorder via
  `observe.TeeUsage`, so usage is priced and the `glyphoxa_voice_llm_tokens_total`
  series still moves. The engine **never** reads a tenant cap and **never** calls
  `AllowTurn`: `recap.Store` deliberately omits any spend-cap method, so the code
  *cannot* gate on a cap. A recap is never refused for spend — the soft/hard-cap
  consequences here are exclusively the live session's concern.
- **Usage attributed to the recapped Voice Session id(s).** Beyond the counters,
  the engine emits one structured `recap: llm usage` log line carrying the
  `voice_session_ids`, the input/output token totals, and the meter's
  `EstimatedUSD`, so an operator can attribute recap spend to the session(s) it
  summarized.

This is additive: the live-session meter, caps, gate, and `SpendCapReached`
mechanics are unchanged.

## Amendment (2026-08-13, #592 Butler planning chat)

The Butler planning chat (ADR-0062) is the second off-session LLM consumer
after Recap, and it extends the posture above in one respect: each chat
exchange builds the same caps-free `spend.NewMeter(spend.Caps{}, …)` teed via
`observe.TeeUsage` and is **metered per exchange with Tenant attribution**,
but — unlike Recap — it **is gated**: the ADR-0055 monthly allowance gate is
checked before each exchange on platform keys. That gate is ADR-0055's
mechanism reading the Usage Ledger, not this ADR's live cap: the soft/hard-cap
mechanics, `AllowTurn`, and `SpendCapReached` remain exclusively the running
Voice Session's concern, unchanged.

## Amendment (2026-08-18, #312 Highlight sound enrichment)

Highlight sound generation (ElevenLabs SFX stings and Music tracks, ADR-0004
amendment 2026-07-22) is the third off-session consumer, and it introduces the
price map's first **sound** rows: `internal/spend/prices.go` gains a
model-keyed `soundPricePerMinute` map — the SFX and Music model ids price
separately, per the ADR-0004 amendment's attribution requirement — with the
usual conservative (higher-than-any-known-row) fallback for an unknown model.
Its metering posture:

- **Estimated directly, never a Meter capture point.** The ADR-0045 usage trio
  (LLM/TTS/STT) carries no sound kind, and caps are live-Voice-Session-only —
  an off-session enrichment job is never cap-gated (the Recap posture). The
  new `spend.EstimateSoundUSD(provider, model, duration)` prices the
  REQUESTED audio duration (the one quantity known before the vendor bill),
  the embeddings-estimate pattern.
- **Usage-Ledger attribution per generation.** The enrichment job flushes one
  `billing.Ledger` row per generation with Tenant attribution: component
  `tts` (the Provider Config the call rode — deliberately no `sound`
  Component), the SFX/Music model id as discriminator, the vendor's
  `character-cost` header as the quantity (its own credit unit; 0 when
  unreported), and the duration-priced estimate. The planning-chat
  per-exchange flush pattern.

The live-session meter, caps, gate, and `SpendCapReached` mechanics are
unchanged.

## Relationship to other ADRs

ADR-0004 (amendment is the spec this implements), ADR-0045 (usage capture points), ADR-0043 (close seam + end_reason prefixes), ADR-0032 (no session labels), ADR-0020/0014/0039 (event + SSE + screen surface).
