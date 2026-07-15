# Glyphoxa documentation

Glyphoxa is a multi-tenant TTRPG voice-and-knowledge platform: AI Agents
(the Butler and Character NPCs) join a Discord **Voice Session**, voice Campaign
NPCs, and assist the **GM** via Slash Commands and Tools, persisting Transcripts
and a per-Campaign **Knowledge Graph**.

Domain vocabulary is defined once in [../CONTEXT.md](../CONTEXT.md) and used
across every doc here. The decisions ledger is [../DESIGN.md](../DESIGN.md).

## Current docs

Start here — these describe the system as it ships today.

- **[architecture.md](architecture.md)** — current-system overview; every
  subsystem names the ADR(s) that govern it.
- **[configuration.md](configuration.md)** — self-host setup runbook: every
  environment variable, the `.env` template, Postgres/pgvector, Discord OAuth,
  the Operator allowlist, and the build order.
- **[quickstart-gm.md](quickstart-gm.md)** — GM quickstart: from a fresh clone
  to a talking Character NPC in a Discord voice channel.

### [adr/](adr/) — Architecture Decision Records

The 53 ADRs (`0001`…`0053`) recording the decisions behind the system, from
multi-GM self-hosting through the Campaign Bundle format. Where an ADR and the
shipped tree disagree, `architecture.md` follows the tree and says so.

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

## Historical

Kept for reference; **not** maintained and not authoritative for the current
system. Superseded content lives here rather than being deleted.

- **[sprints/](sprints/)** — past sprint plans and planning notes.
- **[latency-investigation/](latency-investigation/)** — an audio-pipeline
  latency investigation.
- **[import/](import/)** — early import/mapping notes
  (Foundry/Roll20 field mapping).
