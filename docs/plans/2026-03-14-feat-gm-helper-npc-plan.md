---
title: "feat: GM Helper NPC — Identity, Routing, and Transcript Labels"
type: feat
status: active
date: 2026-03-14
issue: "#37"
brainstorm: docs/brainstorms/2026-03-14-gm-helper-npc-brainstorm.md
---

# feat: GM Helper NPC — Identity, Routing, and Transcript Labels

## Overview

Wire the existing `gm_helper: true` config flag into a fully differentiated GM
assistant NPC. The GM helper gets a merged system prompt (GM-assistant preamble +
user personality), passive address-only routing, `BudgetStandard` by default, and
`(GM)` / `(GM assistant)` transcript labels. All required tools (dice, rules,
memory L1/L2/L3) already exist and are registered — this work is purely about
NPC differentiation and routing.

## Problem Statement / Motivation

The `gm_helper: true` flag exists on `NPCConfig` but is only used to select a
voice for session recaps. Players and GMs cannot actually interact with a
differentiated GM assistant during live sessions. The tools exist (dice rolling,
rules lookup, memory queries) but no NPC is wired to leverage them as a
dedicated helper.

## Proposed Solution

Three changes across four phases:

1. **Identity propagation** — `GMHelper` and `AddressOnly` flow through the full
   chain: config → NPCIdentity → HotContext → system prompt, and
   config → agentEntry → address detector.
2. **System prompt augmentation** — GM-assistant preamble merged before
   personality text when `GMHelper == true`.
3. **Passive routing** — Generic `address_only` flag skips fallback routing
   steps (last-speaker continuation, single-NPC fallback).
4. **Transcript labels** — Display-time `(GM)` and `(GM assistant)` labels
   via a new `SpeakerRole` field on `TranscriptEntry`.

## Technical Approach

### Architecture

```
Config (YAML)                    Proto (gRPC)              NPC Store (DB)
┌──────────────┐                ┌──────────────┐          ┌──────────────┐
│ NPCConfig    │                │ NPCConfig    │          │NPCDefinition │
│  gm_helper   │                │  gm_helper   │          │  gm_helper   │
│  address_only│                │  address_only│          │  address_only│
└──────┬───────┘                └──────┬───────┘          └──────┬───────┘
       │                               │                         │
       └───────────────┬───────────────┘                         │
                       ▼                                         │
                ┌──────────────┐◄────────────────────────────────┘
                │ NPCIdentity  │     (via ToIdentity)
                │  GMHelper    │
                │  AddressOnly │
                └──────┬───────┘
                       │
          ┌────────────┼────────────┐
          ▼            ▼            ▼
   ┌────────────┐ ┌──────────┐ ┌────────────────┐
   │ agentEntry │ │HotContext│ │TranscriptEntry │
   │ addressOnly│ │ GMHelper │ │  SpeakerRole   │
   └─────┬──────┘ └────┬─────┘ └────────────────┘
         │              │
         ▼              ▼
   ┌────────────┐ ┌──────────────────┐
   │  Detect()  │ │FormatSystemPrompt│
   │ skip in    │ │ prepend preamble │
   │ steps 3+4  │ │ before personality│
   └────────────┘ └──────────────────┘
```

### Implementation Phases

#### Phase 1: Identity and Config [Foundation]

Add fields to data structures across all three NPC definition sources.

**Tasks:**

- [ ] Add `AddressOnly *bool` to `config.NPCConfig` (`yaml:"address_only"`)
  - Use `*bool` so we can distinguish "not set" (nil → default true for GM helper)
    from "explicitly false"
  - `internal/config/config.go`
- [ ] Add defaulting logic in `config.Validate()`: when `GMHelper == true` and
  `AddressOnly == nil`, set `AddressOnly` to `ptr(true)`
  - `internal/config/loader.go`
- [ ] Add `GMHelper bool` and `AddressOnly bool` to `agent.NPCIdentity`
  - `internal/agent/agent.go`
- [ ] Add `GMHelper bool` and `AddressOnly bool` to `npcstore.NPCDefinition`
  - `internal/agent/npcstore/definition.go`
- [ ] Wire new fields in `npcstore.ToIdentity()`
  - `internal/agent/npcstore/definition.go`
- [ ] Add `bool gm_helper = 7` and `bool address_only = 8` to proto `NPCConfig`
  - `proto/glyphoxa/v1/session.proto`
- [ ] Regenerate proto Go code
  - `make proto` (or `buf generate`)
