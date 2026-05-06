# Orchestrator-first TDD voice pipeline

The voice pipeline is built orchestrator-first under TDD, not vendor-first. STT, TTS, and LLM are inputs/outputs we trust; the **orchestrator** is the system under test — VAD triggers, Address Detection, turn-taking, barge-in, sentence-commit, Agent + Butler glue. v1's DAVE + Discord WS + Disgo plumbing (cherry-picked per ADR-0007) is bolted on after the orchestrator is solid, not first.

Build sequence:

1. **Slice 1 — TDD voice harness + agent loop (no Discord).** Audio in → behaviour log out → assertions on behaviour. VAD wrapper, STT adapter, address detector, turn-taking gate, barge-in mechanic, sentence-streamed TTS, Agent + Butler.
2. **Slice 2 — Tools + Butler grounding (still no Discord).** MCP host + built-in MCP Tools (`dice.roll`, `rules.lookup`, `transcript_search`). Tested via the same harness.
3. **Slice 3 — Bot skeleton + Discord voice.** Slash commands, `voice_sessions` claim/release, gateway↔voice gRPC, DAVE/MLS handshake, Disgo wiring. The validated orchestrator is bolted onto live audio.
4. **Slice 4 — Web UI live wiring.** SPA developed in parallel against Slice 1's recorded event logs as fixtures; switched to real SSE from Slice 3.

**Why:** v1 wired Discord first and accumulated brittle integration tests around vendor behaviour. The failure mode was "tests are green but nothing reasons correctly" — the orchestrator's logic was never validated in isolation. Building the orchestrator headlessly forces address/turn/barge-in logic to be testable on its own.
