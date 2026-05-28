# Glyphoxa v2 — Design Tracker

Resume-from-here doc for the interactive design grilling. Decisions are recorded as ADRs under [docs/adr/](docs/adr/); domain language lives in [CONTEXT.md](CONTEXT.md).

**Next:** Q13.5 — Barge-in mechanic specifics.

## Decisions ledger

| ADR | Title | Q-source |
|-----|-------|----------|
| [0001](docs/adr/0001-multi-gm-self-hostable.md) | Multi-GM self-hostable platform | Q1 |
| [0002](docs/adr/0002-tenant-as-organization-with-roles.md) | Tenant as organization with role-based members | Q2, Q3 |
| [0003](docs/adr/0003-players-not-tenant-members.md) | Players are not tenant members (Discord-identity default) | Q4 |
| [0004](docs/adr/0004-byok-provider-key-matrix.md) | BYOK provider keys with two-providers-per-component matrix | Q5 |
| [0005](docs/adr/0005-single-binary-modes-no-audio-rpc.md) | Single binary with modes; no audio across process boundaries | Q6 |
| [0006](docs/adr/0006-dave-mls-no-mid-session-migration.md) | DAVE/MLS at session start; no mid-session migration | Q6.5 |
| [0007](docs/adr/0007-cherry-pick-kernels-from-v1.md) | Cherry-pick kernels from v1, rewrite the rest | Q7 |
| [0008](docs/adr/0008-postgres-knowledge-graph-layered.md) | Postgres-backed knowledge graph, layered v1.0 → v2.x | Q8 |
| [0009](docs/adr/0009-single-agent-table-auto-butler.md) | Single Agent table; Butler auto-created per Campaign | Q9, Q9.5 |
| [0010](docs/adr/0010-slash-command-surface.md) | Slash command surface: 6 commands, mixed flat/grouped | Q10 |
| [0011](docs/adr/0011-transcript-chunks-async-embeddings.md) | Transcript chunks with async embeddings (pgvector) | Q11 |
| [0012](docs/adr/0012-deliver-then-commit-sentence.md) | NPC turn-end commits delivered sentences only | Q11.5 |
| [0013](docs/adr/0013-spa-vite-react-18.md) | Web app is a SPA (Vite + React 18) | Q12.1 |
| [0014](docs/adr/0014-grpc-bus-plus-sse.md) | gRPC bus to gateway + SSE to browser | Q12.2 |
| [0015](docs/adr/0015-buf-connect-end-to-end.md) | Buf Connect end-to-end RPC surface | Q12.3 |
| [0016](docs/adr/0016-cookies-discord-only-oauth.md) | Cookie sessions + Discord-only OAuth in v1.0 | Q12.4 |
| [0017](docs/adr/0017-radix-plus-plain-css-tokens.md) | Radix + plain CSS tokens; class vocab anchored to Claude Design | Q12.5 |
| [0018](docs/adr/0018-tanstack-router-and-query.md) | TanStack Router + Query + connect-query | Q12.6 |
| [0019](docs/adr/0019-orchestrator-first-tdd-voice.md) | Orchestrator-first TDD voice pipeline | Q13 |
| [0020](docs/adr/0020-shared-voice-event-taxonomy.md) | Shared event taxonomy across tests and SSE | Q13.2 |
| [0021](docs/adr/0021-cassette-based-llm-determinism.md) | Cassette-based LLM determinism with tiered live runs | Q13.3 |
| [0022](docs/adr/0022-tts-provider-interface.md) | TTS provider interface: small core, opt-in capabilities, opaque markup | TTS interlude (Q1–Q9) |
| [0023](docs/adr/0023-tts-provider-matrix-elevenlabs-openai.md) | TTS provider matrix v1.0: ElevenLabs + OpenAI (amends ADR-0004) | TTS interlude |
| [0024](docs/adr/0024-address-detection-deterministic-fuzzy-chain.md) | Address Detection: deterministic fuzzy chain on raw STT | Q13.4 |
| [0025](docs/adr/0025-ensemble-turns-speculative-lead-reaction.md) | Ensemble turns: speculative lead + cross-talk reaction | Q13.4 |
| [0026](docs/adr/0026-bus-wiring-reactors-and-conversation.md) | Voice bus wiring: typed reactors composed into a Conversation | slice 1 wiring |

## Open questions

- **Q13.5 — Barge-in mechanic specifics.** VAD threshold for trigger (ms voiced energy from a participant during NPC TTS); interaction with the sentence-commit point in ADR-0012; recovery (does cancelled NPC re-attempt or stay silent?); soft-overlap edge case ("mhm" non-barge-in); **and whether a human barge-in cancels a queued Ensemble-Turn Reaction (ADR-0025) or only the current Lead.**
- **Q14 — MCP tool framework.** Built-in tool list, external tool support, sandboxing.
- **Q15 — Migration / DB tooling.** Postgres migration framework, schema versioning.
- **Q16 — Observability.** Metrics, traces, log structure (defer the v1 OTel sprawl?).
- **Q17 — CI / test strategy.** Tight reviews + small commits → test patterns and gates.
- **Q18 — Deployment artifacts.** Docker image, Helm chart, systemd?
- **Q19 — First sprint scope.** What's shipped in week 1, week 2, week 3?

## Methodology notes

- Small reviewable diffs; v1 code is suspect because it was AI-generated.
- Distinguish what *failed* in v1 from what merely *lived* in v1. Identify the specific failure mode before rejecting tooling. (See ADR-0005 vs ADR-0014 — gRPC for audio failed; gRPC for control is fine.)
- Test orchestration, not vendors. STT/TTS/LLM are inputs we trust. (See ADR-0019.)
- Web UI design is anchored to Claude Design handoff bundles. Tokens stay in plain CSS so the design tool can read them; class-name vocabulary stays stable so each new bundle ports cleanly. (See ADR-0017.)
- v1 lives at `/home/luk/Desktop/git/Glyphoxa` for reference (do not trust wholesale).
