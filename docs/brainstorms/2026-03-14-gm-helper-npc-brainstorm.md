---
date: 2026-03-14
topic: gm-helper-npc
issue: "#37"
---

# GM Helper NPC — Implementation Design

## What We're Building

Wire up the GM helper NPC (`gm_helper: true`) as a differentiated, tool-equipped assistant that leverages the **already-existing** tool suite (dice roller, rules lookup, memory L1/L2/L3) and behaves as an address-only passive agent.

Three concrete changes:

1. **GM helper identity propagation** — `GMHelper` flag flows through `NPCIdentity` → `HotContext`, enabling system prompt augmentation and transcript labeling.
2. **Passive/address-only routing** — A generic `address_only` config flag on any NPC; the GM helper defaults to it. Excludes the NPC from fallback routing (last-speaker continuation, single-NPC fallback).
3. **Transcript role labels** — GM players show as `Name (GM)`, the GM helper NPC shows as `Name (GM assistant)` in transcripts and Discord embeds.

## What Already Exists

All tools are implemented and registered:

| Tool | Package | Layer | Registration |
|------|---------|-------|-------------|
| `roll`, `roll_table` | `diceroller` | — | Stateless (global) |
| `search_rules`, `get_rule` | `ruleslookup` | — | Stateless (global) |
| `search_sessions` | `memorytool` | L1 | Tenant-scoped (app.go) |
| `query_entities` | `memorytool` | L3 | Tenant-scoped (app.go) |
| `get_summary` | `memorytool` | L3 | Tenant-scoped (app.go) |
| `search_facts` | `memorytool` | L1+L2 | Tenant-scoped (app.go) |
| `search_graph` | `memorytool` | L3 GraphRAG | Tenant-scoped (app.go) |

Embedding provider is configurable (`openai`, `ollama`) via `providers.embeddings` in YAML. Memory tools gracefully degrade to FTS-only when embeddings are unavailable.

## Why This Approach

The tools and memory layers exist — the gap is purely in NPC differentiation and routing. Adding a generic `address_only` flag (rather than GM-helper-specific routing logic) keeps the orchestrator reusable for other passive NPCs (e.g., a narrator NPC that only speaks when invoked).

## Key Decisions

### 1. `GMHelper` on `NPCIdentity`

Add `GMHelper bool` to `agent.NPCIdentity` (internal/agent/agent.go). Propagated from `config.NPCConfig.GMHelper` during agent construction.

**Effects:**
- `hotctx.HotContext` gains a `GMHelper bool` field so `FormatSystemPrompt` can detect it.
- When `GMHelper == true`, the formatter prepends a GM-assistant preamble *before* the user's personality text. The preamble covers:
  - Role: "You are a GM assistant helping the Game Master run a tabletop RPG session."
  - Tool guidance: brief description of available tools and when to use them.
  - Behavior: be concise, don't interrupt roleplay, only respond when addressed.
- The user's `personality` field is appended after the preamble, so GMs can still customize the assistant's tone/voice.

### 2. `AddressOnly` routing flag

Add `AddressOnly bool` to `config.NPCConfig` (`yaml:"address_only"`).

Propagate to `agent.NPCIdentity` as `AddressOnly bool`.

In `orchestrator/address.go`, `Detect()` changes:
- Step 3 (last-speaker continuation): skip agents where `AddressOnly == true`.
- Step 4 (single-NPC fallback): skip agents where `AddressOnly == true`.
- Steps 1 (explicit name match) and 2 (DM puppet override) remain unchanged — address-only NPCs are always reachable by name or slash command.

When `gm_helper: true` is set and `address_only` is not explicitly configured, default `address_only` to `true`.

### 3. Default budget tier

When `GMHelper == true` and `BudgetTier` is zero-valued (not explicitly set), default to `BudgetStandard` instead of `BudgetFast`. This lets the GM helper use the full memory tool suite (search_facts P50=200ms, search_graph P50=300ms exceed BudgetFast's 500ms ceiling but fit within BudgetStandard's 1500ms).

### 4. GM player identification for transcript labels

Use the existing `PermissionChecker.IsDM()` (internal/discord/permissions.go) which checks for a configured `dm_role_id` Discord role. Players with this role get `Name (GM)` labels in transcripts.

When `dm_role_id` is empty (test/dev setups), the session creator is treated as GM.

The GM helper NPC gets `Name (GM assistant)` labels. This is derived from `NPCIdentity.GMHelper` when formatting transcript entries for display.

### 5. Rules dataset — leave as placeholder

The hardcoded 20-rule D&D 5e SRD dataset in `ruleslookup/rules.go` stays as-is with a TODO comment. A pluggable per-campaign rules system will be tracked in a new issue, blocked on #34 (Campaign Forge) which defines the campaign format going forward.

### 6. Discord slash command integration

The GM helper can also be invoked via Discord slash commands (bypassing the voice router entirely). A `/ask-gm <question>` command sends text directly to the GM helper agent's `HandleUtterance`, and the response is posted as a Discord embed + voice in the channel. This is additive and can land in the same PR or a fast follow-up.

## Files to Change

| File | Change |
|------|--------|
| `internal/agent/agent.go` | Add `GMHelper bool`, `AddressOnly bool` to `NPCIdentity` |
| `internal/agent/npc.go` | Propagate new identity fields from `AgentConfig` |
| `internal/config/config.go` | Add `AddressOnly bool` to `NPCConfig`; default logic for GM helper |
| `internal/hotctx/types.go` | Add `GMHelper bool` to `HotContext` |
| `internal/hotctx/formatter.go` | GM-assistant preamble injection in `FormatSystemPrompt` |
| `internal/hotctx/assembler.go` | Propagate `GMHelper` from identity into `HotContext` |
| `internal/agent/orchestrator/address.go` | Skip `AddressOnly` agents in fallback steps 3 and 4 |
| `internal/agent/orchestrator/orchestrator.go` | Pass identity info to detector (or store on `agentEntry`) |
| `internal/mcp/tools/ruleslookup/rules.go` | Add TODO comment re: pluggable rules (#34) |
| Transcript display layer (Discord embeds) | Render `(GM)` / `(GM assistant)` labels |

## Open Questions

- **`/ask-gm` slash command**: same PR or follow-up? Leaning follow-up to keep scope tight.
- **Dynamic budget escalation**: "think hard" / "use this specific tool" — how does the user signal this in voice? Likely a prompt-engineering concern (the LLM decides to use deeper tools when the question warrants it) rather than runtime budget switching. Defer to follow-up.

## Deferred (Separate Issues)

- Pluggable per-campaign rules dataset (blocked on #34)
- Initiative tracking / combat management
- Timer/reminder functionality
- Loot & inventory tracking
- Dynamic budget tier escalation via voice commands

## Next Steps

-> `/workflows:plan` for implementation steps, file-level changes, and test strategy.
