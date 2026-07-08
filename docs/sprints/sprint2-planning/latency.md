# Sprint 2 Planning — Voice-Loop Latency Diagnosis & Benchmark Plan

**Task:** #2 · **Author:** `latency` · **Scope:** read-only analysis of `Glyphoxa` voice loop. No production code changed.

**Symptom:** "Bart antwortet manchmal sehr spät" — *sometimes* very late responses. The operative word is **manchmal**: this is a **tail-latency / variance** problem, not a uniformly slow loop. The diagnosis below separates causes that explain the *intermittency* from causes that merely inflate the *baseline* every turn.

---

## 1. The loop, stage by stage

```
Session.Inbound (Opus 48k/20ms)
  → codec.DecodeInbound  → reframe to 16k/32ms PCM
  → VAD (Silero v5 / ONNX, per-frame)        ── publishes VADSpeechStart / VADSpeechEnd
  → Segmenter buffers frames between start/end
  → STT (ElevenLabs scribe_v2, one POST)     ── publishes STTFinal
  → Address Detection (in-process scoring)   ── publishes AddressRouted
  → Agent loop (agenttool.Engine)            ── Gemini 2.5-flash, OpenAI-compat /chat/completions, dice tool
  → TTS (ElevenLabs eleven_v3 /stream)       ── publishes TTSInvoked; chunks tee'd to PlaybackPump
  → codec.PlaybackSource (encode to Opus 48k)
  → Session.Play (Discord, auto-interrupts)
```

Key wiring facts (from `internal/wirenpc/wirenpc.go`):
- VAD config: `SampleRate=16000`, `FrameSizeMs=32` → **512-sample frames**; `minSilenceFrames` default **15** (silero default, not overridden).
- LLM: `gemini-2.5-flash` via `agenttool.NewEngine(provider, grants{dice}, model, maxTokens=0, maxRounds=0)`. `maxTokens=0` → `DefaultMaxTokens=2048`; `maxRounds=0` → `tool.DefaultMaxRounds=8`.
- Reply dispatch: the agent returns **one** `orchestrator.Reply` holding the **entire** turn text as a single "sentence". Barge-in confirm window = 0 (instant cut).
- Playback: `PlaybackPump` serializes sentences; **no pre-synthesis pipelining** (its own doc: "inter-sentence gap is N+1's TTS startup latency").

---

## 2. Per-stage latency budget

Estimates are for a warm path, single speaker, short NPC reply. "Where measured" names the timestamp pair or hook.

| Stage | Est. (warm) | Variance | Where it's measured |
|---|---|---|---|
| Codec decode (per 20ms frame) | <1 ms/frame | low | wrap `codec.DecodeInbound` |
| **VAD end-of-speech hangover** | **480 ms** (15×32ms) | **fixed** | `minSilenceFrames` × `FrameSizeMs`; this is detection lag *after* the speaker actually stopped |
| Segmenter flush | <1 ms | low | in-process, negligible |
| **STT (ElevenLabs scribe POST)** | 300–900 ms | **medium** | `AddressRouted.At − STTFinal.At` is downstream; STT itself = STTFinal.At − VADSpeechEnd.At minus address cost. One full HTTP round-trip on a fresh `http.Client{}` |
| Address detection | <1 ms | low | `STTFinal.At → AddressRouted.At` (in-process scoring matcher) |
| **LLM — Gemini turn (agenttool loop)** | **0.8–6 s+** | **HIGH** | `AddressRouted.At → TTSInvoked.At`; this span swallows *all* Gemini rounds + tool exec. The dominant and most variable stage |
| TTS time-to-first-byte | 200–500 ms | medium | `Synthesize()` call → first `AudioChunk` (new hook needed) |
| TTS total (per sentence) | ~real-time of speech | n/a | streamed; not on the critical first-audio path |
| Codec encode | <1 ms/frame | low | wrap `codec.PlaybackSource` |
| Session.Play → audible (Discord) | 40–150 ms | medium | not observable in-process; jitter buffer + network |

**Critical path to first audio (engineering view):**
`VADSpeechEnd → STT(300–900) → address(~0) → Gemini turn(800–6000+) → TTS TTFB(200–500)` ≈ **1.3 s best case, 7 s+ on a bad turn.**

**User-perceived view** adds the 480 ms hangover *before* VADSpeechEnd (the user already stopped talking) and the ~40–150 ms Discord tail *after* first audio. So a "bad" turn the user experiences as ~**2 s → ~7.5 s**, and that spread is exactly the "manchmal" complaint.

---

## 3. Ranked root-cause hypotheses

### Variable causes — these explain *"sometimes"* (rank by likelihood × impact)

