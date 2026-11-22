# llmbench — text-level voice-LLM A/B bench

Plays scripted German tavern conversations against a table of candidate models
and reports, per candidate: how fast the first speakable sentence arrives, how
reliably Tools are used, what leaks into the spoken text, and what it costs.
Built for #702 to decide the voice LLM from measurements.

It is the counterpart of [`voicebench`](../voicebench), which measures the
audio pipeline with one fixed LLM. This bench holds the pipeline fixed (there
is none) and varies the LLM.

## What it drives

A bench turn enters where a live turn enters after Address Detection and ends
where a sentence would be handed to TTS. In between runs the production code,
unmodified:

| Stage | Code |
|---|---|
| Prompt assembly: Persona, speaker roster, spoken-output rule, TTS markup instruction, history | `pkg/voice/agent` (`Replier.ReplyStream`) |
| Tool loop, dice gate, invented-roll guard, tool-syntax fallback, stream scrubber | `pkg/voice/agenttool` (`NewEngine`), `pkg/tool` |
| Sentence splitting, markdown scrub, self-label strip, history commit | `pkg/voice/agent` |
| Provider adapter, with the options production uses | `pkg/voice/llm/{groq,anthropic}` |

Not real: audio (no STT, no TTS — the "voice" only shapes the prompt), and the
store behind the knowledge Tools, which is a small in-memory campaign
(`world.go`) so `recap`, `kg_query`, `transcript_search` and
`remember_knowledge` are callable without a database. `dice` is the real Tool.

The NPC is Flo, a tavern keeper, granted `dice`, `recap`, `transcript_search`,
and `kg_query` / `remember_knowledge` at `own_node` scope (a Character NPC's
grants). Two players address her: Mira, a bard (a woman — the gender-address
check) and Brak, a dwarf warrior.

## Candidates

`Candidates()` in `candidates.go`:

| Name | Provider / model | Client options |
|---|---|---|
| `groq-gpt-oss-120b` | groq / `openai/gpt-oss-120b` | preset default (`reasoning_effort: low`) — the baseline |
| `haiku-5-5-adaptive-low` | anthropic / `claude-haiku-5-5` | adapter default: adaptive thinking, effort low |
| `haiku-5-5-thinking-off` | anthropic / `claude-haiku-5-5` | `WithThinkingFor` → thinking disabled |
| `haiku-4-5` | anthropic / `claude-haiku-4-5` | no thinking fields (the model rejects them) |
| `groq-qwen3.8-27b` (optional) | groq / `qwen/qwen3.8-27b` | preset default (no reasoning field) |

## Corpus

`Scripts()` in `script.go`: four conversations, 34 turns per candidate, each
script played against one Replier so earlier turns are in the prompt of later
ones. Covered: small talk, dice (single, several, summed), recap,
`remember_knowledge` (accepted, and a request the `own_node` grant refuses),
`kg_query` and `transcript_search` lookups, requests no Tool can serve,
arithmetic "reasoning bait", turns where a Tool must not be called, and a
female player character.

Every turn states which Tools a correct reply must call (`Expect`) and which it
may call (`Allow`); anything else — including any call on a no-tool turn — fails
the turn's tool check.

## Running it

```sh
# credentials: GROQ_API_KEY, and ANTHROPIC_API_KEY — an API key, or the `ant`
# CLI's OAuth token (the adapter sends "sk-ant-oat…" as a bearer):
export ANTHROPIC_API_KEY="$(ant auth print-credentials --access-token)"

GX_LLMBENCH_CANDIDATES=all GX_LLMBENCH_OUT=/tmp/llmbench \
  go test -tags "bench live" -run '^TestLLMBench_Live$' -count=1 -v -timeout 40m ./pkg/voice/llmbench/
```

| Variable | Meaning | Default |
|---|---|---|
| `GX_LLMBENCH_CANDIDATES` | comma-separated candidate names; `all` includes the optional arms | the non-optional arms |
| `GX_LLMBENCH_SCRIPTS` | comma-separated script names | all four |
| `GX_LLMBENCH_REPEAT` | passes over the corpus | 1 |
| `GX_LLMBENCH_DELAY` | pause before every turn | 400ms |
| `GX_LLMBENCH_OUT` | output directory | a temp dir (logged) |

Candidates are interleaved at turn granularity (turn 1 for every candidate,
then turn 2, …) and the order rotates, so all candidates are measured over the
same stretch of time. A full run of all five arms is 170 turns and takes about
five minutes.

The live test is a measurement, not a gate: it fails only when the run itself
is broken (a missing credential, a candidate whose every turn errored). It is
compiled by the CI vet of the `live bench` tags and never run there.

## Output

In `GX_LLMBENCH_OUT`:

- `<candidate>.json` — every turn (`TurnResult`) and the `Summary`.
- `<candidate>.replies.md` — every reply as it would be spoken, with tool
  calls and flags, for the naturalness read no metric replaces.
- `comparison.md` — the table.

`comparison.md` holds two tables, one row per candidate each.

**Latency and cost** — timings are milliseconds from the start of the turn,
p50 / p95:

- **first delta** — the first text the provider streamed, in any round.
- **first sentence** — the first complete sentence handed to TTS: what a
  player actually waits for. It is split into **one-round turns** (plain
  conversation) and **tool turns** (at least one tool round came first). On a
  dice turn the production path buffers the whole reply for the invented-roll
  guard, so first sentence equals end there.
- **end** — the whole turn. **rounds / turn** — provider completions per turn.
- **$ / 100 turns** — provider-reported tokens at the `internal/spend` rates.

**Behaviour:**

- **tools OK** — share of all turns with correct tool use; **tool turns OK** —
  the same over the turns that expect a Tool.
- **schema-valid calls** — share of tool calls whose arguments satisfy the
  Tool's declared schema (number of calls in parentheses).
- **prose before tool** — turns in which the model wrote text in the same
  round as a tool call. On the streaming path that text is spoken before the
  Tool runs; it is the channel through which a model narrates or reasons aloud
  ("let me look that up"). The reply dump quotes it.
- **silent** — turns with nothing to speak and no error.
- **guard signals** — how often the production path's own guards fired
  (`observe.MalformedPath`): `stream_error` a provider tool-syntax failure that
  was retried, `text_leak` a pseudo tool call scrubbed from the text,
  `roll_claim` a narrated dice result without a roll (the reply is regenerated
  with the dice Tool forced), `regen_mismatch` a regenerated reply that
  contradicted the roll.
- The detector columns are `spoken (raw)`: findings in what would be spoken,
  and in parentheses in the raw model text before the production scrub. A
  non-zero raw count with a zero spoken count is the scrub doing its job.
  - *tool markup* — pseudo tool calls and leaked elements (every shape
    `pkg/tool` scrubs, any other XML-like element, a bare tool-call JSON, a
    chat-template control token).
  - *stage directions* — asterisk- or parenthesis-wrapped actions, or the NPC
    narrated in the third person.
  - *markdown*, *English* (sentences that read as English), *tool narration*
    (talk of tools, parameters, databases), *gender slips* (a masculine form of
    address to Mira).

The detectors are cheap heuristics (`detect.go`) meant to point a reader at
turns worth reading, not to score prose. A false positive costs a glance at the
reply dump; read the dump before drawing a conclusion from a count of one.

## Results

Dated result tables live in [`results/`](results).
