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

## Amendment: `remember_knowledge` is proposal-mediated, turn-commit narrowed (2026-07-07, #298)

**ADR-0052** routes Agent KG writes through a GM-reviewed proposal queue: the Tool's only effect is inserting a `pending` proposal row at execution time, so it needs neither intent recording nor commit-time flushing (a barged reply still yields its proposal — the Agent heard the fact; the GM review is the safety net). Turn-commit remains the specified mechanism for future Tools whose effects must be atomic with the spoken utterance, and the `pkg/tool/loop.go` hard-refusal stays for any side-effecting Tool that is not proposal-mediated.

## Amendment: tool rounds are no longer paced by delivery (2026-11-20)

Until now the streaming Agent turn consumed the provider stream in lockstep with playback, which gated tool use on the room by accident: a tool round that followed spoken prose ("Let me check.") only started once that prose had been delivered, so a barge during it cancelled the turn before the round ran. Generation is now drained independently of playback (ADR-0012 amendment of the same date), so **every LLM round and every tool round of a turn runs to completion as fast as the provider allows, even if the players cut the reply after its first sentence** — unless the barge lands while generation is still in flight, which cancels it as before.

For the Tools that exist this changes nothing that matters. Read-only Tools were always allowed to run speculatively. `remember_knowledge` is proposal-mediated (the amendment above, ADR-0052): its only effect is a `pending` proposal a GM reviews, and that amendment already accepts a barged reply yielding its proposal — it now also yields it when the reply was cut before the sentence that preceded the call was heard.

It does harden one rule. Delivery no longer stands between the model and a Tool's execution at all, so **any future side-effecting Tool that is not proposal-mediated MUST go through turn-commit** (record the intent during generation, execute only once the turn's delivered text commits). Inline execution of such a Tool is not merely speculative now; it is unconditional. The hard refusal in `pkg/tool/loop.go` for a side-effecting, non-proposal-mediated Tool stays the enforcement.