**H1 — Gemini 2.5-flash dynamic "thinking" is uncapped in wall-time. (PRIMARY)**
`pkg/voice/llm/gemini/complete.go` sends only `max_tokens` in the body — **no** `reasoning_effort`, no `thinkingConfig`/`thinkingBudget`. On 2.5-flash via the OpenAI-compat endpoint, thinking is **dynamic by default**: the model decides how many reasoning tokens to spend per input, charged against the 2048 ceiling but **unbounded in time**. A trivial "Bart, noch ein Bier?" thinks little; a question that triggers reasoning can spend seconds *before the first content token streams*. This is the single best match for input-dependent intermittency and is **directly testable** (pin thinking low vs. default and compare the *distribution*, not the mean).

**H2 — Tool-loop adds N sequential Gemini round-trips when dice fires.**
`agenttool` + `tool.Loop.Run`: each round is `Generate → execute tool → Generate again`, **sequential**, capped at `MaxRounds=8`. A turn where Gemini calls `dice` is **≥2** full Gemini completions back-to-back (each with its own thinking budget per H1); a confused model can chain several. Turns that *don't* roll dice skip this entirely → bimodal latency. `tool_choice:"auto"` means the model, not us, decides — so it's input-dependent variance stacked on top of H1.

**H3 — Cold HTTP connections / idle re-handshake.**
Both adapters use a bare `http.Client{}` (gemini `gemini.go`, EL stt/tts) with no tuned `Transport`. The first call after an idle gap pays full TLS+TCP setup to `generativelanguage.googleapis.com` and `api.elevenlabs.io`. In a quiet voice channel (gaps between utterances), connections go idle and the *next* turn eats a fresh handshake → a slow turn after silence, fast turns in rapid back-and-forth. Classic "manchmal".

**H4 — Provider-side network/queue jitter.**
STT and TTS are remote single round-trips; ElevenLabs/Gemini p99 >> p50 under load. Out of our control but must be *measured* so we don't chase it in code.

### Constant-baseline causes — real, worth fixing, but do NOT explain *intermittency*

**B1 — 480 ms VAD end-of-speech hangover.** `minSilenceFrames=15 × 32ms`. Bart can't even *start* STT until ~half a second after the user stops. Fixed cost on every turn. Tunable: `WithMinSilenceFrames(8)` → 256 ms is a cheap, safe baseline win (validate against false speech-end / clipped tails).

**B2 — Streaming is fully defeated; first audio waits for the *complete* final-round text.** Gemini streams SSE, but: `agenttool.providerAdapter.Generate` drains the whole stream into one `AssistantMessage`; `agent.turn` calls `engine.Generate` which blocks until that returns; the turn then emits **one** `Reply` = the entire text as a single sentence. So `TTS.Dispatch` (and thus first audio) cannot begin until the *last* token of the *last* round is in hand. The per-sentence `PlaybackPump` is already built for incremental speech — **it's just being fed one sentence.** This inflates every turn by (full-completion-time − first-sentence-time) and is the **highest-leverage fix**: segment the streamed text into sentences and dispatch each as it completes.

**B3 — No TTS pre-buffering across sentences.** Once B2 is fixed and we have multiple sentences, the pump back-pressures sentence N+1's synthesis until N finishes playing → an audible inter-sentence gap = N+1's TTS startup. Deferred, but note it so B2 doesn't just relocate the stall.

**Headline:** Fix **H1** (cap thinking) and **B2** (sentence-stream to TTS) and the loop goes from "1.3–7 s, unpredictable" to "sub-second to first audio, tight tail". B1 is a one-line tuning win. H2/H3 are measured-then-decided.

---

## 4. Proposed instrumentation

**Principle (per advisor):** most of this is observable *today* from the bus — every `Publish` already stamps `At: time.Now()` (`VADSpeechStart/End`, `STTFinal`, `AddressRouted`, `TTSInvoked` — see `orchestrator/vad.go`, `stt.go`, `tts.go`). One bus subscriber can derive most stage deltas without touching adapter code. Only two things need new hooks.

### Derivable today (single bus subscriber, keyed by a per-turn `turn_id`)
- `voice_stt_request_ms` ≈ `STTFinal.At − VADSpeechEnd.At` (minus a near-zero address cost; or instrument the EL call directly for a clean split)
- `voice_address_detect_ms` = `AddressRouted.At − STTFinal.At`
- `voice_llm_turn_ms` = `TTSInvoked.At − AddressRouted.At` (full agenttool loop incl. all rounds + tool exec)

### New hooks needed (2)
1. **First-audio-out** — the one thing not currently observable. Stamp the moment the **first `AudioChunk` reaches `PlaybackPump.HandleSentence`** (the `TeeSynthesizer`→pump boundary in `pkg/voice/wire`). This closes the headline SLO. → `voice_tts_ttfb_ms` and the end-to-end `voice_response_latency_ms`.
2. **Per-LLM-round span** — to separate H1 (thinking) from H2 (extra rounds), instrument inside `agenttool.providerAdapter.Generate`: one `voice_llm_round_ms` per `Provider.Complete` with labels `round_index`, `had_tool_call`. Optionally a finer `llm_first_token_ms` (Complete → first `EventText`/`EventToolCall`) which isolates thinking time from generation time — the cleanest H1 signal.

