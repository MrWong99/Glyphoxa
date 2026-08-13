# Butler planning chat: Connect streaming, persisted threads, per-exchange metering under the allowance gate

The Butler planning chat (#592, third slice of E9) needed ADR-level answers before implementation: the issue captured the shape and four open decisions, plus the transport choice it left to "the ADR". Decided with the operator 2026-08-13; this ADR records them. The surface itself is as #592 proposes: the Campaign's Butler (ADR-0009) reachable as a GM-only chat from the Campaign screen — same persona, same Agent row, chat Tool Grants as separate rows from voice grants (ADR-0029), tool belt `search_transcripts` / `find_node` / `appearances_of` (#591), `propose_knowledge` (ADR-0052), and a `recap` tool reusing #271's generator.

## What this decides

- **Transport: a server-streaming Connect RPC**, not SSE. Chat is a per-request
  response stream, which is exactly what a server-streaming method on the
  existing Connect stack (ADR-0015) models: typed stream frames (token deltas,
  tool-call activity, exchange done), the same auth/interceptor chain as every
  other RPC, no second wire surface. The ADR-0014 SSE relay stays what it is —
  fan-out of session events — and gains no chat traffic.
- **Metering: per-exchange flush, allowance-gated.** Chat happens with no Voice
  Session, so ADR-0045's flush-at-session-end boundary and ADR-0046's live cap
  don't exist here. Each chat exchange builds its own caps-free
  `spend.NewMeter(spend.Caps{}, …)` teed via `observe.TeeUsage` — the ADR-0046
  recap-amendment pattern — and flushes to the Usage Ledger **per exchange**,
  attributed to the Tenant (and thread/campaign) rather than to a Voice
  Session. On platform keys, the ADR-0055 monthly allowance gate
  (month-to-date estimated USD vs `plan.included_usage_usd`) is checked
  **before each exchange starts**; a refused exchange is a clear in-chat
  refusal, not a silent drop. This satisfies #592's "must before public-beta"
  condition from day one. BYOK tenants are metered identically but not
  allowance-gated, matching ADR-0055's platform-key scoping. ADR-0046's
  soft/hard live-cap mechanics remain exclusively the running Voice Session's
  concern (amendment note added there).
- **Threads are persisted, with full data citizenship in v1.** Planning
  threads are new per-Campaign data (thread + message tables), surviving
  reloads and spanning prep sessions — not the ephemeral Recap posture (#271).
  Persistence carries its obligations immediately rather than as documented
  debt: threads join the Campaign Bundle (ADR-0053; a format-version bump with
  the same `omitempty`/range-accept compatibility discipline as v2) and are
  deleted by the campaign deletion sweep from day one. No orphaned prep
  conversations, no export blind spot discovered at restore time.
- **Model selection: a separate chat-scoped slot.** The voice Butler stays on
  the latency-optimized ADR-0036 default; the Agent's provider config grows a
  second, chat-scoped provider/model selection whose default is chosen for
  quality over latency. The existing Provider Config component/UI is reused —
  one more entry, not a new component. A GM upgrading chat quality can no
  longer accidentally slow down (or re-price) the live voice loop.
- **Prompt context: chat gets its own stable-prefix layout.** The ADR-0059
  *principle* (stable prefix, cache-friendly) carries over; the voice layout
  does not. Stable prefix = Persona + chat-tool instructions; the thread
  history appends after it; there is **no** speaker roster and **no** volatile
  facts/memory/location/directive tail. World knowledge arrives through the
  tool calls (`search_transcripts`, `find_node`, `appearances_of`) instead of
  pushed prompt blocks — that is the point of the tool belt, and it keeps the
  prefix stable for provider-side prompt caching without per-message forks.
- **GM-only v1** (as proposed in #592): the chat endpoint requires the gm
  Member Role — the matcher-side GM gate's web equivalent.

## Considered and rejected

- **SSE for the chat stream** — familiar plumbing from the Session screen, but
  it is designed there for event fan-out; chat would add a second, untyped
  wire surface where a typed Connect stream already exists end-to-end.
- **Idle-timeout flush boundary** — a chat-session analogue of the Voice
  Session boundary; fewer ledger rows, but in-memory usage is lost on crash
  and the allowance gate lags actual spend within a thread. Per-exchange is
  crash-safe and needs no idle-timer machinery.
- **Meter only, defer gating (pure recap posture)** — least work now, but
  #592 itself makes the allowance gate a precondition for public-beta tenants;
  deferring it turns a decided requirement into a blocking follow-up.
- **Ephemeral threads (Recap posture)** — smaller v1 with no export/deletion
  implications, but prep is exactly the workflow where losing yesterday's
  thread hurts; chosen posture is persist-and-own-the-obligations.
- **Sweep-yes-export-later for thread data** — a known export blind spot of
  the kind the ADR-0053 v2 amendment exists to prevent ("a silent omission in
  a backup is discovered only when someone restores").
- **Reusing the voice Provider Config as-is** — zero new schema, but chat
  inherits the latency-optimized model and any chat-motivated change also
  changes the live voice loop.
- **Reusing the Hot Context layout wholesale** — the volatile tail would fork
  the cached prefix on every message and push content the chat tools already
  retrieve on demand.

## Relationship to other ADRs

- **ADR-0009 / 0028 / 0029** — the Butler is the agent; chat tools go through
  the existing tool framework; chat Tool Grants are separate least-privilege
  rows beside the voice grants.
- **ADR-0045 / 0046** — usage capture points reused; the per-exchange
  caps-free meter follows ADR-0046's recap amendment pattern, and a note there
  records chat as the second off-session consumer. Live cap mechanics stay
  Voice-Session-only.
- **ADR-0054 / 0055** — the Usage Ledger stays attribution-only; the ADR-0055
  allowance gate is the gating mechanism and gains the chat exchange as a
  gated surface on platform keys.
- **ADR-0014 / 0015** — chat streams over Connect end-to-end; the SSE relay is
  unchanged.
- **ADR-0036** — the voice default is untouched; the chat slot is a separate
  selection recorded here.
- **ADR-0059** — its stable-prefix rationale applies; its voice layout
  (roster, volatile tail) deliberately does not carry over.
- **ADR-0052** — `propose_knowledge` writes stay Knowledge Proposals under GM
  approval; chat adds no direct KG write path.
- **ADR-0053** — planning threads join the bundle via a format-version bump;
  the format amendment lands there with the implementation slice, under the
  v2 compatibility discipline.
- **#591 / #271** — the campaign search API is the tool belt's dependency;
  the recap generator is reused as a chat tool.
