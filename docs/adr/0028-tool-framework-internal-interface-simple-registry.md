# Tool framework: one internal Tool interface, simple registry, MCP Server as a backing

v2 has a single internal **Tool** interface (name, input JSON schema, handler). Agents, the orchestrator, and Address Detection only ever see this interface; a Tool's *backing* — **built-in** (in-process Go function, lowest latency) or an **MCP Server** (out-of-process, speaks the Model Context Protocol) — is an implementation detail hidden behind it. "MCP" is the adapter for one backing, not the category; the domain term is **Tool**, not "MCP Tool" (see CONTEXT.md).

## What we build now

- The **tool-use loop** (LLM emits `tool_call` → orchestrator executes → feeds result back as a tool-role message → LLM continues). This is the hard, reusable building block; it is identical for 1 tool or 50 and gets the highest QA attention, cassette-tested per ADR-0019.
- A thin **Tool interface** so the loop is generic, not dice-specific. Handler signature carries the grant config (ADR-0029): `Execute(ctx, args, grantConfig)`.
- A **Registry** (`name → Tool`). We *know* more tools are coming, so the registry exists from day one — but registration is kept dumb (a map, no lifecycle ceremony). The registry API must not bake in in-process assumptions, so a future MCP Server registers by enumerating its tools into the *same* registry.

## What we ship in v1.0

Exactly **one** built-in Tool: `dice` (PoC). Further tools are added when their building blocks land (transcript search needs the ADR-0011 pgvector path wired into a Tool; rules lookup needs an SRD corpus; a consolidated `kg_query` waits on the ADR-0008 KG layer). This amends ADR-0009's default grant set to `dice`-only.

## What we deliberately do NOT build

v1's **calibration probes, rolling-window P50/P99 latency metrics, budget tiers, and the dynamic tier selector** are cut entirely. These were bespoke Glyphoxa inventions, not part of any standard tool-use API — not a *failure* of v1, just complexity we have not earned. They are revisited only if a real performance problem appears. Also deferred: the MCP Server adapter itself and its transports (kept in mind in the registry design, not implemented).

**Why:** the expensive-to-retrofit part is the tool-use loop and the interface boundary; everything else (registry richness, tiers, MCP transports) is cheap to add when a consumer exists. Building one internal interface with MCP as an adapter matches what v1 actually did and keeps the hot path (Hot Context <50ms) free of serialization for built-ins.
