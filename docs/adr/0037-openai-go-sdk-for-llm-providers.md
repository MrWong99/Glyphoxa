# LLM providers on the official OpenAI Go SDK behind a shared OpenAI-compat core

The OpenAI-compatible LLM adapters (Groq, Gemini) move off hand-rolled `net/http` + `bufio` SSE machinery and onto the official OpenAI Go SDK (`github.com/openai/openai-go/v3`), pointed at each provider's base URL via `option.WithBaseURL`. The two adapters — which were ~95% byte-identical — collapse into one shared core, `pkg/voice/llm/openaicompat`, that `groq` and `gemini` now wrap as thin presets. The Anthropic adapter (native Messages API, the cassette-recording reference) is unchanged.

## What this decides

- **One SDK-backed core, two thin presets.** `pkg/voice/llm/openaicompat` implements [`llm.Provider`](../../pkg/voice/llm/llm.go) on top of `client.Chat.Completions.NewStreaming`, owning the translation between the `llm` message vocabulary and the SDK's params/stream types. `groq` and `gemini` keep their packages, `ProviderID`s, defaults, and (for Gemini) the ADR-0035 thinking knobs, but delegate the wire to the core. The `llm.Provider` contract is unchanged, so the agent loop, `internal/wirenpc` wiring, `internal/observe` provider keys, and the seed (`internal/wirenpc/agentspec.go`) need no changes.
- **Gemini stays on the OpenAI-compat endpoint** (`generativelanguage.googleapis.com/v1beta/openai`), driven by the OpenAI SDK — **not** native `google.golang.org/genai`. This preserves the ADR-0036/ADR-0028 rationale (tool-result-by-id seam maps 1:1 onto `llm.ToolResult.CallID`; shared embeddings auth path) and keeps the dependency tree lean. The thinking cap rides as the SDK's typed `reasoning_effort` field; the explicit `thinking_budget` rides as `extra_body.google.thinking_config` via the SDK's JSON-set escape hatch (`option.WithJSONSet`).
- **Anthropic is out of scope.** It speaks the native Anthropic Messages API (not OpenAI-compatible), it is the `-tags=record` cassette-recording client (ADR-0021), and the official `anthropic-sdk-go` would drag in the AWS SDK v2 + Google auth stacks. It stays on its hand-rolled adapter.
- **SDK auto-retries are disabled** (`option.WithMaxRetries(0)`). On the voice hot path a retry-with-backoff could blow the LLM time-to-first-token budget (<400 ms target, 800 ms hard limit); transient-failure handling stays the job of the project's resilience layer and the request-context deadline.
- **Dependency footprint:** `openai-go/v3` plus `tidwall/{gjson,sjson,match,pretty}` — no gRPC, no cloud SDK. (`google.golang.org/protobuf` was already in the tree.)

## Why

The README advertised an LLM stack "via any-llm-go", but no LLM SDK was ever in `go.mod` — the `groq` and `gemini` adapters were hand-rolled OpenAI-compat clients with duplicated SSE framing, tool-call accumulation, and error handling. A June-2026 research review of Go LLM libraries for exactly our constraints (idiomatic, low-overhead streaming, modest deps) recommended **building thin per-provider adapters behind the existing `pkg/provider`/`llm.Provider` interface on the official `openai-go` SDK**, pointed at each provider's base URL — rather than wrapping a multi-provider abstraction (any-llm-go, goai) inside an abstraction we already own.

The SDK is Stainless-generated, GA, and light: iterator-style streaming (`stream.Next()`/`stream.Current()`/`stream.Err()`), typed errors (`*openai.Error` carrying status + body), `context.Context` throughout, and a confirmed-in-practice `WithBaseURL` path for Groq, Gemini-compat, OpenAI, and local Ollama. Because Groq/Gemini/OpenAI/Ollama all speak the same chat-completions wire, standardizing on one client with a per-provider base URL keeps any latency benchmark apples-to-apples (identical client overhead, identical SSE handling) and deletes the duplicated machinery the two adapters carried.

