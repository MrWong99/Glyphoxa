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
- **Model selection: a separate chat-scoped slot** *(reshaped by the amendment
  of 2026-11-22 below: a Tenant choice between providers, not one more Provider
  Config entry)*. The voice Butler stays on
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

## Amendment: the chat model is a tenant choice between providers (2026-11-22)

This replaces the "one more Provider Config entry" reading of the *Model
selection* bullet above. That bullet was built as a second Groq key slot, and
the slot turned out wrong in four ways: the Configuration screen described it
from the Groq key while the engine actually answered with whatever voice ran
(in production: "Planning chat (Groq) / openai/gpt-oss-120b / Included" over a
chat running Claude Haiku); a Tenant on the Platform Keys could not pick a chat
model at all (a model with no stored key was refused); a BYOK Tenant had to
paste the same Groq key a second time; and nothing led back to "same as voice".

### What this decides

- **The Planning chat model is one Tenant-level choice: a provider and a
  model.** It is stored as the Tenant's single `chat_llm` Provider Config. No
  row means "same as voice". It is a choice, not a credential slot: it leaves
  the credential list of the Configuration read and gets its own save, delete
  and read (ADR-0039 amendment of the same date).
- **Resolution order**, one pure function (`llmbuild.DeploymentDefault.SelectChat`)
  shared by the chat engine, the Configuration read and the chat model catalog,
  so the screen cannot name a model the engine does not run:
  1. the Butler-bound row (`agents.chat_llm_provider_config_id`) — reserved,
     nothing writes it;
  2. the Tenant's chat choice;
  3. the Tenant's voice `llm` row — "same as voice", the model and key a Voice
     Session runs;
  4. the deployment default (ADR-0036).
- **Key ladder of a chat choice:** a key saved for the chat alone, else the
  Tenant's own key in that provider's LLM slot, else the Platform Key behind
  the entitlement (ADR-0054/0055). **There is no fallback to another
  provider.** With no key on any rung the exchange is refused
  (`ErrNoPlatformKeyEntitlement`, the existing in-chat "no key" notice) and the
  screen says the chat needs a key. A chat-only key is stored on the chat row
  and never becomes the Tenant's voice LLM.
- **Providers offered:** Groq and Anthropic. Gemini is reachable for chat only
  through "same as voice" or the deployment default.
- **Defaults and tuning.** A chat choice that names no model runs the
  provider's *chat* default: `claude-sonnet-5-5` on Anthropic, the voice
  default on Groq. On Anthropic the chat thinks under its own policy —
  adaptive thinking at effort `medium` on every model family that accepts it,
  nothing on `claude-haiku-4-5` — and a reply is capped at 16000 tokens,
  because thinking tokens count against `max_tokens`; other providers keep
  4096. Quality over latency: this is prep, not a spoken turn. The prompt-cache
  gate and the thinking display are unchanged.
- **Priced models only on the Platform Key** (ADR-0036 rule): a chat choice
  that would run on the Platform Key may store no model or a priced one. The
  engine checks the same rule again on every exchange, because a keyless
  choice borrows the Tenant's key in that provider's LLM slot dynamically: if
  that key is gone, an unpriced model is refused rather than metered at the
  flat fallback rate. The ADR-0055 allowance gate before each exchange stays
  the only spend gate.
- **The recap tool keeps the voice LLM.** A recap requested inside a chat
  exchange runs through the shared recap engine on the Tenant's voice model;
  it is metered into the same exchange.

### Considered and rejected

- **A model picker per thread or per Campaign.** Per thread needs
  `planning_thread` columns and a Campaign Bundle format bump (ADR-0053). Per
  Campaign cannot be expressed by pointing the Butler binding at shared rows,
  because the model lives on the row. The Butler rung stays reserved for it.
- **A second chat slot per provider with newest-wins**, the voice pattern: it
  reproduces "saving a key switches the model" and "no way back" (#712 items 1
  and 2) and doubles key pasting.
- **A deployment-level chat default env.** A follow-up if an operator wants
  every Tenant's chat on one model by default.

### Consequences

- No migration. At most one `chat_llm` row per Tenant is kept by the single
  writer (it removes the other providers' rows in the same transaction), not
  by a database constraint: two concurrent saves can leave two rows, readers
  take the newest and the next save removes the other.
- A model-only save never re-writes a stored key (the credential columns are
  not part of that update), which closes the #712 item 3 shape for chat.
- A Tenant that put its chat on a Platform-Key provider and later moves to a
  BYOK plan gets the "no key" refusal in chat and on the screen. It does not
  fall back to its voice provider. This is deliberate and visible.
- `effective_chat_llm` is Tenant-scoped and ignores the Butler rung. Whoever
  gives that rung a writer must make the read Campaign-scoped.
