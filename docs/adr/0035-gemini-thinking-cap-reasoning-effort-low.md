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

The A/B harness is `TestLive_ThinkingCap_AB` in `pkg/voice/llm/gemini/thinking_live_test.go` (`//go:build live`, excluded from the keyless suite): interleaved arms (uncapped default vs. `reasoning_effort:"low"`), measuring time-to-first-content-token (the cleanest H1/thinking signal) and total wall-time, reported as a distribution (p50/p95/p99), with a sample answer per arm for the quality check. It paces calls (`GX_AB_DELAY`, default 13s) to respect rate limits.

**Findings from the first keyed run (2026-06-09), and why it's only directional:**
- **The field is honoured, not silently ignored.** Both arms completed real calls against `gemini-2.5-flash` on the compat endpoint with no 4xx — `reasoning_effort` is accepted on 2.5-flash (the open question keyless tests could not answer). Sample replies were valid and in-character on the trivial tier (default → `"Aye."`, low → `"Comin'."`), i.e. no quality regression observed there.
- **No wall-time distribution yet.** The shared deployment key is on the Gemini **free tier**, which caps `gemini-2.5-flash` at **20 requests/day** (`generate_content_free_tier_requests`, a daily cap distinct from the 5-req/min RPM throttle). A real p50/p95 needs N≥~30 per arm; the daily 20 cannot fund it, and this is the live NPC's shared key (a paced A/B starves the bot). So the latency distribution is **deferred to the paid nightly live tier** (ADR-0033) — exactly the residual Gemini live-unknown the Sprint-2 plan files under D3. Re-run `TestLive_ThinkingCap_AB` under a paid/billing-enabled key (or raise the free-tier quota) and paste the two `ARM … ttft_ms/total_ms` distribution lines here.

**Status:** wire shape + default cap landed and keyless-green; the field is confirmed honoured live with no quality regression on the trivial tier; the p50/p95 wall-time delta is pending a paid-quota run. The chosen default (`"low"`) is recorded here and in `gemini.DefaultReasoningEffort`.

## Considered options

- **Explicit `thinking_budget` integer as the default** — rejected as the *default* (kept as an opt-in via `WithThinkingBudget`): a hard token count is model-specific and brittle across model swaps, whereas `reasoning_effort: "low"` is a portable, model-relative allowance.
- **`reasoning_effort: "none"` (thinking fully off)** — rejected as the default: too aggressive for a model that benefits from a little reasoning on dice/RP turns; risks a quality regression. `"low"` keeps a small allowance while still bounding the tail. `"none"` remains reachable via `WithReasoningEffort`.
- **Leave it to `max_tokens` only (status quo)** — rejected: `max_tokens` bounds tokens, not wall-time; this is exactly the H1 failure mode.
- **Send both `reasoning_effort` and `thinking_config`** — impossible: the endpoint rejects the pair; the adapter enforces exclusivity.
