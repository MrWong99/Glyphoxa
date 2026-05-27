# Ensemble turns: speculative lead + cross-talk reaction

When one utterance addresses two or more Agents (ADR-0024 returns a set), the turn-taking layer runs an **Ensemble Turn** rather than N independent turns. The goal is human-like cross-talk while each NPC stays an independent Agent — its own Persona, LLM config, Hot Context, and Voice. There is no director LLM and no ADR-0022 dialogue render on the hot path.

Mechanic:

1. **Speculative fan-out** — all addressed Agents generate in parallel.
2. **Lead** — the first Agent to finish its full response *text* takes the floor and streams TTS. Text completes well before its audio finishes playing, so winning on text costs little wall-clock latency. A side effect: the Agent with the shorter response tends to lead.
3. **Cross-talk reaction** — the Lead's complete text is fed to the fastest remaining addressed Agent as **Cross-talk**; that Agent discards its speculative draft and regenerates a **Reaction** (typically a short affirmation or a longer disagreement). The Reaction generates and pre-renders TTS during the Lead's audio playback.
4. **Queued follow-up** — when the Lead's turn ends, the Reaction plays immediately after, with near-zero gap.

**Bounds (v1.0): lead + one reaction, no rebuttals.** If 3+ Agents are addressed, only the fastest remaining Agent reacts; the rest stay silent that turn. A Reaction never re-triggers the Lead (depth capped at 1). Breadth and depth can grow later behind the same mechanic.

**Reactor may decline.** The Reaction prompt permits an explicit "stay silent" output; a silent Reaction commits nothing, consistent with ADR-0012's zero-delivered rule. This avoids hollow "yeah, what he said" filler.

Each spoken turn (Lead and Reaction) is a normal per-Agent utterance under ADR-0012 — per-sentence deliver-then-commit, independently barge-able. Discarded speculative drafts are never committed.

**Terminology.** "Barge-in" stays reserved for a *human* interrupting a speaking Agent (Q13.5). The Lead's text fed to other Agents is **Cross-talk**, not barge-in — the receiving Agent has not spoken yet, so nothing is interrupted. Whether a human barge-in cancels a queued Reaction is settled in Q13.5.

**Why not the ElevenLabs dialogue API.** `SynthesizeDialogue` exists but ADR-0022 scopes it to off-hot-path batch renders (recap, cutscenes) that are not transcript-committed, and it needs the whole multi-speaker script up front from a single LLM. Live Ensemble Turns must commit to Transcripts (ADR-0012), preserve per-Agent reasoning, and stay barge-able — so they use the normal streaming `Synthesize` path per Agent, not the batch dialogue render.

**Why a speculative race over a deterministic lead.** A cheaper design picks the Lead deterministically (e.g. first-named) and generates only two responses. The speculative race costs one extra discarded generation but lets the more eager / more decisive Agent emerge organically, which is the point of the human feel. Multi-address is occasional, so the extra cost is acceptable.
