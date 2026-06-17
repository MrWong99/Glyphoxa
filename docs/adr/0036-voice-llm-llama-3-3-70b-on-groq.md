# Voice-loop deployment LLM: Llama 3.3 70B on Groq, Gemini Flash-Lite as fallback

The live NPC's default LLM moves off `gemini-2.5-flash` (Google) and onto **Llama 3.3 70B on Groq** — a non-reasoning model served on a low-TTFT inference endpoint. This is the decisive lever for the speech-end→first-audio SLO (≤1.2 s p50, latency.md), replacing the thinking-cap mitigation of ADR-0035 with a model that has no reasoning tail to cap. Gemini 2.5 Flash-Lite stays configured as the automatic fallback.

## What this decides

- **The deployment LLM provider becomes Groq; the model becomes `llama-3.3-70b-versatile`.** Non-reasoning, 128K context, full tool/function calling, good German. This is the `providers.llm` default the seed records (`internal/wirenpc/agentspec.go` `llmProvider`/`llmModel`) and what `buildConversation` wires.
- **No new wire protocol.** Groq exposes an OpenAI-compatible `/chat/completions` endpoint, exactly the shape the existing gemini adapter already targets (see `pkg/voice/llm/gemini/gemini.go`: it deliberately drives Gemini's *OpenAI-compat* surface, not the native one). The switch is a **base-URL + key + model swap** over the same streaming/tool-call machinery — Groq base URL `https://api.groq.com/openai/v1`, key from the keyring entry `service=glyphoxa key=groq` (env `GROQ_API_KEY`), the `dice` tool schema re-tested on the new provider.
- **`reasoning_effort` / `thinking_budget` are dropped from the default path.** Those knobs (ADR-0035) exist to bound *dynamic thinking wall-time* on a thinking model. Llama 3.3 70B does not think, so there is nothing to cap: the new default sends neither field. The knobs remain on the gemini adapter for any Gemini-routed traffic (the fallback) but are off the hot path.
- **Gemini 2.5 Flash-Lite is the documented fallback, not 2.5 Flash.** If Groq is unreachable or rate-limited, route to `gemini-2.5-flash-lite` (thinking disabled by default, ~0.26–0.42 s TTFT, 1M context) — *not* `gemini-2.5-flash`, whose dynamic thinking is the very tail ADR-0035 was fighting. Pin the stable `gemini-2.5-flash-lite` id (the preview alias retires mid-2026).

## Why

The Sprint-2 latency work (latency.md) named the LLM turn as the dominant, most-variable stage (0.8–6 s+) and `gemini-2.5-flash`'s uncapped dynamic thinking as the primary cause of the "manchmal sehr spät" tail (H1). ADR-0035 capped that tail with `reasoning_effort: "low"` and the live A/B confirmed the mechanism (reasoning-bait p95 9282→5549 ms) — but it also documented the honest cost: a ~1 s *floor* added to already-trivial turns, because `reasoning_effort` behaves as a thinking target, not only a ceiling. Capping a thinking model is a mitigation; removing thinking is the fix.

A June-2026 latency research review of the 2026 fast-model landscape ranked the options for exactly our constraints (sub-2 s first audio, tool calling, ≥100K context, German). Its headline: **the biggest latency lever is the inference provider, not the model.** Llama 3.3 70B on Groq's LPU measures ~0.27 s LLM TTFT in a live voice pipeline (vs ~0.9–5 s for capped/uncapped Gemini Flash on the same boundary in our A/B), is non-reasoning (no tail to cap), supports tool calling + JSON mode, has a 128K window, and handles German well. Practitioner stacks report ~1.0–1.1 s *total* voice-turn latency with this LLM. That collapses the LLM's contribution from seconds to a few hundred milliseconds and is what brings the SLO into reach without the trivial-turn floor ADR-0035 paid.

Keeping the OpenAI-compat adapter is the reason this is cheap: the gemini package was written against the compat endpoint precisely so the tool-result-by-id seam (ADR-0028) maps 1:1, and that contract is provider-neutral. Groq is one more compat backend behind the same `llm.Provider`.

## Considered options

- **Stay on Gemini 2.5 Flash with the ADR-0035 thinking cap** — rejected as the default: it is a mitigation that trades a ~1 s floor for a tighter tail and still leaves the LLM as the dominant stage. Superseded here; the cap stays available for the Gemini fallback.
- **Gemini 2.5 Flash-Lite as the primary** — the lowest-effort migration (a model-id swap in the same provider, thinking off by default) and a genuine fix for the tail. Chosen as the **fallback** rather than primary because Groq's LPU gives a materially lower and more *consistent* TTFT, and the research ranks the provider change above the same-ecosystem model change. Flash-Lite is the safety net precisely because it is the near-zero-code path.
- **Cerebras (Llama 3.3 70B, ~200–300 ms TTFT, official LiveKit plugin)** — a strong alternative, higher throughput on the tail. Held as the documented escape hatch if Groq shows throughput tails under load: same model, same OpenAI-compat swap.
- **Mistral Small 3.2 for German polish** — best German naturalness among fast models, low repetition (good for long roleplay), ~0.64 s TTFT. Deferred to an A/B follow-up: if Llama's German feels stiff on real NPC prompts, route German-locale traffic to Mistral and keep Groq/Llama for English. Not the default because raw TTFT wins for the SLO and Llama 3.3 70B's German is rated solid.
- **gpt-oss-120b on Groq/Cerebras** — extremely fast but a reasoning model with documented German weaknesses; English-first. Not pursued.

## Caveats (re-test before launch)

- **EU region.** Groq's Helsinki endpoint is Enterprise-gated; a standard self-serve key may route to the US, adding ~80–120 ms transatlantic RTT for an Aachen-based deployment. Still well under the SLO, but measure the live tier from the deployment's egress before assuming the research's ~0.27 s TTFT.
- **Tool calling varies by provider** even for the same open model — re-run the `dice`-tool corpus (voicebench) against Groq before trusting the function-call path.
- **German has no clean 2026 roleplay leaderboard** — validate Llama 3.3 70B's German on the actual NPC scripts; the Mistral A/B above is the lever if it disappoints.
- **Model churn** — Groq deprecated Kimi K2 and Llama 4 Maverick in early 2026; re-check the live model list before pinning.

## Relationship to other ADRs

- **Supersedes ADR-0035** for the deployment default: the thinking-cap default (`reasoning_effort: "low"`) is moot on a non-reasoning model. ADR-0035 stays as the record of the Gemini A/B and keeps the `WithReasoningEffort`/`WithThinkingBudget` knobs alive for the Gemini fallback.
- **Extends ADR-0004 (BYOK provider matrix).** That ADR fixes the *tenant-supplied* 2-providers-per-component set (LLM: Anthropic + Ollama). The deployment's own voice NPC has always run a provider outside that BYOK set (Gemini, now Groq); this ADR records the deployment default, it does not change the BYOK matrix. Adding Groq to the self-serve BYOK provider list is a separate, additive decision.
- **No change to ADR-0021** (cassette determinism) or **ADR-0028** (tool seam): unit tests stay keyless via cassettes, and the OpenAI-compat tool-result-by-id mapping is unchanged across compat providers.