### Correlation
Stamp a `turn_id` at `STTFinal` and propagate it through `AddressRouted → reply → TTSInvoked → first-audio` so one turn's spans join up. (These metric names are shared with task #3 / `observability` — see the message thread; keep them identical so benchmark and prod dashboards align.)

### Metric vocabulary (shared with #3)
`voice_vad_hangover_ms`, `voice_stt_request_ms`, `voice_address_detect_ms`, `voice_llm_round_ms` (labels round_index, had_tool_call), `voice_llm_turn_ms`, `voice_tts_ttfb_ms`, `voice_tts_total_ms`, `voice_codec_decode_ms`, `voice_codec_encode_ms`, and the headline `voice_response_latency_ms`. Labels: `guild`, `agent_id`.

---

## 5. Benchmark plan

**Build on what exists — do not invent a new harness.** Reuse `pkg/voice/voicecassette` (VCR record/replay, ADR-0021) and `pkg/voice/voicetest.Harness` (already subscribes to every bus event and snapshots them with timestamps — `voicetest/harness.go`). The benchmark is a thin layer that runs clips through the real `orchestrator.Conversation` and reads the timestamped event log the Harness already collects.

### Two modes
1. **Cassette-replay (deterministic, zero network).** STT/TTS/LLM served from cassettes. Isolates **orchestration + CPU overhead** (VAD inference, segmentation, bus, codec). Catches regressions in our own code with no provider noise. Fast, runs in CI.
2. **Live (the real point here).** Real ElevenLabs + real Gemini. Measures the actual round-trips, **thinking variance (H1)**, tool-loop rounds (H2), and cold-connection effects (H3). Gated behind keys + a build tag, run as a scheduled job, never in unit CI.

### What to measure — distributions, not means
The complaint is a **tail**, so report **p50 / p95 / p99** (and max) per stage and end-to-end. A mean hides exactly the "manchmal". Run each clip set N≥30 in live mode.

### Harness design
- **Input:** a fixed corpus of utterance clips under `tests/voice-clips/` — mix of (a) trivial replies (no tool), (b) dice-triggering prompts (forces H2), (c) reasoning-bait prompts (forces H1).
- **Drive:** feed each clip through the inbound codec → real `Conversation` (same wiring as `wirenpc`). The clip's trailing silence makes VAD fire `VADSpeechEnd` naturally, so the 480 ms hangover sits **inside** the measured budget.
- **Collect:** the `voicetest.Harness` event log + the two new hooks (first-audio, per-round). Compute the stage deltas in §4.
- **A/B knobs** (the experiments that confirm hypotheses):
  - H1: pin Gemini thinking low (add `reasoning_effort`/`thinkingBudget` — *in a branch*, this report doesn't change code) vs. default → compare `voice_llm_round_ms` distribution.
  - H2: dice-prompt corpus vs. no-tool corpus → confirm bimodal `voice_llm_turn_ms`.
  - H3: warm loop (back-to-back) vs. cold (sleep > idle-timeout between turns) → confirm first-turn-after-idle penalty.
  - B1: `minSilenceFrames` 15 vs. 8 → confirm 480→256 ms shift with no quality regression.
  - B2: current single-sentence vs. a sentence-streaming branch → confirm `voice_response_latency_ms` drop.

### Target SLOs
Two SLOs because two boundaries matter:

| SLO | Boundary | Target p50 | Target p95 |
|---|---|---|---|
| **Engineering** | `VADSpeechEnd → first audio to PlaybackPump` | ≤ 1200 ms | ≤ 2500 ms |
| **User-perceived** | `true end-of-speech → audible in Discord` | ≤ 1700 ms | ≤ 3000 ms |

(User-perceived = engineering + 480 ms hangover + ~40–150 ms Discord tail. In cassette/clip benchmarks anchor at the clip's *true* end-of-speech so the hangover is in-budget; in prod we only have `VADSpeechEnd`, so the engineering SLO is what prod dashboards alert on.)

These targets assume H1+B2 are fixed; current measured p95 is expected to be well above them, which is the baseline this plan establishes before the Sprint-2 fixes land.

---

## 6. One-paragraph summary

Bart is slow *sometimes* because the variable stages are input-dependent: **Gemini 2.5-flash thinks for an uncapped amount of wall-time** (no `reasoning_effort`/`thinkingBudget` is sent — H1), and turns that trigger the **dice tool run 2+ sequential Gemini completions** (H2), with cold-connection handshakes (H3) and provider jitter (H4) on top. Underneath that, every turn pays a **fixed ~480 ms VAD hangover** (B1) and, worse, **first audio waits for the entire LLM completion** because the streamed reply is re-buffered three times and dispatched as one sentence (B2) — even though the per-sentence playback pump is already built for incremental speech. Fix H1 (cap thinking) and B2 (sentence-stream to TTS) for the big wins; tune B1 for a cheap one; measure H2/H3 with a cassette+live benchmark harness built on the existing `voicecassette`/`voicetest` plumbing, reporting p50/p95/p99 against an engineering SLO of speech-end→first-audio ≤1.2 s p50.
