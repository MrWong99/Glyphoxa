# Glyphoxa documentation

Glyphoxa is a multi-tenant TTRPG voice-and-knowledge platform: AI Agents
(the Butler and Character NPCs) join a Discord **Voice Session**, voice Campaign
NPCs, and assist the **GM** via Slash Commands and Tools, persisting Transcripts
and a per-Campaign **Knowledge Graph**.

Domain vocabulary is defined once in [../CONTEXT.md](../CONTEXT.md) and used
across every doc here. Decisions are the ADRs indexed below.

## Current docs

Start here — these describe the system as it ships today.

- **[architecture.md](architecture.md)** — current-system overview; every
  subsystem names the ADR(s) that govern it.
- **[configuration.md](configuration.md)** — self-host setup runbook: every
  environment variable, the `.env` template, Postgres/pgvector, Discord OAuth,
  the Operator allowlist, and the build order.
- **[quickstart-gm.md](quickstart-gm.md)** — GM quickstart: from a fresh clone
  to a talking Character NPC in a Discord voice channel.

### [deploy/](deploy/) — hosted-deployment runbooks (ADR-0054)

Running Glyphoxa **for others**, beyond the single-machine self-host:

- **[deploy/k3s-proxmox.md](deploy/k3s-proxmox.md)** — the Helm chart on a k3s
  cluster in a Proxmox VM, exposed via DynDNS with Let's Encrypt TLS.
- **[deploy/cloud-providers.md](deploy/cloud-providers.md)** — moving that
  deployment to a paid cloud: provider suggestions and cost estimates.
- **[deploy/legal-pages.md](deploy/legal-pages.md)** — the Impressum /
  Datenschutzerklärung / AUP pages the SPA serves, and the operator identity you
  must fill in before going live.
- **[deploy/backup-restore.md](deploy/backup-restore.md)** — the chart's
  Postgres backup CronJob (local + off-site) and the restore runbook.
- **[deploy/saas-operations.md](deploy/saas-operations.md)** — subscription
  Plans, platform keys, and the cost/revenue report
  (`glyphoxa billing`).

### [adr/](adr/) — Architecture Decision Records

