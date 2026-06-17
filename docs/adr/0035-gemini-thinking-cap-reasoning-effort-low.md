# Cap gemini-2.5-flash "thinking" wall-time: send `reasoning_effort: "low"` by default

> **Superseded as the deployment default by ADR-0036.** The voice loop's default LLM moves to Llama 3.3 70B on Groq — a *non-reasoning* model with no thinking tail to cap, which removes both the H1 tail and the ~1 s trivial-turn floor this cap traded for. This ADR stays as the record of the Gemini thinking A/B, and the `WithReasoningEffort`/`WithThinkingBudget` knobs it documents remain live for any Gemini-routed traffic (the ADR-0036 fallback is `gemini-2.5-flash-lite`, which ships with thinking off by default).

The Gemini adapter bounds gemini-2.5-flash's dynamic reasoning by sending a thinking cap on every completion — `reasoning_effort: "low"` by default — rather than only `max_tokens`. This is the primary fix for the Sprint-1 "manchmal sehr spät" latency complaint (latency.md H1 / Sprint 2 B2).

## What this decides

- **The adapter always sends a thinking cap by default.** `New` initialises `reasoningEffort` to `DefaultReasoningEffort = "low"`, so prod gets the cap with no opt-in. On the OpenAI-compat endpoint `reasoning_effort` is a top-level field (`"none"`/`"minimal"`/`"low"`/`"medium"`/`"high"`); for gemini-2.5 it maps to an internal `thinking_budget`. Empirically confirmed to move the latency distribution — see the A/B below.
- **Two configurable knobs, mutually exclusive on the wire.** `WithReasoningEffort(effort)` overrides the coarse bucket (`""` disables the cap → the old time-unbounded default). `WithThinkingBudget(n)` is the precise escape hatch, sent as `extra_body.google.thinking_config.thinking_budget` (`0` = thinking off, `-1` = dynamic/unbounded, `N` = at most N reasoning tokens). The endpoint rejects `reasoning_effort` and `thinking_config` together, so the adapter enforces exclusivity: a set budget wins and suppresses `reasoning_effort`.
- **`max_tokens` is unchanged and orthogonal.** It caps total completion tokens (reasoning + output); it does **not** bound wall-time, which is the whole point of H1. The thinking cap bounds *how long the model reasons*; `max_tokens` bounds *how much it can emit*.

## Why

`gemini-2.5-flash` is a thinking model whose reasoning is **dynamic by default**: the model decides how many reasoning tokens to spend per input, charged against `max_tokens` but **unbounded in wall-time**. A trivial "Bart, noch ein Bier?" thinks little; a reasoning-bait question can spend several seconds *before the first content token streams*. That input-dependence is the best match for the intermittent "sometimes very late" tail — and it is directly testable by pinning thinking low vs. default and comparing the *distribution* (not the mean).

`reasoning_effort` is chosen as the default knob over an explicit integer budget because (a) it is the documented, stable compat field; (b) `"low"` is a model-relative allowance that survives model swaps, where a hard token count is brittle; (c) a short spoken NPC turn rarely needs deep reasoning, so `"low"` trims the tail without a quality regression (confirmed on the dice/RP corpus below). The `thinking_budget` escape hatch stays available for when `"low"` proves too coarse.

## Live A/B (verification — the part keyless tests cannot close)

Keyless `httptest` tests pin the *wire shape* (default sends `reasoning_effort: "low"`, override/budget paths, mutual exclusivity) but cannot prove the endpoint *honours* the field or that wall-time actually tightens — a silently-ignored field would pass every keyless test. So the cap was verified with a small, key-blind live A/B against the real endpoint (key from the keyring via env, never printed).

The A/B harness is `TestLive_ThinkingCap_AB` in `pkg/voice/llm/gemini/thinking_live_test.go` (`//go:build live`, excluded from the keyless suite): three interleaved arms (uncapped default / `reasoning_effort:"low"` / `"medium"`), per-(arm,prompt) buckets, measuring time-to-first-content-token (the cleanest H1/thinking signal) and total wall-time as a distribution, with every answer logged (`GX_AB_LOG_ALL`) for the quality read. Paced (`GX_AB_DELAY`) to respect rate limits.