The keyless `httptest` suite that pinned the old wire shape (request body, headers, tool-call decode, tool-result round-trip, malformed-frame truncation, non-2xx errors) **passed unchanged against the SDK path**, confirming wire compatibility before any test was rewritten.

## Considered options

- **Keep the hand-rolled `net/http` adapters (status quo)** — rejected: two near-identical copies of SSE framing, tool-call accumulation, and error taxonomy to own and keep in sync; zero-dep, but the maintenance cost outweighs it now that a lean official SDK exists.
- **`mozilla-ai/any-llm-go`** (the README's original claim) — rejected: it wraps the same official SDKs behind a second abstraction we already have in `llm.Provider` (a wrapper around a wrapper), and its single module pulls every provider SDK plus the Google cloud/gRPC/websocket stack regardless of which provider you import; young and pre-1.0.
- **`zendev-sh/goai`** (stdlib-only, 25+ providers) — rejected: we'd still wrap its abstraction in ours, and it is young and effectively single-maintainer.
- **Move Gemini to native `google.golang.org/genai`** (the research's stage-1 pairing) — rejected here: it contradicts ADR-0036's deliberate choice of the compat endpoint (the tool-result-by-id seam and shared embeddings auth) and pulls `cloud.google.com/go` + auth + gRPC + websocket. Revisit only if/when Gemini Live (S2S) lands, which needs `genai` anyway.
- **Migrate Anthropic to `anthropic-sdk-go`** — rejected: not OpenAI-compatible (outside this adapter), it is the cassette-recording reference, and it would add the AWS SDK v2 + Google auth stacks for first-party use.

## Caveats (and the one real behaviour change)

- **The SDK owns SSE framing now.** The hand-rolled reader capped each SSE line (1 MiB) and the whole raw stream (16 MiB) as a guard against a hostile/misbehaving `WithBaseURL` gateway. The core keeps the whole-stream guard as a **decoded**-bytes cap (summed content + tool-argument bytes, 16 MiB); a single oversized SSE line is now bounded by the SDK's own decoder rather than by us. Acceptable for the BYOK/self-hosted-gateway threat model.
- **Auto-retries are off** by design (above); a flaky endpoint surfaces as an error to the resilience layer, not a silent in-SDK backoff.
- **Compat-endpoint field gaps.** Groq/Gemini reject a few OpenAI-only fields (`logprobs`, `n>1`, …); the adapter never sends them.
- **`content` is always present now.** The hand-rolled adapters tagged message content `omitempty`, so an empty-text turn omitted the `content` key; the SDK helpers send `"content":""`. This is intentional and harmless (it is more spec-correct — a tool message's `content` is API-required — and Groq/Gemini tolerate it). The only reachable case is an empty tool result; the assistant-with-tool-calls turn still omits an empty spoken preamble.
- **Library churn.** Pinned at `openai-go/v3 v3.40.0`; re-check the streaming/union API at upgrade time (Stainless regenerates aggressively).

## Relationship to other ADRs

- **Implements ADR-0036's "no new wire protocol."** That ADR moved the deployment LLM to Llama 3.3 70B on Groq as a "base-URL + key + model swap over the same streaming/tool-call machinery." The machinery is now the OpenAI SDK; the swap is still base URL + key + model.
- **No change to ADR-0021** (cassette determinism): unit tests stay keyless via cassettes, and the `-tags=record` recording client (Anthropic) is untouched.
- **No change to ADR-0028** (tool seam): the tool-result-by-id mapping is preserved identically across compat providers.
- **Preserves ADR-0035** (Gemini thinking cap): `reasoning_effort`/`thinking_budget`, their mutual exclusivity, and the `"low"` default remain on the `gemini` preset, now expressed through the SDK's typed field and JSON-set escape hatch. The live A/B harness (`thinking_live_test.go`) is unchanged.
- **No change to ADR-0004** (BYOK provider matrix): keys are still per-provider env vars resolved at request time; the SDK is always pinned to the configured key and base URL so it can never fall back to `OPENAI_API_KEY` / `OPENAI_BASE_URL`.