The 64 ADRs recording the decisions behind the system are indexed in full at
[the end of this page](#architecture-decision-records). Where an ADR and the
shipped tree disagree, `architecture.md` follows the tree and says so; later
decisions are recorded as dated amendments inside the original ADR.

### [agents/](agents/) — agent & contributor conventions

- **[domain.md](agents/domain.md)** — how the single-context domain docs
  (`CONTEXT.md` + ADRs) are kept.
- **[issue-tracker.md](agents/issue-tracker.md)** — GitHub Issues workflow via
  the `gh` CLI.
- **[triage-labels.md](agents/triage-labels.md)** — the triage label vocabulary.
- **[live-npc-run.md](agents/live-npc-run.md)** — developer runbook for the
  `voice`-mode live NPC, including the `opus`/`dave` build tags.

### [devs/](devs/) — developer guides

- **[voice-tests.md](devs/voice-tests.md)** — how the voice integration tests
  exercise the orchestrator stage against a deterministic provider seam.

## Architecture Decision Records

| ADR | Decision |
|-----|----------|
| [0001](adr/0001-multi-gm-self-hostable.md) | Multi-GM self-hostable platform |
| [0002](adr/0002-tenant-as-organization-with-roles.md) | Tenant as organization with role-based members |
| [0003](adr/0003-players-not-tenant-members.md) | Players are not tenant members (Discord-identity default) |
| [0004](adr/0004-byok-provider-key-matrix.md) | BYOK provider keys with two-providers-per-component matrix |
| [0005](adr/0005-single-binary-modes-no-audio-rpc.md) | Single binary with modes; no audio across process boundaries |
| [0006](adr/0006-dave-mls-no-mid-session-migration.md) | DAVE/MLS at session start; no mid-session migration |
| [0007](adr/0007-cherry-pick-kernels-from-v1.md) | Cherry-pick kernels from v1, rewrite the rest |
| [0008](adr/0008-postgres-knowledge-graph-layered.md) | Postgres-backed knowledge graph, layered v1.0 → v2.x |
| [0009](adr/0009-single-agent-table-auto-butler.md) | Single Agent table; Butler auto-created per Campaign |
| [0010](adr/0010-slash-command-surface.md) | Slash command surface: 6 commands, mixed flat/grouped |
| [0011](adr/0011-transcript-chunks-async-embeddings.md) | Transcript chunks with async embeddings (pgvector) |
| [0012](adr/0012-deliver-then-commit-sentence.md) | NPC turn-end commits delivered sentences only |
| [0013](adr/0013-spa-vite-react-18.md) | Web app is a SPA (Vite + React 18) |
| [0014](adr/0014-grpc-bus-plus-sse.md) | gRPC bus to gateway + SSE to browser |
| [0015](adr/0015-buf-connect-end-to-end.md) | Buf Connect end-to-end RPC surface |
| [0016](adr/0016-cookies-discord-only-oauth.md) | Cookie sessions + Discord-only OAuth in v1.0 |
| [0017](adr/0017-radix-plus-plain-css-tokens.md) | Radix + plain CSS tokens; class vocabulary anchored to Claude Design |
| [0018](adr/0018-tanstack-router-and-query.md) | TanStack Router + Query + connect-query |
| [0019](adr/0019-orchestrator-first-tdd-voice.md) | Orchestrator-first TDD voice pipeline |
| [0020](adr/0020-shared-voice-event-taxonomy.md) | Shared event taxonomy across tests and SSE |
| [0021](adr/0021-cassette-based-llm-determinism.md) | Cassette-based LLM determinism with tiered live runs |
| [0022](adr/0022-tts-provider-interface.md) | TTS provider interface: small core, opt-in capabilities, opaque markup |
| [0023](adr/0023-tts-provider-matrix-elevenlabs-openai.md) | TTS provider matrix v1.0: ElevenLabs + OpenAI (amends ADR-0004) |
| [0024](adr/0024-address-detection-deterministic-fuzzy-chain.md) | Address Detection: deterministic fuzzy chain on raw STT |
| [0025](adr/0025-ensemble-turns-speculative-lead-reaction.md) | Ensemble turns: speculative lead + cross-talk reaction |
| [0026](adr/0026-bus-wiring-reactors-and-conversation.md) | Voice bus wiring: typed reactors composed into a Conversation |
| [0027](adr/0027-barge-in-confirm-window-cancels-turn.md) | Barge-in: per-participant confirm window cancels the whole turn |
| [0028](adr/0028-tool-framework-internal-interface-simple-registry.md) | Tool framework: one internal Tool interface, simple registry, MCP Server as a backing |
| [0029](adr/0029-tool-grants-least-privilege-scoping-config.md) | Tool Grants: least-privilege, per-grant scoping config enforced in the handler |
| [0030](adr/0030-tool-side-effects-deferred-to-turn-commit.md) | Tool side effects: read-only run inline, side-effecting flush at turn-commit |
| [0031](adr/0031-postgres-migration-tooling.md) | Postgres migration tooling: goose with embedded SQL migrations |
| [0032](adr/0032-observability-slog-prometheus-deferred-tracing.md) | Observability: structured slog + thin Prometheus, tracing deferred behind a flag |
| [0033](adr/0033-ci-test-strategy.md) | CI / test strategy: keyless-by-default suite, build-tag-isolated heavy tests, tiered live |
| [0034](adr/0034-deployment-artifacts.md) | Deployment artifacts: one container image (mode as arg), Helm for k8s, systemd unit for self-host |
| [0035](adr/0035-gemini-thinking-cap-reasoning-effort-low.md) | Cap gemini-2.5-flash "thinking" wall-time: send `reasoning_effort: "low"` by default |
| [0036](adr/0036-voice-llm-llama-3-3-70b-on-groq.md) | Voice-loop deployment LLM: Llama 3.3 70B on Groq, Gemini Flash-Lite as fallback |
| [0037](adr/0037-openai-go-sdk-for-llm-providers.md) | LLM providers on the official OpenAI Go SDK behind a shared OpenAI-compat core |
| [0038](adr/0038-multi-npc-single-target-default-programmatic-roster.md) | Multi-NPC: single-target default, one shared floor, programmatic roster |
| [0039](adr/0039-mvp-ui-backend-single-operator-web-tier.md) | MVP UI ↔ backend: single-operator self-host web tier |
| [0040](adr/0040-transcript-line-persistence.md) | Transcript lines: per-line persistence for Session replay |
| [0041](adr/0041-operator-allowlist-access-policy.md) | Operator access: mandatory Discord allowlist, no trust-on-first-use |
| [0042](adr/0042-streaming-stt-speculative-memory-recall.md) | Streaming STT (Scribe v2 Realtime, manual commit) + speculative memory recall |
| [0043](adr/0043-gateway-fatal-transient-classification.md) | Gateway fatal-vs-transient classification and the connection-state taxonomy |
| [0044](adr/0044-provider-retry-policy-and-metric-placement.md) | Provider retry policy and provider-call metric placement |
| [0045](adr/0045-provider-usage-metering-estimates.md) | Provider usage metering: event shape, labels, and estimate fallbacks |
| [0046](adr/0046-spend-meter-price-map-cap-mechanics.md) | Per-session spend meter: ownership, price map, and cap mechanics |
| [0047](adr/0047-discord-invite-resolver-bot-authorization.md) | Discord invite resolver and bot-authorization surface |
| [0048](adr/0048-blob-storage-seam-postgres-v1.md) | Blob storage seam: Postgres bytea behind a Store interface in v1 |
| [0049](adr/0049-background-job-runner.md) | Background work: one minimal DB-backed job runner, not N bespoke loops |
| [0050](adr/0050-per-speaker-utterance-segmentation.md) | Per-speaker utterance segmentation: N Speaker Lanes, SpeakerID on events |
| [0051](adr/0051-rollover-tape-consent-retention.md) | Rollover tape: all-participant consent, bounded retention, GM-gated sharing |
| [0052](adr/0052-kg-write-proposals.md) | Agent KG writes land as GM-reviewed Knowledge Proposals |
| [0053](adr/0053-campaign-bundle-format.md) | Campaign Bundle: versioned gzipped-JSON export with mandatory secrets exclusion |
| [0054](adr/0054-saas-plans-platform-keys-usage-ledger.md) | SaaS foundation: Plans as synced data, Subscriptions with price snapshots, a durable Usage Ledger, and platform keys on the env-fallback path |
| [0055](adr/0055-self-signup-admission-modes.md) | Self-signup: Admission Modes, create-only Tenant provisioning, and the ADR-0054 entitlement gates going live |
| [0056](adr/0056-player-invitations-linked-player-access.md) | Player Invitations: Linked Player access at per-link Access Levels; transcript text shared by GM opt-in |
| [0057](adr/0057-shared-voice-worker-pool-multi-tenant.md) | Shared voice-worker pool: per-tenant Discord clients, Postgres claim plane, presence owner |
| [0058](adr/0058-guild-ownership-proof.md) | Guild-ownership proof for Discord settings and the guild release path |
| [0059](adr/0059-volatile-hot-context-tail-gm-directives.md) | Volatile Hot Context tail (cache-stable prompts) and the GM directive track |
| [0060](adr/0060-maps-and-pins.md) | World Maps and Pins: positions for the Knowledge Graph, not a VTT |
| [0061](adr/0061-idle-close-voice-session-watchdog.md) | Idle Close: a Voice Instance ends the Voice Sessions nothing is using |
| [0062](adr/0062-butler-planning-chat.md) | Butler planning chat: Connect streaming, persisted threads, per-exchange metering under the allowance gate |
| [0063](adr/0063-campaign-subnav-in-sidebar.md) | Campaign sub-views move into the sidebar, addressed by path |
| [0064](adr/0064-voice-media-path-liveness.md) | Voice media-path liveness: UDP keepalive, receive watchdog, honest close reason |
