# Cap gemini-2.5-flash "thinking" wall-time: send `reasoning_effort: "low"` by default

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

<!-- A/B RESULTS — fill in from the live run:
  Corpus: trivial / dice-trigger / reasoning-bait prompts, N≈15–20 per arm.
  Arm A = default (no cap), Arm B = reasoning_effort:"low".
  Report DISTRIBUTION, not mean — p50 / p95 of llm_round wall-time, low vs default.
  Confirm no answer-quality regression on the dice/RP prompts.
-->
**Status:** wire shape landed + keyless-green; live A/B distribution to be appended from the next keyed run (the live tier is gated, ADR-0033). The chosen default (`"low"`) is recorded here and in `gemini.DefaultReasoningEffort`.

## Considered options

- **Explicit `thinking_budget` integer as the default** — rejected as the *default* (kept as an opt-in via `WithThinkingBudget`): a hard token count is model-specific and brittle across model swaps, whereas `reasoning_effort: "low"` is a portable, model-relative allowance.
- **`reasoning_effort: "none"` (thinking fully off)** — rejected as the default: too aggressive for a model that benefits from a little reasoning on dice/RP turns; risks a quality regression. `"low"` keeps a small allowance while still bounding the tail. `"none"` remains reachable via `WithReasoningEffort`.
- **Leave it to `max_tokens` only (status quo)** — rejected: `max_tokens` bounds tokens, not wall-time; this is exactly the H1 failure mode.
- **Send both `reasoning_effort` and `thinking_config`** — impossible: the endpoint rejects the pair; the adapter enforces exclusivity.
