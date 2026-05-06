# Cassette-based LLM determinism with tiered live runs

LLM and STT calls in tests use a VCR-style cassette pattern: the first run hits the real Provider and records request + response to disk; subsequent runs replay from disk. Cassettes live at `tests/voice-cassettes/*.yaml` and are committed.

The cassette key includes a `prompt_hash` (sha256 of the rendered prompt). When prompts change, the hash changes, the cassette misses, and the test fails with "no cassette for this hash; run with `-tags=record`". This forces deliberate cassette regeneration on prompt changes and makes them reviewable as diffs in PRs.

Tiered execution:

- `go test ./...` (default) — replay cassettes. Sub-second, free, deterministic. Every PR.
- `go test -tags=record -run=...` — re-records against live Providers. Run when intentionally changing prompts or upgrading vendor models.
- `go test -tags=live` — ~5–10 canonical cases against real APIs. Nightly cron + pre-release. Catches vendor drift between cassette refreshes; failures open a "vendor regression" notification, **don't** block unrelated PRs.

Per-vendor cassette policy:

- **LLM** (Anthropic, Ollama): full cassettes — prompt + response + tool calls. Tool-call routing is the most important thing to pin.
- **STT** (Deepgram, whisper.cpp): cassettes pin transcript text per WAV input.
- **TTS** (ElevenLabs, Coqui): stub cassettes — only "TTS was invoked with sentence N" is recorded; audio output isn't fed back into the test.

**Considered options:**

- **Live-only** — rejected. Slow, costly, flaky → muted tests hide bugs.
- **Seed-based determinism** — rejected. Anthropic doesn't expose a seed; partial coverage at best.
- **Hand-written mocks** — rejected. Tests the mock, not the system.
