# Volatile Hot Context tail (cache-stable prompts) and the GM directive track

Two coupled decisions about how per-turn content reaches an Agent's LLM request.

## 1. The volatile Hot Context tail

The Replier's request layout changes from "everything in the system prompt" to a
stable-prefix / volatile-tail split:

- **Stable system prompt** (fixed at session start): Persona → speaker-roster
  section → audio-markup instruction.
- **Recent Transcript**: the append-only bounded history, unchanged.
- **Volatile tail**: ONE trailing system-role message carrying this turn's
  KG-facts block (#126), recalled-memory block (ADR-0011/0042), the Cross-talk
  instruction (ADR-0025, React path only), and the GM directive (below) — in
  that order, the directive last (strongest recency). All slots empty ⇒ no tail
  message; the request is byte-identical to the plain system+history prompt.

**Why.** The deployment default LLM is `openai/gpt-oss-120b` on Groq (ADR-0036
#424 amendment). Groq's prompt caching is automatic and prefix-based: it
matches request tokens from the start and stops at the first difference; cached
tokens are half-priced, faster to first token, and don't count against rate
limits. The pre-0059 slot order (Persona → facts → memory → roster → markup →
history) put the per-turn facts/memory right after the Persona, so every turn
forked the prefix a few hundred tokens in and the roster, markup, and the
ENTIRE conversation history missed the cache — the worst possible layout for a
voice product whose two SaaS axes are TTFT and per-turn cost. With the volatile
content trailing the history, everything up to the previous turn is a stable
prefix again.

**Provider safety.** A trailing system message is legal on every wired adapter:
the OpenAI-compatible ones (Groq, Gemini) keep it positionally last; the
anthropic adapter folds every system message into the top-level system field —
semantically the pre-0059 placement, and Anthropic-side caching is explicit
(`cache_control`, not wired) so nothing regresses there.

**Persona-fidelity trade-off.** Facts/memory now arrive after the history
rather than inside the Persona's system prompt. Accepted: the blocks keep their
labeled headers, the batch/streaming/Draft/React paths all carry the same tail,
and the layout is A/B-testable with the voicecassette rig (a prompt change
re-records cassettes per ADR-0021). Revisit only if a live run shows persona
drift attributable to the move.

**History discipline.** The tail is per-request and never committed: history
keeps holding exactly the user/assistant turns (ADR-0012), so yesterday's facts
can neither bloat the context nor poison the next turn's prefix.

## 2. The GM directive track (/direct)

`/say` puppets an NPC — the GM's words, verbatim, LLM bypassed (ADR-0024).
What was missing is *steering*: a quiet stage note ("Bart lies about the key")
that tilts ONE Agent's own generation for a few turns without the table ever
hearing it. Regie statt Puppenspiel.

- **Surface**: flat GM-only `/direct as:<npc> [note] [turns]` beside /say
  (ADR-0010). Omitting `note` clears; `turns` (1–25) bounds how many committed
  Agent turns the note rides, omitted = sticky until cleared/replaced/session
  end. Replies are always ephemeral. The Butler is not a target (the GM's own
  assistant needs no secret notes) — the same voiced-Characters-only chokepoint
  the mute set uses.
- **State**: volatile and session-local on the session Manager (the #211 mute
  precedent): no DB column, no transcript projection, no bus event — the
  Replier PULLS the directive per turn through a new `agent.DirectiveRecaller`
  seam (`Directive(ctx, agentID, consume)`) the Manager satisfies structurally,
  exactly like `MuteView`. Nothing observable leaks to players: no relay/SSE
  frame exists to leak.
- **Prompt placement**: the LAST block of the volatile tail (decision 1), with
  a secrecy contract ("never quote, mention, or hint"). Putting it in the
  system prompt would both fork the cached prefix and outlive its welcome; the
  tail gives it maximum recency at zero prefix cost.
- **Turn accounting**: committed reply paths (the single-target batch/streaming
  turns) consume one budget unit per turn — the consult that spends the last
  unit still gets the text; the speculative Ensemble paths (Draft/React) peek
  without consuming, so a losing candidate never burns budget. Ensemble turns
  therefore see the directive but don't count it down (the live matcher is
  single-target anyway, ADR-0025 deferred).
- **Split mode**: relays through the existing `voice_session_controls` claim
  plane (#503) as kind `direct` (new `direct_turns` column, migration 00039);
  the hosting worker's ClaimLoop executes it against its local Manager.

**Considered options:**

- **Reorder inside the system prompt only** (stable blocks first, volatile
  last, still one message) — rejected: the history follows the system message,
  so per-turn facts anywhere inside it still invalidate the whole conversation
  cache.
- **Directive via an edited Persona** — rejected: persists past the scene,
  visible in the web editor, no turn bound, and a Persona save mid-session
  doesn't reach a running roster.
- **Directive as a bus event consumed by a pipeline-side store** — rejected:
  needs reconnect reseeding and an event a relay could one day leak; the
  Manager-owned pull seam has neither problem.
- **Turn countdown decremented on TurnEnded events** — rejected: TurnEnded
  carries no AgentID; correlating TurnID→Agent adds a tracking map for no
  user-visible gain over consume-on-committed-consult.
