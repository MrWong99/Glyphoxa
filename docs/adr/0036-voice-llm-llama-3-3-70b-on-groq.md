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

## Amendment (2026-07-13, #424 — default model → `openai/gpt-oss-120b`)

The deployment default LLM moves from `llama-3.3-70b-versatile` to **`openai/gpt-oss-120b` on Groq** (`groq.DefaultModel`). This **reverses the "gpt-oss-120b … Not pursued" line** in *Considered options* above: that pre-launch rejection cited "documented German weaknesses; English-first", but a live A/B (campaign *The Prancing Pony*, 2026-07-13 night) contradicted it on the actual NPC prompts.

Live findings vs `llama-3.3-70b-versatile`:

- **Tool calling:** clean native calls throughout (dice single/multi + sum, recap ×3, remember with correct scope handling) — zero pseudo-XML text leaks. llama-3.3 produced the #398/#410 pseudo-call flake class the same evening.
- **German:** in-character and natural, including a roleplayed scope rejection — no sign of the weakness the original ADR feared.
- **Latency:** first_audio ~1.9–3.5 s vs ~1.4–2.0 s on llama-3.3 — noticeably higher but accepted as within budget.
- **Cost:** `$0.15 in / $0.60 out` per MTok — *cheaper* than llama-3.3 (`$0.59 / $0.79`), consistent with ADR-0046's conservative-default posture. Price entry added to `internal/spend/prices.go` (#426).
- **Residual:** one live case of a tool call emitted despite `tool_choice=none` during a forced-regen chain → Groq 400 → turn abandoned; tracked separately as #427.

Scope: only the **default for fresh setups** changes (the seed `internal/wirenpc/agentspec.go` `llmModel` and the adapter fallback). Existing campaigns keep their configured `provider_config` model — the `llama-3.3-70b-versatile` price entry is retained for them, and no migration rewrites existing rows. Everything else in this ADR (Groq provider, OpenAI-compat wire, Gemini Flash-Lite fallback, no `reasoning_effort` on the default path) stands unchanged. ADR-0021 is still unaffected: cassette prompt hashes key on the request model (`""` on the default path), so the constant change alters no committed hash.

Reasoning-knob posture: unlike the prior llama default, `openai/gpt-oss-120b` **is** reasoning-capable, so the "Supersedes ADR-0035 — `reasoning_effort` is moot on a non-reasoning model" rationale above no longer strictly holds. The posture is nonetheless **unchanged for now**: the default path still sends no `reasoning_effort`, and any reasoning tokens gpt-oss emits share the same `DefaultMaxTokens` (1024) budget as the spoken reply. The #424 live run produced complete replies within that cap (recap ×3, multi-tool turns), so this is accepted as-is — revisit (send an explicit low `reasoning_effort`, or widen the cap) only if a live run shows reasoning tokens starving the reply.

## Amendment (2026-11-22, #702 — the deployment may choose its default LLM; Claude Haiku is the recommended choice where naturalness matters)

The default voice LLM stops being a compile-time constant. Two envs, read once
at boot, name what a Tenant with **no LLM Provider Config of its own** runs:

- `GLYPHOXA_LLM_DEFAULT_PROVIDER` — `groq`, `anthropic` or `gemini`;
- `GLYPHOXA_LLM_DEFAULT_MODEL` — the model asked of it.

**The code default stays Groq `openai/gpt-oss-120b`.** Both envs unset behaves
exactly as before this amendment. **A deployment may choose its default; Claude
Haiku is the recommended choice where naturalness matters more than
~0.4–0.6 s** of first-audio latency.

### What was measured (2026-11-22)

**Production voice probe**, the same 14 German clips each time, server-side
speech-end → first-audio, median:

| build / LLM | median |
|---|---|
| v0.17.0 — batch STT, `eleven_v3`, `openai/gpt-oss-120b` | 1.86 s |
| v0.18.0-rc.1 — `openai/gpt-oss-120b` | 0.90 s |
| v0.18.0-rc.1 — `claude-haiku-5-5` (adaptive thinking, effort low) | 1.50 s |
| v0.18.0-rc.1 — `claude-haiku-4-5` | 1.31 s |