**These are isolated single-call numbers, not the SLO.** Each measurement is one raw Gemini `chat/completions` call (one-line system prompt, no history, no tools, no orchestrator), so they prove the **H1 mechanism** — does capping thinking move the wall-time distribution — and nothing more. They are NOT the in-pipeline `glyphoxa_voice_llm_round_seconds` series and NOT the speech-end→first-audio SLO (≤1.2 s p50): even trivial ttft here is ~1.9 s because it's a raw call on a different boundary, pre-B1. Don't read 4971 ms against the 1.2 s budget — the SLO is a live-tier pipeline measurement (the second live run + the C1 live tier).

**Measured A/B (2026-06-09, billing-enabled key, N=20/arm/prompt, ttft_ms):**

| prompt tier | arm | p50 | p95 | max |
|---|---|---|---|---|
| reasoning-bait | default (uncapped) | 5560 | 9282 | 11067 |
| reasoning-bait | **low** | **4971** | **5549** | **5585** |
| reasoning-bait | medium | 6668 | 11717 | 11835 |
| trivial | default (uncapped) | 1892 | 2849 | 2985 |
| trivial | low | 2933 | 4654 | 4745 |
| trivial | medium | 2860 | 6242 | 7864 |

**1. The win — the reasoning tail collapses (the actual complaint).** On reasoning-bait, `low` beats uncapped across the whole distribution: p95 **9282→5549 ms (−40%)**, max **11067→5585 ms (−50%)**, and even p50 5560→4971. This replicates the N=4 pilot (max 11378→5066), so two independent runs agree. The Sprint-1 complaint was *"manchmal sehr spät"* — an intermittent **tail**, input-dependent — and capping thinking is exactly what flattens it. This is H1 confirmed.

**2. The honest cost — a trivial-latency penalty.** On already-fast trivial turns `low` is ~1 s *slower* (p50 1892→2933, p95 2849→4654). `reasoning_effort` behaves as a thinking *floor/target*, not only a ceiling: on a prompt the model would barely think about it adds latency; on a prompt it would over-think (bait) it caps. So `low` **trades ~1 s on already-fast turns for a dramatically tighter reasoning tail.** Since the complaint was the tail — and B1 (sentence-streaming) attacks median latency on a separate axis — this trade is the right call for B2's charter, but it is a real trade, named here. If it bites in the live run, the lever is the explicit `WithThinkingBudget` knob (a numeric cap that bounds the tail without raising the floor as much).

**3. `medium` is not pursued.** It showed no improvement over `low` on either tier (and looked worse), but at N=20 a p95 is the 2nd-worst sample = one vendor-jittery call, so the medium numbers are not a robust finding — just "no reason to prefer it." Not investigated further.

**4. No quality regression.** All 60 reasoning-bait answers (20×3 arms) were reviewed: every arm answered with a coherent, in-character split that sums to 17 in one of two reasonable schemes (6.8/6.8/3.4 or the rounded 7/7/3 for the contradictory "split evenly but one drank half" premise). The capped arms are not muddier than uncapped (the single muddy sample in the N=4 pilot did not recur — it was noise; one `low` answer even *flags* the rounding remainder, which is awareness, not contradiction). The premise is contradictory and has no ground truth, so this is a self-consistency read, not a correctness one; a ground-truth reasoning prompt is a nightly-tier follow-up.

**Decision:** keep the shipped default `reasoning_effort:"low"`. It targets the actual Sprint-1 complaint (the tail), with a stated and acceptable trivial-latency trade. Recorded in `gemini.DefaultReasoningEffort`.

## Considered options

- **Explicit `thinking_budget` integer as the default** — rejected as the *default* (kept as an opt-in via `WithThinkingBudget`): a hard token count is model-specific and brittle across model swaps, whereas `reasoning_effort: "low"` is a portable, model-relative allowance.
- **`reasoning_effort: "none"` (thinking fully off)** — rejected as the default: too aggressive for a model that benefits from a little reasoning on dice/RP turns; risks a quality regression. `"low"` keeps a small allowance while still bounding the tail. `"none"` remains reachable via `WithReasoningEffort`.
- **Leave it to `max_tokens` only (status quo)** — rejected: `max_tokens` bounds tokens, not wall-time; this is exactly the H1 failure mode.
- **Send both `reasoning_effort` and `thinking_config`** — impossible: the endpoint rejects the pair; the adapter enforces exclusivity.