- [ ] Wire new fields in all three identity construction sites:
  - `internal/app/app.go` (standalone/full mode)
  - `internal/app/session_manager.go` (session-based mode)
  - gRPC worker handler that maps proto NPCConfig → agent.NPCIdentity
- [ ] Default `BudgetTier` to `BudgetStandard` when `GMHelper == true` and
  tier is zero-valued — in agent construction sites alongside existing
  `configBudgetTier()` calls
  - `internal/app/app.go`
  - `internal/app/session_manager.go`

**Tests:**

- [ ] Config validation: `gm_helper: true` defaults `address_only` to `true`
- [ ] Config validation: `gm_helper: true` + `address_only: false` allowed (no error)
- [ ] Config validation: `gm_helper: true` + `address_only: true` explicit — no change
- [ ] `npcstore.ToIdentity()` propagates `GMHelper` and `AddressOnly`
- [ ] Budget tier defaults to `BudgetStandard` for GM helper, `BudgetFast` for regular

**Success criteria:** New fields compile, serialize/deserialize in YAML, proto,
and DB; all existing tests pass.

**Estimated effort:** Small — struct field additions, 3 wiring sites.

---

#### Phase 2: System Prompt Augmentation [Core]

Merge a GM-assistant preamble into the system prompt before the user's
personality text.

**Tasks:**

- [ ] Add `GMHelper bool` field to `hotctx.HotContext`
  - `internal/hotctx/assembler.go` (where `HotContext` is defined)
- [ ] Set `hctx.GMHelper = a.identity.GMHelper` in `liveAgent.HandleUtterance`
  after calling `a.assembler.Assemble()` and before `FormatSystemPrompt()`
  - `internal/agent/npc.go:227` (after the assembler returns)
- [ ] Modify `FormatSystemPrompt` to detect `hctx.GMHelper` and prepend the
  GM-assistant preamble before the personality text
  - `internal/hotctx/formatter.go`

**GM-assistant preamble** (draft — iterate during testing):

```
You are a GM assistant helping the Game Master run a tabletop RPG session.
Your role is to answer rules questions, roll dice, and recall campaign
information when asked. You have access to the following tools:

- roll: Evaluate dice expressions (e.g. "2d6+3")
- roll_table: Roll on random tables (wild_magic, treasure_hoard, random_encounter)
- search_rules / get_rule: Look up game rules from the SRD
- search_sessions: Search session transcript history
- query_entities: Find NPCs, locations, items in the knowledge graph
- get_summary: Get a full entity profile
- search_facts: Search for facts across session history and semantic memory
- search_graph: Graph-augmented retrieval for complex knowledge queries

Guidelines:
- Be concise and direct. Players are mid-game and need quick answers.
- Use tools to give accurate information rather than guessing.
- Do not interrupt active roleplay — only respond when directly addressed.
- When rolling dice, always announce the individual rolls and the total.
```