**Blind German naturalness panel** over four `llmbench` passes: three lenses,
408 judgements per candidate, scored 1–5. The judges were Claude models, blind
to which system produced a reply — which keeps them from favouring a name, not
from sharing a house style with two of the candidates; read the Haiku lead with
that in mind.

| candidate | score |
|---|---|
| `claude-haiku-4-5` | 4.13 |
| `claude-haiku-5-5`, adaptive thinking, effort low | 3.66 |
| `claude-haiku-5-5`, thinking off | 3.61 |
| `openai/gpt-oss-120b` (Groq) | 3.13 |
| `qwen/qwen3.8-27b` (Groq) | 2.01 |

**Tool-turn reliability** (`pkg/voice/llmbench`, results in
`pkg/voice/llmbench/results/2026-11-22.md`): both Haiku models 94–100 %;
`openai/gpt-oss-120b` 75–94 % — it skips lookups and invents the facts it
should have fetched.

**Cost per 100 bench turns:** `openai/gpt-oss-120b` $0.047, `claude-haiku-5-5`
$0.076, `claude-haiku-4-5` $0.59. The Haiku 4.5 figure is uncached: the bench
prompt is below that model's 4,096-token prompt-cache minimum, so it pays full
input price every turn; a production prompt long enough to cache would cost
less, and that was not measured.

### What this decides

- **The default is deployment configuration, not a constant.** gpt-oss-120b is
  the fastest by 0.4–0.6 s and the cheapest; the Haiku models sound more
  natural in German and call tools more reliably. Which of those a table needs
  is the operator's call, so the operator gets the switch. The measurements do
  not justify moving every self-host to a different vendor and a second API
  key unasked — hence no change to the code default.
- **One resolution, shared by everything.** `llmbuild.DeploymentDefault.Select`
  turns "the Tenant's newest `llm` row, else nothing" into a provider **and a
  model, neither ever empty**. The Voice Session, the Highlight classifier,
  recap, assist, the planning chat, the model catalog, the Configuration read
  and the LLM health probe all call it. There is no second place where "no row"
  is read as Groq — an empty provider id is an error in `llmbuild.New`, not
  Groq — so the adapter built, the key env spent, the span label and the price
  row cannot disagree.
- **One model default per config.** A row that names no model runs the same
  model everywhere: the deployment model when the row is on the default
  provider, else that provider's default — `openai/gpt-oss-120b` on Groq,
  **`claude-haiku-5-5` on Anthropic**. The Anthropic adapter's own fallback is
  its flagship (`claude-opus-4-8`, $5 / $25); no engine falls through to it.
  Recap, assist and the planning chat used to, while voice ran Haiku — one
  config, two models, the off-session one ~50x the price the screen showed.
- **Tenant rows still win.** The deployment default applies only when no row
  exists. A row on another provider never receives a model id belonging to a
  different API: an NPC's model comes only from a row of the provider whose
  adapter serves the session.
- **Agent-bound LLM rows are retired as a source.** `agents.llm_provider_config_id`
  was only ever written by the demo seed, always pointing at the Tenant's one
  LLM row. With two slots it would pin a seeded campaign's adapter to the row
  the Tenant switched *away* from, while the screen named the other. The seed
  no longer binds, migration `00060` clears the existing bindings (a no-op for
  behaviour: the bound row was the Tenant row), and the Tenant's row is the one
  source the session, the off-session engines and `effective_llm` all read. The
  column and the "bound config wins" code path remain for a future per-Agent
  choice; whoever adds a writer for it must extend `effective_llm` with it.
- **Fail fast.** An unknown provider, or a named provider whose platform key
  env is empty, stops the boot naming the variable. Unset needs no key: a
  BYOK-only deployment still boots without any platform LLM key. The Helm chart
  fails the render on the same two mistakes.
- **The entitlement gate is provider-agnostic.** The platform-key gate
  (ADR-0054 seam (a), ADR-0055) refuses an env-fallback resolution, whichever
  provider's env key it would have spent. A BYOK-plan Tenant with no saved key
  is refused on an Anthropic default exactly as on Groq.
