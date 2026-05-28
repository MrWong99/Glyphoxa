# Tool side effects: read-only run inline, side-effecting flush at turn-commit

Each Tool declares one bit: **read-only** or **side-effecting**. This bit is the discriminator for *when* a Tool's effect happens relative to a real-time voice turn (ADR-0019), and it is the only side-effect machinery built now (`dice` = read-only).

## The hazard

A tool call forces a second LLM round-trip mid-turn (generate → `tool_call` → execute → feed back → generate final), before the Agent takes the floor. Two locked ADRs make a *side-effecting* tool dangerous on this path:

- **ADR-0025 (speculative ensemble):** all addressed Agents generate in parallel; losers' drafts are discarded. A side-effecting tool in a discarded draft has already mutated state — the KG would hold facts from utterances the room never heard, exactly the pollution ADR-0012 (zero-delivered → not logged) exists to prevent.
- **ADR-0027 (barge-in):** a human interruption discards in-flight generation; a write mid-turn may have already landed (delivered=0, yet state changed).

## Decision

- **Read-only tools** (`dice`, future `query_knowledge`): execute **instantly**, during generation and speculation. The LLM needs read results mid-generation, and reads are safe to speculate.
- **Side-effecting tools** (future `remember_knowledge`): during generation, **record the intent** (tool name + args + grantConfig) and hand the LLM an optimistic result ("saved"); **execute for real only at turn-commit** — ADR-0012's deliver-then-commit rule, extended to tool effects. A discarded ensemble draft or a barged-in turn simply **drops the recorded intents** — nothing was written, so there is nothing to undo.

Tool execution shares the turn's `context.Context`, so barge-in cancellation tears down an in-flight (read-only) tool call for free.

## Considered options

- **Saga / compensating `Undoer`** (write instantly, reverse on discard) — rejected as the primary mechanism. It gives atomicity but **not isolation**: a speculative sibling can dirty-read an uncommitted write and absorb its *influence* into spoken text before the write is undone — and undo cannot retract influence. It also cannot cover irreversible external effects, and a failed compensation leaves a half-state. "The best undo is not doing it yet." Held **in reserve** for the single future case it uniquely fits: an *irreversible external* (MCP Server) effect whose result is *load-bearing mid-generation* — must run during the turn, cannot be deferred or faked. No such tool exists in v1.0.
- **Open a DB transaction per draft across the whole generation** — rejected; long-running transactions across multi-second LLM latency hold locks and exhaust connections. Recording intent and flushing in a short commit-time transaction achieves the same isolation without the open-tx cost.

**Why this works:** a write's result is almost never load-bearing for the spoken text (`remember_knowledge` returns "saved", which the LLM does not need to keep talking), while reads — which the LLM *does* need mid-generation — are read-only and safe to speculate. Deferring writes to commit therefore costs nothing the spoken turn depends on, and yields isolation + atomicity for free with zero per-tool compensation code. The lost capability is intra-turn read-your-own-writes, which is rare and can be revisited.