The user's `personality` field is appended after this preamble, so GMs can
customize tone (e.g., "Speak with a dry, sarcastic wit" or "Use formal
academic language").

**Interaction with existing fields:**
- `BehaviorRules` from the NPC config are appended after personality as usual.
  The preamble's "be concise" guideline and user-specified rules coexist — the
  LLM weighs both. If they conflict, the user's explicit rules take precedence
  (they appear later in the prompt).
- `SecretKnowledge` works normally — a GM helper can have secrets too.

**Tests:**

- [ ] `FormatSystemPrompt` with `GMHelper == true` includes preamble text
- [ ] `FormatSystemPrompt` with `GMHelper == true` still includes personality
- [ ] `FormatSystemPrompt` with `GMHelper == false` has no preamble (regression)
- [ ] `FormatSystemPrompt` with `GMHelper == true` and empty personality — preamble only
- [ ] Preamble appears before personality in output order

**Success criteria:** GM helper NPC gets a merged prompt; regular NPCs unchanged.

**Estimated effort:** Small — one new field on HotContext, formatter logic.

---

#### Phase 3: Passive Address-Only Routing [Core]

Make the GM helper (and any future passive NPC) unreachable via fallback routing.

**Tasks:**

- [ ] Add `addressOnly bool` field to `orchestrator.agentEntry`
  - `internal/agent/orchestrator/orchestrator.go:42`
- [ ] Set `addressOnly` from `agent.Identity().AddressOnly` in `orchestrator.New()`
  when building `agentEntry` map
  - `internal/agent/orchestrator/orchestrator.go:72`
- [ ] Set `addressOnly` in `orchestrator.AddAgent()`
  - `internal/agent/orchestrator/orchestrator.go:221`
- [ ] In `AddressDetector.Detect()`, modify step 3 (last-speaker continuation):
  skip if `activeAgents[lastSpeaker].addressOnly`
  - `internal/agent/orchestrator/address.go:80`
- [ ] In `AddressDetector.Detect()`, modify step 4 (single-NPC fallback):
  skip address-only agents when counting unmuted agents
  - `internal/agent/orchestrator/address.go:87-100`

**Edge cases to handle:**

- **GM helper is sole NPC**: Player speaks without naming anyone → step 4 skips
  address-only → `ErrNoTarget`. This is intentional — document it.
- **Last-speaker was GM helper**: Follow-up question without name → step 3 skips
  → falls through to other NPCs or `ErrNoTarget`. This is intentional — players
  must re-address the helper for each question. (A time-windowed grace period
  could be added later as an enhancement.)
- **Muted + address-only**: Mute check happens first in steps 1-2; address-only
  check is additive in steps 3-4. Correct by construction.
- **DM puppet override to GM helper**: Step 2 does NOT check address-only — puppet
  mode always works. Correct.

**Tests:**

- [ ] Address-only NPC reachable via explicit name match (step 1)
- [ ] Address-only NPC reachable via DM puppet override (step 2)
- [ ] Address-only NPC skipped in last-speaker continuation (step 3)
- [ ] Address-only NPC skipped in single-NPC fallback (step 4)
- [ ] Session with only address-only NPC(s) → `ErrNoTarget` for unnamed utterances
- [ ] Mixed session: address-only + regular NPCs — regular NPC receives fallback
- [ ] Muted address-only NPC: unreachable via all steps
- [ ] `AddAgent` with address-only NPC: correctly stores flag

**Success criteria:** GM helper only responds when explicitly addressed or
puppet-overridden; all other routing unaffected.

**Estimated effort:** Small — conditional checks in 2 places in Detect().

---

#### Phase 4: Transcript Labels [Polish]

Add display-time role labels for GM players and the GM assistant NPC.

**Tasks:**

- [ ] Add `SpeakerRole string` field to `memory.TranscriptEntry`
  - `pkg/memory/types.go`
  - Well-known values: `"gm"`, `"gm_assistant"`, `""` (regular)
- [ ] When recording transcript entries for an NPC with `GMHelper == true`,
  set `SpeakerRole = "gm_assistant"`
  - `internal/agent/npc.go` (in HandleUtterance, line 305, and SpeakText, line 424)
- [ ] Add `IsDMByUserID(guildID string, userID string) bool` to `PermissionChecker`
  - Resolves DM status for voice participants (who are identified by user ID,
    not interaction members)
  - Uses cached guild member lookup via Discord API
  - `internal/discord/permissions.go`
- [ ] When recording transcript entries for voice participants identified as GM,
  set `SpeakerRole = "gm"`
  - In the voice pipeline transcript recording path (session runtime)
- [ ] Update `writeTranscriptSection` in formatter.go to render role suffixes:
  `"gm"` → `" (GM)"`, `"gm_assistant"` → `" (GM assistant)"`
  - `internal/hotctx/formatter.go:250`
- [ ] Update Discord embed rendering to show role labels
  - Discord message/embed layer (display concern)
- [ ] Add TODO comment to `internal/mcp/tools/ruleslookup/rules.go`:
  `// TODO(#37): Replace hardcoded SRD rules with pluggable per-campaign rules dataset. Blocked on #34 (Campaign Forge).`

**Design decision — display-time labels, not stored:**

Labels are computed at display time from `SpeakerRole`, NOT baked into
`SpeakerName`. Rationale:
- Searching for "Clark" still matches (no need to also search "Clark (GM assistant)")
- Knowledge graph entity extraction isn't confused by suffixed names
- Labels can change if the same NPC is reconfigured (e.g., helper flag removed)
- `SpeakerRole` is a lightweight metadata field, not a display string

**Tests:**

- [ ] `TranscriptEntry` with `SpeakerRole = "gm_assistant"` renders as `"Clark (GM assistant)"` in formatter
- [ ] `TranscriptEntry` with `SpeakerRole = "gm"` renders as `"MrWong99 (GM)"` in formatter
- [ ] `TranscriptEntry` with empty `SpeakerRole` renders bare name (regression)
- [ ] `IsDMByUserID` returns true for users with DM role
- [ ] `IsDMByUserID` returns true for all users when `dm_role_id` is empty

**Success criteria:** Transcripts show role labels; search/memory unaffected.

**Estimated effort:** Medium — new `IsDMByUserID` requires Discord API guild
member lookup with caching.

## Acceptance Criteria

### Functional Requirements

- [ ] GM helper NPC responds only when explicitly addressed by name or via DM puppet/slash command
- [ ] GM helper NPC's system prompt includes tool guidance preamble merged with user personality
- [ ] GM helper NPC defaults to `BudgetStandard` tier (access to full memory tool suite)
- [ ] Transcripts label GM players as `Name (GM)` and the helper as `Name (GM assistant)`
- [ ] `address_only: true` can be used on any NPC (not GM-helper-specific)
- [ ] `gm_helper: true` implies `address_only: true` unless explicitly overridden
- [ ] Distributed mode (gateway+worker) propagates `gm_helper` and `address_only` via gRPC
- [ ] Multi-tenant NPC store (npcstore) supports `gm_helper` and `address_only` fields
- [ ] Existing routing for non-GM-helper NPCs is completely unchanged

### Non-Functional Requirements

- [ ] Zero latency impact on hot-context assembly (<50ms target maintained)
- [ ] All new code has `t.Parallel()` tests with table-driven subtests
- [ ] Race detector clean (`-race -count=1`)
- [ ] Compile-time interface assertions where applicable

## Explicitly Out of Scope

| Item | Reason | Tracked |
|------|--------|---------|
| `/ask-gm` slash command | Scope control — follow-up PR | Create issue |
| Pluggable per-campaign rules dataset | Blocked on #34 (Campaign Forge) | Add TODO + create issue |
| Hot-reload of `gm_helper`/`address_only` | Non-trivial agent reconstruction; changes require session restart | Document as known limitation |
| Initiative/combat tracking | Separate feature | Issue #37 deferred list |
| Timer/reminder functionality | Separate feature | Issue #37 deferred list |
| Loot & inventory | Separate feature | Issue #37 deferred list |
| Last-speaker grace period for address-only NPCs | Enhancement — players must re-address each question | Future issue if UX feedback warrants it |

## Dependencies & Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| Proto regeneration breaks existing gRPC clients | Build failure | New fields are additive (proto3 default false); backward compatible |
| Preamble text causes poor LLM behavior | UX degradation | Iterate preamble during testing; keep it concise |
| `*bool` for `AddressOnly` in config complicates YAML | Developer confusion | Document clearly; add config validation test |
| `IsDMByUserID` requires Discord API call | Latency on first call per user | Cache guild member roles; only needed for transcript labels |
| S2S engine + GM helper: tools may not work | Reduced functionality | Log warning in config validation; don't block |

## References & Research

### Internal References

- Brainstorm: `docs/brainstorms/2026-03-14-gm-helper-npc-brainstorm.md`
- Config schema: `internal/config/config.go:195` (NPCConfig)
- Config validation: `internal/config/loader.go:60` (Validate)
- Config diff: `internal/config/diff.go:24` (Diff — does NOT track gm_helper/address_only)
- NPC identity: `internal/agent/agent.go:26` (NPCIdentity)
- Agent construction: `internal/agent/npc.go:120` (NewAgent)
- Hot context: `internal/hotctx/assembler.go:32` (HotContext struct)
- System prompt: `internal/hotctx/formatter.go:22` (FormatSystemPrompt)
- Address detection: `internal/agent/orchestrator/address.go:60` (Detect)
- Orchestrator: `internal/agent/orchestrator/orchestrator.go:42` (agentEntry)
- NPC store: `internal/agent/npcstore/definition.go:27` (NPCDefinition)
- Proto: `proto/glyphoxa/v1/session.proto:18` (NPCConfig message)
- Permissions: `internal/discord/permissions.go:20` (PermissionChecker)
- Transcript: `pkg/memory/types.go:10` (TranscriptEntry)
- Memory tools: `internal/mcp/tools/memorytool/memorytool.go:327` (NewTools — all implemented)
- Dice tools: `internal/mcp/tools/diceroller/diceroller.go:267` (Tools — implemented)
- Rules tools: `internal/mcp/tools/ruleslookup/ruleslookup.go:106` (Tools — hardcoded placeholder)
- Identity wiring site 1: `internal/app/app.go:351`
- Identity wiring site 2: `internal/app/session_manager.go:566`
- Identity wiring site 3: `internal/agent/npcstore/definition.go:137` (ToIdentity)

### Related Issues

- #33: Introduced `gm_helper` flag for voice recaps
- #34: Campaign Forge — pluggable rules dataset blocked on this
- #37: This issue (GM Helper NPC full functionality)