- **Nothing offered unasked is unpriced.** A model without a price row is
  metered at one flat fallback ($5 / $15 per MTok): dozens of times the real
  rate of a small model, but *below* the real rate of an Opus-class one
  (`claude-opus-4-8` output is $25). Every provider default and every fallback
  catalog entry has a row and a test that says so; `claude-opus-4-8` and
  `gemini-2.5-flash` gained theirs here (the Gemini figure is recalled, not
  re-retrieved — verify it before making Gemini a default). A deployment
  default set to an unpriced id boots with a WARN.
- **The platform key runs only priced models.** The flat fallback over-meters
  a cheap unpriced model (a trial allowance gone in a handful of turns) and
  under-meters a dear one (the platform pays past the allowance). So a keyless
  LLM row — a Tenant riding the Platform Key — may only store a model the price
  table has a row for; `SaveProviderConfig` refuses any other id, and
  `ListModels` on the platform key lists none. The empty model (the default) is
  always allowed. A Tenant's own key keeps free-text entry and the full
  catalog: its spend is its own bill.

### Consequences

- The seed still records a Tenant row `groq` / `openai/gpt-oss-120b`
  (`internal/wirenpc` `llmProvider`/`llmModel`), and a row wins: the seeded
  tenant runs Groq until it chooses otherwise in Configuration. It no longer
  binds that row to the seeded NPC.
- A self-host on env keys (keyless rows) that wants a model the price table
  does not know either saves its key in Configuration (its own key: free text)
  or names the model in `GLYPHOXA_LLM_DEFAULT_MODEL` (the operator's explicit
  choice; boots with a WARN).
- A key saved into the LLM slot not in use becomes the newest row and so the
  LLM in effect; the screen says so next to the key field. There is no
  separate "active" marker, and no action that returns a Tenant with a row to
  "follow the platform default" — it can switch to the default provider's slot,
  which pins that provider. An explicit marker is a follow-up.
- "Gemini 2.5 Flash-Lite as the automatic fallback" above was never built and
  is not built here; `gemini` is selectable as a default like the other two.
- Whether the Haiku naturalness lead survives judges from another model family
  is open, as is Haiku 4.5's cached cost on production-length prompts.

## Amendment (2026-11-22 — the planning chat has its own model default and thinking policy)

ADR-0062 (amendment of the same date) makes the Planning chat model a Tenant
choice. What that changes here:

- **"One model default per config" still holds** for `llm` rows and for a chat
  that follows voice. An explicit chat choice that names no model has a default
  of its own (`llmbuild.DefaultChatModel`: `claude-sonnet-5-5` on Anthropic,
  the voice default on Groq), and the Configuration screen names it.
- **The thinking policy is per surface.** Voice keeps `DefaultThinking`
  (adaptive at effort `low` on Claude Haiku 5.5, nothing else sent, requests
  byte-identical to before). The chat is built with `ChatThinking`: adaptive at
  effort `medium` on every model family that accepts both fields.
- **Price rows added**, platform.claude.com/docs/en/about-claude/pricing,
  retrieved 2026-11-22: `claude-sonnet-5-5` $2 / $10, `claude-opus-5-5`
  $4 / $20, `claude-sonnet-5` $2 / $10, `claude-opus-5` $5 / $25 per MTok.
  Estimates, as every row.
- **A forced `tool_choice` is sent as `auto` to models that reject it.**
  Claude Sonnet 5.5, Opus 5.5, Fable 5.1/5.2 and Mythos 5.1/5.2 answer 400 to
  `any` and `tool`; the adapter downgrades and warns once per model.
- **Consequence for voice (#712 item 7, now wider):** pricing Sonnet 5.5 and
  Opus 5.5 also makes them selectable as the *voice* model on the Platform
  Key, since the voice catalog uses the same priced-only rule. They run there
  at the API's own default effort with no voice tuning, and the forced dice
  re-roll becomes a likely tool call instead of a guaranteed one. Whether the
  voice catalog on the Platform Key needs a suitability filter is decided in
  #712.
