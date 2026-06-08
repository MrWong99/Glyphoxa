# Sprint 1 Retrospective — Glyphoxa v2 MVP

**Sprint goal:** get a first live NPC ("Bart", the innkeeper) into a Discord voice
channel and hold a real two-way conversation. **Outcome: met.** Merged to `main`
as **6aa3649** (PR #14, 95 files, +12043). Live-tested 2026-06-08: Bart joined
voice, two-way conversation worked, barge-in worked, the dice tool worked.

This retro feeds Sprint 2 planning (Q19 in DESIGN.md). The two biggest findings —
latency and log noise — map directly to the already-open Sprint 2 tasks #2
(latency + benchmarks) and #3 (logging cleanup + monitoring).

## What shipped

The MVP is a single Go binary (`cmd/glyphoxa -mode voice`) running the full
voice loop end to end:

```
Session.Inbound (Opus) → [codec] → VAD (Silero) → STT (ElevenLabs)
  → Address Detection → Agent loop (Gemini + dice tool) → TTS (ElevenLabs)
  → [codec] → Session.Play (Opus)
```

- **Discord audio layer** — `pkg/voice` Manager/Session over disgo, per-speaker
  `Frame.UserID`, DAVE/MLS real encryption behind `-tags dave`.
- **Opus↔PCM codec** (`pkg/voice/wire/codec`) — pure-Go DSP resampler/reframer
  (48 kHz↔16 kHz, 20 ms) + libopus encode/decode behind `-tags opus`, stub by
  default. The audible-demo unblocker.
- **LLM layer** — `llm.Provider` interface + hand-rolled Anthropic SSE adapter
  (cassette/record path) **and** a Gemini adapter (`gemini-2.5-flash`, OpenAI-
  compat endpoint) as the live provider; production ReplyFunc / Agent loop in
  `pkg/voice/agent` assembling Hot Context.
- **Tool framework** (ADR-0028..0030) — `pkg/tool`: fail-closed GrantSet, generic
  tool-use loop (read-only inline), the `dice` built-in; bridged to the LLM via
  `pkg/voice/agenttool`.
- **Outbound speech path** — TTS-stage tee → per-sentence playback pump →
  Opus-encode back to channel; barge-in cancels the turn.
- **Persistence** (ADR-0031) — goose-embedded migrations, `glyphoxa migrate`/
  `seed`, tenant/campaign/agent schema with auto-Butler, pgvector transcript
  chunks; DB-loaded NPC is the default, `-hardcoded` is the no-DB escape hatch.
- **Design closed out** — ADRs 0031–0034 (migrations, observability, CI strategy,
  deployment) landed; only Q19 (this retro) remained open.
- **CI** — default `go test -race ./...` stays keyless/Docker-free/sub-minute;
  testcontainers DB tests and audio/CGO builds are tag-isolated into separate
  jobs (ADR-0033).

## What went well

- **Orchestrator-first TDD + cassettes paid off (ADR-0019/0021).** Every
  component — LLM, Gemini, tool loop, codec, TTS tee — shipped with keyless,
  race-clean, deterministic tests that ran on every commit. The expensive real
  Discord+ElevenLabs+Gemini run was a single coordinated manual smoke, not a
  gate. That is exactly what makes small reviewable diffs affordable.
- **The provider seam held.** Swapping the live LLM from Anthropic to Gemini in
  `internal/wirenpc` needed *no* change to the Agent loop, the tool bridge, or
  the cassette tests — the `llm.Provider` interface absorbed it.
- **Codec isolation worked.** The whole pipeline builds and wires without the
  native libs (stub codec); only the audible run needs `-tags opus`. Wiring was
  reviewable long before libopus was in play.
- **The live test validated the hard parts.** Two-way audio, barge-in, and the
  dice tool-use loop all worked on the first real run.

## What went wrong / was painful

### Technical

- **Latency: Bart sometimes responds VERY late** (Luk's #1 flag). This is an
  observed *symptom*, not a diagnosis — the live log captures join/WARN/ERROR
  lines but **no turn-timing data**, so nothing currently measures
  utterance→first-audio. We cannot yet attribute a cause. One *documented
  hypothesis* to test (not a finding): the crew log flagged `gemini-2.5-flash`
  thinking-token budget vs `max_tokens` as a live-only unknown that keyless tests
  couldn't close. Root-cause analysis is Sprint 2 task #2.
- **Log levels misrepresent severity — and caused a real misdiagnosis mid-test**
  (Luk's #2 flag). During the run where two-way audio *actually worked*, the log
  streamed repeated ERROR/WARN lines that are in fact benign:
  - `ERROR ... failed to DAVE decrypt packet: failed to decrypt frame` (dozens of
    times across the session)
  - `WARN ... skipping undecodable inbound frame ... opus: corrupted stream`

  Because audio worked end to end despite these lines firing continuously, they
  are noise at the wrong level — and a human reading them live misread the run as
  broken. The signal-to-noise of the logs is a correctness-of-operations problem,
  not just cosmetics.
- **Observability is specced but not built.** ADR-0032 already designs the fix —
  a turn-latency histogram (utterance→first-audio) and a small Prometheus surface
  — but none of it is implemented yet. That single gap ties the two pain points
  together: with no latency metric we can't quantify the "late" complaint, and
  with no severity discipline the logs mislead. The pain is "observability isn't
  implemented," and the planned shape already has an ADR.
- **DAVE is mandatory; there is no unencrypted live path.** Discord hard-rejects
  the non-DAVE build (websocket close **4017**), so the live smoke *must* be built
  with `-tags dave` (plus `opus`, `nolibopusfile`) and the libdave native libs.
  There is no keyless/unencrypted shortcut for live audio.

### Process

- **"Reported done but UNCOMMITTED" recurred 4+ times.** Teammates marked tasks
  done while the work sat untracked (branch == base, files untracked). Named
  instances from the crew log: **#1 discord-audio** (caught at integration),
  **#8 persistence** ("2nd case", reverted to in_progress), **architect docs**
  ("4th case", committed only after a nudge). Each one cost an integration-time
  catch and a re-verify cycle. "Done" needs to mean *committed + green*, verified
  by the lead, not self-asserted.
- **Repeated ownership collisions on shared wiring.** `internal/wirenpc.go` and
  `pkg/voice/wire` collided 3× (SequentialSink/PlaybackPump, pcm_48000) until an
  explicit ownership freeze (sole-owned by one teammate until the assembly
  landed). Shared integration surfaces need a single owner from the start.
- **Live-only unknowns couldn't be closed before the smoke test.** Gemini
  streamed-tool-call-id presence and the thinking-token budget were both flagged
  as unverifiable without a paid live run, leaving them open until the very end.

> Note: the detailed branch/worktree state above comes from the crew memory file,
> which is ~3 days old. "What shipped" is anchored on what's verifiable in-repo
> (merge 6aa3649 / PR #14 + ADRs 0031–0034); the process narrative is trusted
> from the crew log.

## Improvement candidates for Sprint 2

Ranked; the top two are the existing open tasks.

1. **Diagnose and fix turn latency (→ task #2).** Build the ADR-0032
   utterance→first-audio histogram first so "late" is measurable, then add
   per-stage timing (VAD/STT/route/LLM/TTS/first-Opus-out) to localize the spend.
   Test the Gemini thinking-token / `max_tokens` hypothesis explicitly. Stand up a
   benchmark harness so latency regressions are catchable, not anecdotal.
2. **Clean up logging + implement monitoring (→ task #3).** Re-level the benign
   DAVE-decrypt and "corrupted stream / skipping undecodable inbound frame" lines
   to DEBUG (or rate-limit/aggregate them) so ERROR means something actionable.
   Then implement the small ADR-0032 Prometheus surface (turn-latency histogram,
   provider call duration/error counters, active-sessions and embedding-backlog
   gauges) on the `/metrics` listener. Verify with a second live run that an
   operator can read the run's health from the logs alone.
3. **Enforce "done = committed + green."** Make the lead verify a teammate's
   branch actually has the commits before integrating; treat a self-reported
   "done" with an untracked tree as not-done. (Already captured as standing
   feedback.)
4. **Assign single owners to shared integration surfaces up front** (e.g.
   `internal/wirenpc`, `pkg/voice/wire`) to avoid the collision/freeze churn.
5. **Close the residual live-only unknowns** during the next live run: confirm
   streamed Gemini tool-call-id correlation and pin a working thinking-token
   budget; fold any findings back into the Gemini adapter and ADRs.
6. **Carry-overs / deferred:** Gemini key rotation (deferred by Luk), the
   transcript-residual scrub, the web/control-plane (#6), and the PR #14 merge to
   `main` itself.
