# Glyphoxa v2 — Design Tracker

Resume-from-here doc for the interactive design grilling. Decisions are recorded as ADRs under [docs/adr/](docs/adr/); domain language lives in [CONTEXT.md](CONTEXT.md). For what those decisions actually add up to in the tree today, read [docs/architecture.md](docs/architecture.md).

**Next:** Q19 — First sprint scope (held until the live demo runs).

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
| [0027](docs/adr/0027-barge-in-confirm-window-cancels-turn.md) | Barge-in: per-participant confirm window cancels the whole turn | Q13.5 |
| [0028](docs/adr/0028-tool-framework-internal-interface-simple-registry.md) | Tool framework: one internal Tool interface, simple registry, MCP Server as a backing | Q14.1–Q14.3 |
| [0029](docs/adr/0029-tool-grants-least-privilege-scoping-config.md) | Tool Grants: least-privilege, per-grant scoping config enforced in handler | Q14.4 |
| [0030](docs/adr/0030-tool-side-effects-deferred-to-turn-commit.md) | Tool side effects: read-only inline, side-effecting flush at turn-commit | Q14.5 |
| [0031](docs/adr/0031-postgres-migration-tooling.md) | Postgres migration tooling: goose with embedded SQL migrations | Q15 |
| [0032](docs/adr/0032-observability-slog-prometheus-deferred-tracing.md) | Observability: structured slog + thin Prometheus; tracing deferred behind a flag | Q16 |
| [0033](docs/adr/0033-ci-test-strategy.md) | CI/test: keyless-default suite, build-tag-isolated heavy tests, tiered live | Q17 |
| [0034](docs/adr/0034-deployment-artifacts.md) | Deployment: one image (mode as arg), Helm for k8s, systemd for self-host | Q18 |
| [0035](docs/adr/0035-gemini-thinking-cap-reasoning-effort-low.md) | Gemini thinking cap: reasoning effort low | LLM tuning |
| [0036](docs/adr/0036-voice-llm-llama-3-3-70b-on-groq.md) | Voice LLM: Llama 3.3 70B on Groq | LLM provider |
| [0037](docs/adr/0037-openai-go-sdk-for-llm-providers.md) | OpenAI Go SDK for LLM providers (openaicompat) | LLM provider |
| [0038](docs/adr/0038-multi-npc-single-target-default-programmatic-roster.md) | Multi-NPC: single-target default, one shared floor, programmatic roster | multi-NPC epic |
| [0039](docs/adr/0039-mvp-ui-backend-single-operator-web-tier.md) | MVP UI ↔ backend: single-operator self-host web tier | MVP UI integration |
| [0040](docs/adr/0040-transcript-line-persistence.md) | Transcript lines: per-line persistence for Session replay | #74 |
| [0041](docs/adr/0041-operator-allowlist-access-policy.md) | Operator access: mandatory Discord allowlist, no trust-on-first-use | #96, #112, #184 |
| [0042](docs/adr/0042-streaming-stt-speculative-memory-recall.md) | Streaming STT (Scribe v2 Realtime, manual commit) + speculative memory recall | #122, #180 |
| [0043](docs/adr/0043-gateway-fatal-transient-classification.md) | Gateway fatal-vs-transient classification and the connection-state taxonomy | #123 (E6) |
| [0044](docs/adr/0044-provider-retry-policy-and-metric-placement.md) | Provider retry policy and provider-call metric placement | #124, #125 (E6) |
| [0045](docs/adr/0045-provider-usage-metering-estimates.md) | Provider usage metering: event shape, labels, and estimate fallbacks | #127 (E6) |
| [0046](docs/adr/0046-spend-meter-price-map-cap-mechanics.md) | Per-session spend meter: ownership, price map, and cap mechanics | #130 (E6) |
| [0047](docs/adr/0047-discord-invite-resolver-bot-authorization.md) | Discord invite resolver and bot-authorization surface | #101, #105, #110 (E7) |
| [0048](docs/adr/0048-blob-storage-seam-postgres-v1.md) | Blob storage seam: Postgres bytea behind a Store interface in v1 | #283 |
| [0049](docs/adr/0049-background-job-runner.md) | Background work: one minimal DB-backed job runner | #284 |
| [0050](docs/adr/0050-per-speaker-utterance-segmentation.md) | Per-speaker utterance segmentation: N Speaker Lanes, SpeakerID on events | #275 |
| [0051](docs/adr/0051-rollover-tape-consent-retention.md) | Rollover tape: all-participant consent, bounded retention, GM-gated sharing | #303 |
| [0052](docs/adr/0052-kg-write-proposals.md) | Agent KG writes land as GM-reviewed Knowledge Proposals | #298 |
| [0053](docs/adr/0053-campaign-bundle-format.md) | Campaign Bundle: versioned gzipped-JSON export with mandatory secrets exclusion | #287 |
| [0054](docs/adr/0054-saas-plans-platform-keys-usage-ledger.md) | SaaS foundation: Plans as synced data, Subscriptions with price snapshots, Usage Ledger, platform keys | SaaS request 2026-07-17 |

## Open questions

- **Q19 — First sprint scope.** Retrospective / next-sprint planning given what shipped. Held until the live demo runs (needs to see what shipped audibly).
- **MVP UI integration increment.** Scoped by [ADR-0039](docs/adr/0039-mvp-ui-backend-single-operator-web-tier.md): the first web tier that makes the three designed screens (Configuration · Campaign · Session) drive the real voice pipeline, single-operator self-host. Tracked as an Epic + TDD vertical-slice issues on the tracker.

## Methodology notes

- Small reviewable diffs; v1 code is suspect because it was AI-generated.
- Distinguish what *failed* in v1 from what merely *lived* in v1. Identify the specific failure mode before rejecting tooling. (See ADR-0005 vs ADR-0014 — gRPC for audio failed; gRPC for control is fine.)
- Test orchestration, not vendors. STT/TTS/LLM are inputs we trust. (See ADR-0019.)
- Web UI design is anchored to Claude Design handoff bundles. Tokens stay in plain CSS so the design tool can read them; class-name vocabulary stays stable so each new bundle ports cleanly. (See ADR-0017.)
- v1 is reference material, not a source of truth: consult it for a specific kernel (ADR-0007), never trust it wholesale.
