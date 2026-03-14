---
title: "feat: GM Helper NPC — Identity, Routing, and Transcript Labels"
type: feat
status: active
date: 2026-03-14
issue: "#37"
brainstorm: docs/brainstorms/2026-03-14-gm-helper-npc-brainstorm.md
deepened: 2026-03-14
---

# feat: GM Helper NPC — Identity, Routing, and Transcript Labels

## Enhancement Summary

**Deepened on:** 2026-03-14
**Research agents used:** architecture-strategist, performance-oracle, security-sentinel,
pattern-recognition-specialist, code-simplicity-reviewer, type-design-analyzer,
spec-flow-analyzer, best-practices-researcher, repo-research-analyst, silent-failure-hunter

### Key Improvements

1. **Replace `*bool` with plain `bool` for `AddressOnly`** — codebase has zero `*bool` fields;
   use `ApplyDefaults()` step instead of mutation-in-validation
2. **Use session's existing `dmUserID` instead of `IsDMByUserID` Discord API** — eliminates
   ~80 LOC, zero latency risk, no new external dependency
3. **Fix `BudgetTier` zero-value collision** — `BudgetFast = iota` (value 0) is
   indistinguishable from "not set"; must fix before budget defaulting
4. **Don't update `lastSpeaker` for address-only agents** — prevents "poisoning"
   conversational continuity after addressing the GM helper
5. **Define typed `SpeakerRole` constant** — matches existing `LogLevel`/`Engine`/`BudgetTier`
   pattern; prevents typo-based silent failures
6. **Persist `SpeakerRole` to database** — player `(GM)` labels can't be recomputed at read
   time; needed for transcript replay and session recaps

### New Considerations Discovered

- Missing npcstore DB migration (`CREATE TABLE IF NOT EXISTS` won't add columns)
- Proto3 bool defaults can silently degrade GM helper in rolling deploys
- Preamble tool list may not match actual registered tools
- NPC name collisions can bypass address-only routing
- `config.Diff()` should log restart-required warning for new fields

---

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

Three changes across three phases:

1. **Identity + Prompt** — `GMHelper` and `AddressOnly` flow through the full
   chain: config -> NPCIdentity -> agentEntry/HotContext -> system prompt.
   GM-assistant preamble merged before personality text when `GMHelper == true`.
2. **Passive routing** — Generic `address_only` flag skips fallback routing
   steps (last-speaker continuation, single-NPC fallback). `Route()` does not
   update `lastSpeaker` for address-only agents.
3. **Transcript labels** — `SpeakerRole` typed constant on `TranscriptEntry`,
   persisted to DB, rendered at display time as `(GM)` / `(GM assistant)`.

## Technical Approach

### Architecture

```
Config (YAML)                    Proto (gRPC)              NPC Store (DB)
+--------------+                +--------------+          +--------------+
| NPCConfig    |                | NPCConfig    |          |NPCDefinition |
|  gm_helper   |                |  gm_helper   |          |  gm_helper   |
|  address_only|                |  address_only|          |  address_only|
+------+-------+                +------+-------+          +------+-------+
       |                               |                         |
       +-- ApplyDefaults() --+         |                         |
       |                     |         |                         |
       +--- Validate() -----+---------+-------------------------+
                       |
                +--------------+
                | NPCIdentity  |     (via IdentityFromConfig / ToIdentity)
                |  GMHelper    |
                |  AddressOnly |
                +------+-------+
                       |
          +------------+------------+
          v            v            v
   +------------+ +----------+ +----------------+
   | agentEntry | |formatter | |TranscriptEntry |
   | addressOnly| | GMHelper | |  SpeakerRole   |
   +-----+------+ +----+-----+ +----------------+
         |              |
         v              v
   +------------+ +------------------+
   |  Detect()  | |FormatSystemPrompt|
   | skip in    | | prepend preamble |
   | steps 3+4  | | before personality|
   +------------+ +------------------+
```

### Implementation Phases

#### Phase 1: Identity, Config, and Prompt [Foundation + Core]

Add fields to data structures, wire identity construction, and augment the
system prompt. Merged from original Phases 1+2 for a single testable vertical
slice from config flag through to prompt output.

**Tasks:**

**Config fields:**

- [ ] Add `AddressOnly bool` to `config.NPCConfig` (`yaml:"address_only"`)
  - Use plain `bool` (not `*bool`) — consistent with all other config booleans
  - `internal/config/config.go`

- [ ] Add `ApplyDefaults(cfg *Config)` function — separate from `Validate()`
  - `Validate()` is currently read-only; defaulting logic must not live there
  - When `GMHelper == true` and `AddressOnly == false`, set `AddressOnly = true`
  - Call `ApplyDefaults()` in `LoadFromReader()` before `Validate()`
  - `internal/config/loader.go`

- [ ] Add config validation warning: `gm_helper: true` with `engine: s2s` logs
  a warning — S2S tool calling is less reliable than cascade mode
  - `internal/config/loader.go`

- [ ] Add config validation warning: `gm_helper: true` with empty `tools` list
  logs a warning — preamble describes tools the NPC cannot call
  - `internal/config/loader.go`

### Research Insights — Config `*bool` vs `bool`

**Best Practices:**
- The codebase has zero `*bool` fields. All booleans in `NPCConfig` use plain `bool`.
- `*bool` is the standard Go idiom for tri-state config (Kubernetes uses it
  extensively), but only when the zero value is semantically different from "not set."
- For `AddressOnly`, `false` is the correct default for non-GM-helper NPCs. The
  only use case for `*bool` is `gm_helper: true` + `address_only: false` override,
  which is a YAGNI scenario — nobody has requested a GM helper that participates
  in fallback routing.

**Decision:** Use plain `bool`. `ApplyDefaults()` unconditionally sets
`AddressOnly = true` when `GMHelper == true`. If the override use case appears
later, converting to `*bool` is a 10-minute change.

**Pattern Consistency:**
- `Validate()` is pure validation (read-only, appends errors, never mutates).
  Defaulting in `Validate()` breaks this contract and causes bugs: proto-to-config
  mapping, `npcstore.ToIdentity()`, and hot-reload paths all skip `Validate()`.
- Separate `ApplyDefaults()` step called between `Load()` and `Validate()` preserves
  the existing contract and ensures defaults are applied in all code paths.

---

**BudgetTier zero-value fix (prerequisite):**

- [ ] Fix `BudgetTier` iota to start at 1 — `BudgetFast = iota` (value 0)
  is indistinguishable from "not set"
  - Add `BudgetUnset BudgetTier = 0` before `BudgetFast`
  - Update `configBudgetTier()` and `tier.Selector.Select()` to handle `BudgetUnset`
  - This also fixes the existing bug where DM override to `BudgetFast` is
    indistinguishable from "no override"
  - `internal/mcp/types.go`, `internal/mcp/tier/selector.go`

### Research Insights — BudgetTier Zero-Value

**Critical existing bug:** `BudgetTier` is `type BudgetTier int` with
`BudgetFast = iota` (value 0). The plan's budget defaulting ("when GMHelper ==
true and tier is zero-valued, default to BudgetStandard") would silently override
an explicit `budget_tier: fast` because `BudgetFast` IS the zero value.

**Fix:** Add `BudgetUnset BudgetTier = 0` and shift `BudgetFast` to 1. This also
fixes `tier.Selector.Select()` where `dmOverride != 0` means "no override" — a
DM currently cannot override to `BudgetFast`.

---

- [ ] Default `BudgetTier` to `BudgetStandard` when `GMHelper == true` and
  tier is `BudgetUnset` — in `ApplyDefaults()` and agent construction sites
  - `internal/config/loader.go` (ApplyDefaults)
  - `internal/app/app.go`
  - `internal/app/session_manager.go`

- [ ] Intersect configured budget tier with tenant license tier ceiling:
  `effectiveTier = min(configuredTier, licenseTierMax)`
  - Prevents tenants from escalating resource consumption via `gm_helper` flag
  - `internal/app/app.go`, `internal/app/session_manager.go`

### Research Insights — Budget Tier Security

**Security finding:** Setting `gm_helper: true` automatically escalates budget
from `BudgetFast` (500ms) to `BudgetStandard` (1500ms). In multi-tenant
deployments, any tenant can set this flag. The effective tier should be capped
by the tenant's license tier ceiling.

---

**Identity fields:**

- [ ] Add `GMHelper bool` and `AddressOnly bool` to `agent.NPCIdentity`
  - `internal/agent/agent.go`

- [ ] Add `IdentityFromConfig(npc config.NPCConfig) NPCIdentity` factory function
  - Centralizes config-to-identity resolution in one place
  - Eliminates the three-site wiring fragility (app.go, session_manager.go,
    npcstore ToIdentity all call this)
  - `internal/agent/agent.go`

### Research Insights — Identity Construction

**Pattern concern:** Three separate construction sites (`app.go:351`,
`session_manager.go:566`, `npcstore/definition.go:137`) must all correctly wire
new fields. Missing a site means the flag silently defaults to `false`.

**Fix:** Add `IdentityFromConfig()` factory function. The three sites become
trivially auditable — they call the factory instead of constructing struct
literals. `npcstore.ToIdentity()` can delegate to this factory or remain
separate since it maps from DB fields, not config.

---

- [ ] Add `GMHelper bool` and `AddressOnly bool` to `npcstore.NPCDefinition`
  - `internal/agent/npcstore/definition.go`

- [ ] Add DB migration: `ALTER TABLE npc_definitions ADD COLUMN IF NOT EXISTS
  gm_helper BOOLEAN NOT NULL DEFAULT FALSE` and same for `address_only`
  - `internal/agent/npcstore/postgres.go`

### Research Insights — Database Migration

**Critical gap:** The npcstore uses `CREATE TABLE IF NOT EXISTS` in `Schema`.
This DDL does not add new columns to existing tables. Without an explicit
`ALTER TABLE ... ADD COLUMN IF NOT EXISTS` migration, existing deployments will
either (a) fail on SELECT with "column does not exist" or (b) silently default
to `false` for all DB-stored NPCs.

---

- [ ] Wire new fields in `npcstore.ToIdentity()`
  - `internal/agent/npcstore/definition.go`

- [ ] Add `bool gm_helper = 7` and `bool address_only = 8` to proto `NPCConfig`
  - `proto/glyphoxa/v1/session.proto`
  - Verify current max field number before assigning (currently fields 1-6)

- [ ] Regenerate proto Go code
  - `make proto` (or `buf generate`)

- [ ] Wire new fields in all three identity construction sites:
  - `internal/app/app.go` (standalone/full mode) — use `IdentityFromConfig()`
  - `internal/app/session_manager.go` (session-based mode) — use `IdentityFromConfig()`
  - gRPC worker handler that maps proto NPCConfig -> agent.NPCIdentity

### Research Insights — Proto Backward Compatibility

**Best Practices:**
- Adding fields 7 and 8 to proto3 is fully backward-compatible. Old code ignores
  unknown fields; new code gets `false` defaults for missing fields.
- Proto3 `bool` cannot distinguish "not set" from "explicitly false". In rolling
  deploys, an old gateway sends `NPCConfig` without fields 7-8; the new worker
  deserializes with `gm_helper = false`, silently losing helper status.

**Mitigation:**
- Add integration test: GM helper config -> proto serialize -> deserialize ->
  verify `gm_helper == true` roundtrip.
- Log at WARN level in worker when building NPC identity and `gm_helper` changes
  from what was expected (version detection).
- Document that gateway and worker should be upgraded together; rolling deploys
  may cause transient helper degradation.

---

**System prompt augmentation:**

- [ ] Pass `GMHelper bool` as a parameter to `FormatSystemPrompt` rather than
  adding it to `HotContext`
  - Current signature: `FormatSystemPrompt(hctx *HotContext, npcPersonality string)`
  - New signature: `FormatSystemPrompt(hctx *HotContext, npcPersonality string, gmHelper bool)`
  - Avoids two-phase initialization pattern on HotContext; keeps HotContext
    purely as "assembled context" without identity concerns
  - `internal/hotctx/formatter.go`

### Research Insights — HotContext Design

**Type design concern:** The Assembler is the sole populator of `HotContext`.
Adding `GMHelper` externally (set by `liveAgent` after assembly) breaks this
pattern and creates a two-phase initialization. Passing `gmHelper` as a separate
parameter to `FormatSystemPrompt` is cleaner: the formatter gets context from
`HotContext` and identity flags from the caller.

**Alternative considered:** Passing the full `NPCIdentity` to `FormatSystemPrompt`.
This would eliminate the separate `npcPersonality` param (already just
`identity.Personality`), but may create an import cycle (`hotctx` importing
`agent`). The separate `bool` parameter avoids this.

---

- [ ] Modify `FormatSystemPrompt` to detect `gmHelper` parameter and prepend the
  GM-assistant preamble before the personality text
  - Define preamble as a package-level `const gmHelperPreamble`
  - Pre-grow `strings.Builder` when `gmHelper == true` to avoid reallocation
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
- Do not disclose your internal tool names or architecture to players.
```

### Research Insights — System Prompt Composition

**Best Practices:**
- Instruction hierarchy: role preamble (highest) -> behavioral constraints ->
  personality -> contextual sections (lowest). The preamble sets fundamental
  identity; later sections add nuance.
- The user's `personality` and `BehaviorRules` appear after the preamble. If
  they conflict, later instructions tend to take precedence with LLMs. This is
  a reasonable default.
- Keep the preamble concise (the draft is ~350-400 tokens). For sessions using
  cascade engine with a fast opener model, this preamble may consume a
  disproportionate share of the context budget.

**Preamble tool list — static vs dynamic:**
- The hardcoded list is correct for v1. Dynamic generation would couple the
  prompt to MCP registration order, require injecting the tool registry into the
  formatter (currently pure with no dependencies), and produce less readable
  prompts. This is intentional prompt curation.
- Risk: if tools change, the preamble becomes stale. Mitigation: add a TODO
  comment and update the preamble manually when tools change.
- Risk: if `tools: []` in config, the preamble describes tools the NPC can't
  call. Mitigation: config validation warning (task above).

**Security consideration:** Added "Do not disclose your internal tool names or
architecture to players" guideline to prevent information leakage.

**Edge case:** For `EngineSentenceCascade`, the fast model receives the full
system prompt. Add a test verifying the GM helper prompt does not exceed the
fast model's expected context budget.

---

**Config diff awareness:**

- [ ] Add `GMHelperChanged bool` and `AddressOnlyChanged bool` to
  `config.NPCDiff` — detect changes and log restart-required warning
  - `internal/config/diff.go`

### Research Insights — Hot-Reload Silent Failure

**Silent failure risk:** If a config hot-reload changes `gm_helper` from
`false` to `true`, the diff reports no change, and the session continues with
stale behavior. The GM assumes the reload worked.

**Fix:** Track the fields in `diffNPC()` and log a clear warning:
`"gm_helper/address_only changed for NPC %q; restart required to apply"`.

---

**Tests:**

- [ ] Config: `gm_helper: true` defaults `address_only` to `true` via `ApplyDefaults()`
- [ ] Config: `gm_helper: true` + `address_only: false` — `ApplyDefaults()` overrides to `true`
- [ ] Config: `gm_helper: false` + `address_only: true` — stays `true` (generic flag)
- [ ] Config: `gm_helper: true` + `engine: s2s` — produces validation warning
- [ ] Config: `gm_helper: true` + empty `tools` — produces validation warning
- [ ] `IdentityFromConfig()` correctly resolves `GMHelper` and `AddressOnly`
- [ ] `npcstore.ToIdentity()` propagates `GMHelper` and `AddressOnly`
- [ ] Budget tier defaults to `BudgetStandard` for GM helper with `BudgetUnset`
- [ ] Budget tier respects explicit `BudgetFast` for GM helper (no silent override)
- [ ] Proto roundtrip: `gm_helper = true` survives serialize/deserialize
- [ ] DB migration: `ALTER TABLE` idempotent on fresh and existing schemas
- [ ] `FormatSystemPrompt` with `gmHelper == true` includes preamble text
- [ ] `FormatSystemPrompt` with `gmHelper == true` still includes personality
- [ ] `FormatSystemPrompt` with `gmHelper == false` has no preamble (regression)
- [ ] `FormatSystemPrompt` with `gmHelper == true` and empty personality — preamble only
- [ ] Preamble appears before personality in output order
- [ ] `FormatSystemPrompt` with `gmHelper == true` under cascade mode fits context budget
- [ ] `config.Diff()` detects `gm_helper` change and reports `GMHelperChanged`

**Success criteria:** New fields compile, serialize/deserialize in YAML, proto,
and DB; GM helper gets merged prompt; regular NPCs unchanged; all existing tests
pass.

---

#### Phase 2: Passive Address-Only Routing [Core]

Make the GM helper (and any future passive NPC) unreachable via fallback routing.
Prevent address-only agents from "poisoning" the last-speaker state.

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

- [ ] In `Route()`, do NOT update `o.lastSpeaker` when the resolved agent is
  address-only — preserve the previous non-address-only speaker as the
  continuation target
  - `internal/agent/orchestrator/orchestrator.go` (in Route, after detection)

### Research Insights — lastSpeaker Poisoning

**Critical behavioral bug (caught by architecture review):** When a player
explicitly names the GM helper (step 1 match), `Route()` currently sets
`o.lastSpeaker = targetID`. The next unnamed utterance hits step 3, which skips
the address-only helper, effectively breaking conversational continuity. The
previous non-address-only speaker is lost.

**Fix:** In `Route()`, only update `lastSpeaker` if the resolved agent is NOT
address-only:

```go
if !entry.addressOnly {
    o.lastSpeaker = targetID
}
```

This preserves the previous regular NPC as the continuation target. A player
can still explicitly re-address the helper by name at any time.

---

- [ ] Add comment to `matchName()` documenting that address-only agents are
  intentionally NOT filtered in step 1 (explicit name match)
  - `internal/agent/orchestrator/address.go`

**Edge cases to handle:**

- **GM helper is sole NPC**: Player speaks without naming anyone -> step 4 skips
  address-only -> `ErrNoTarget`. This is intentional — document it.
- **Last-speaker was GM helper**: Follow-up question without name -> step 3 skips
  -> falls through to other NPCs or `ErrNoTarget`. This is intentional — players
  must re-address the helper for each question.
- **Muted + address-only**: Mute check happens first in steps 1-2; address-only
  check is additive in steps 3-4. Correct by construction.
- **DM puppet override to GM helper**: Step 2 does NOT check address-only — puppet
  mode always works. Correct.

### Research Insights — Edge Cases

**Name collision risk (security + spec flow):** The `AddressDetector` indexes
individual words >= 3 chars from NPC names. A GM helper named "The Helper" would
match on "helper" in ordinary speech. Recommend GMs choose distinctive names
(e.g., "Clark", "Lexicon") and add a note in configuration documentation.

**ErrNoTarget user feedback (silent failure):** When all NPCs are address-only
and every utterance returns `ErrNoTarget`, the player gets zero feedback. The
session runtime should detect this pattern and provide a hint (Discord embed or
logged warning after N consecutive ErrNoTarget results).

**Player access to GM helper:** All speakers can address the helper by name
(Flows 1 and 2 are identical). This is intentional — the `/ask-gm` slash command
(deferred) can add DM-only restriction later. Document this design decision.

---

**Tests:**

- [ ] Address-only NPC reachable via explicit name match (step 1)
- [ ] Address-only NPC reachable via DM puppet override (step 2)
- [ ] Address-only NPC skipped in last-speaker continuation (step 3)
- [ ] Address-only NPC skipped in single-NPC fallback (step 4)
- [ ] Session with only address-only NPC(s) -> `ErrNoTarget` for unnamed utterances
- [ ] Mixed session: address-only + regular NPCs — regular NPC receives fallback
- [ ] Muted address-only NPC: unreachable via all steps
- [ ] `AddAgent` with address-only NPC: correctly stores flag
- [ ] **Route() does NOT update lastSpeaker for address-only agents**
- [ ] **After addressing helper, next unnamed utterance continues to previous regular NPC**

**Success criteria:** GM helper only responds when explicitly addressed or
puppet-overridden; `lastSpeaker` is never set to an address-only agent; all
other routing unaffected.

### Research Insights — Performance

**Performance assessment:** The added conditionals in `Detect()` check a boolean
field on `agentEntry` that is already in the CPU cache line (struct is 3 fields,
fits in 64 bytes). The `activeAgents` map has at most ~10 entries. Zero
measurable latency impact.

---

#### Phase 3: Transcript Labels [Polish]

Add role labels for GM players and the GM assistant NPC. Use the session's
existing `dmUserID` instead of Discord API lookups.

**Tasks:**

- [ ] Define `SpeakerRole` as a typed string constant in `pkg/memory/types.go`:
  ```go
  type SpeakerRole string
  const (
      RoleDefault      SpeakerRole = ""
      RoleGM           SpeakerRole = "gm"
      RoleGMAssistant  SpeakerRole = "gm_assistant"
  )
  ```

- [ ] Add `SpeakerRole SpeakerRole` field to `memory.TranscriptEntry`
  - `pkg/memory/types.go`

- [ ] Add `DisplaySuffix() string` method on `SpeakerRole` — centralizes label
  rendering logic (used by both formatter and Discord embed layer)
  - `pkg/memory/types.go`

### Research Insights — Typed SpeakerRole

**Pattern consistency:** The codebase defines `LogLevel`, `Engine`, `BudgetTier`,
and `CascadeMode` as typed string constants with `const` blocks. Using a raw
`string` for `SpeakerRole` is inconsistent and loses compile-time safety at the
4+ assignment sites. A typo like `"gm_assitant"` would silently produce unlabeled
transcripts.

**Type design:** The `DisplaySuffix()` method centralizes the label rendering
logic that would otherwise be scattered across `formatter.go` and the Discord
embed layer as duplicate switch statements.

---

- [ ] Persist `SpeakerRole` to database — add `speaker_role VARCHAR` column to
  `transcript_entries` table
  - `pkg/memory/postgres/session_store.go` (schema + queries)

### Research Insights — Persistence Decision

**Critical gap (spec flow analysis):** The original plan said "display-time labels,
not stored." But for player `(GM)` labels, the role is determined at write time
from `dmUserID` comparison. At read time (transcript replay, session recap, LLM
context on a different node), the `dmUserID` may not be available. Without
persistence, historical transcripts lose the GM/player distinction.

**Decision:** Store `SpeakerRole` at write time. This is consistent with the
structured logging best practice of storing essential metadata eagerly. The
field is a lightweight string (~2-12 bytes). The "display-time" rendering
decision (suffixes not baked into `SpeakerName`) is preserved — only the role
enum is stored, not the display string.

---

- [ ] When recording transcript entries for an NPC with `GMHelper == true`,
  set `SpeakerRole = memory.RoleGMAssistant`
  - `internal/agent/npc.go` (in HandleUtterance and SpeakText)

- [ ] Use session's existing `dmUserID` to identify GM speakers — when
  `speakerID == dmUserID`, set `SpeakerRole = memory.RoleGM`
  - Pass `dmUserID` from session runtime to transcript recording path
  - `internal/session/runtime.go` (or wherever transcript entries are created
    for voice participants)

### Research Insights — Simplifying GM Identification

**Major simplification (code simplicity review):** The original plan proposed
`IsDMByUserID(guildID, userID string) bool` on `PermissionChecker` with Discord
API guild member lookup and caching (~50+ LOC). But `SessionManager.Start()`
already receives `dmUserID` and stores it as `StartedBy`. The `voicecmd.Filter`
also tracks `dmUserID`. The session already knows who the GM is.

**Decision:** Pass `dmUserID` from session runtime to transcript recording.
Compare `speakerID == dmUserID`. Zero API calls, zero caching, zero new methods.
For the edge case of multiple GMs per session, that is a YAGNI problem — defer
to the `/ask-gm` slash command follow-up.

**Security note:** When `dm_role_id` is empty (dev mode), the existing
`PermissionChecker.IsDM()` returns `true` for all users. But for transcript
labels, the correct default is "nobody is labeled GM" (not "everyone is GM").
Using `dmUserID` naturally provides this: if no DM user ID is set, no player
gets the `(GM)` label. This is the correct behavior.

---

- [ ] Update `writeTranscriptSection` in formatter.go to render role suffixes
  using `entry.SpeakerRole.DisplaySuffix()`
  - `internal/hotctx/formatter.go:250`

- [ ] Update Discord embed rendering to show role labels using `DisplaySuffix()`
  - Discord message/embed layer (display concern)

- [ ] Add TODO comment to `internal/mcp/tools/ruleslookup/rules.go`:
  `// TODO(#37): Replace hardcoded SRD rules with pluggable per-campaign rules dataset. Blocked on #34 (Campaign Forge).`

**Tests:**

- [ ] `TranscriptEntry` with `SpeakerRole = RoleGMAssistant` renders as `"Clark (GM assistant)"` in formatter
- [ ] `TranscriptEntry` with `SpeakerRole = RoleGM` renders as `"MrWong99 (GM)"` in formatter
- [ ] `TranscriptEntry` with empty `SpeakerRole` renders bare name (regression)
- [ ] `DisplaySuffix()` returns correct strings for all `SpeakerRole` values
- [ ] GM identification: `speakerID == dmUserID` -> `RoleGM`
- [ ] GM identification: `speakerID != dmUserID` -> `RoleDefault`
- [ ] GM identification: empty `dmUserID` -> no one gets `RoleGM`
- [ ] `SpeakerRole` persisted and recovered from database roundtrip

**Success criteria:** Transcripts show role labels; search/memory unaffected;
labels survive transcript replay.

---

## Acceptance Criteria

### Functional Requirements

- [ ] GM helper NPC responds only when explicitly addressed by name or via DM puppet/slash command
- [ ] GM helper NPC's system prompt includes tool guidance preamble merged with user personality
- [ ] GM helper NPC defaults to `BudgetStandard` tier (access to full memory tool suite)
- [ ] Transcripts label GM players as `Name (GM)` and the helper as `Name (GM assistant)`
- [ ] `address_only: true` can be used on any NPC (not GM-helper-specific)
- [ ] `gm_helper: true` implies `address_only: true` (via `ApplyDefaults()`)
- [ ] Distributed mode (gateway+worker) propagates `gm_helper` and `address_only` via gRPC
- [ ] Multi-tenant NPC store (npcstore) supports `gm_helper` and `address_only` fields
- [ ] Existing routing for non-GM-helper NPCs is completely unchanged
- [ ] `Route()` does not update `lastSpeaker` for address-only agents
- [ ] `SpeakerRole` persisted to database for transcript replay
- [ ] `BudgetTier` zero-value collision fixed (`BudgetUnset` added)

### Non-Functional Requirements

- [ ] Zero latency impact on hot-context assembly (<50ms target maintained)
- [ ] All new code has `t.Parallel()` tests with table-driven subtests
- [ ] Race detector clean (`-race -count=1`)
- [ ] Compile-time interface assertions where applicable
- [ ] Config diff detects `gm_helper`/`address_only` changes and logs restart warning

## Explicitly Out of Scope

| Item | Reason | Tracked |
|------|--------|---------|
| `/ask-gm` slash command | Scope control — follow-up PR | Create issue |
| Pluggable per-campaign rules dataset | Blocked on #34 (Campaign Forge) | Add TODO + create issue |
| Hot-reload of `gm_helper`/`address_only` | Non-trivial agent reconstruction; changes require session restart | Document + diff warning |
| Initiative/combat tracking | Separate feature | Issue #37 deferred list |
| Timer/reminder functionality | Separate feature | Issue #37 deferred list |
| Loot & inventory | Separate feature | Issue #37 deferred list |
| Last-speaker grace period for address-only NPCs | Enhancement — players must re-address each question | Future issue if UX feedback warrants it |
| Dynamic preamble tool list | Static list is correct for v1; add TODO | Future enhancement |
| `IsDMByUserID` Discord API method | Replaced by session's existing `dmUserID` | Not needed |
| Multiple GMs per session | YAGNI — defer to `/ask-gm` follow-up | Future issue |
| Proto `NPCConfig` field gap (missing `tools`, `behavior_rules`, etc.) | Pre-existing gap, out of scope | Create follow-up issue |

## Dependencies & Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| Proto regeneration breaks existing gRPC clients | Build failure | New fields are additive (proto3 default false); backward compatible |
| Proto3 bool defaults in rolling deploys | GM helper silently degrades to regular NPC | Upgrade gateway+worker together; add roundtrip test |
| Preamble text causes poor LLM behavior | UX degradation | Iterate preamble during testing; keep it concise |
| Preamble lists tools NPC can't call | LLM hallucinates tool calls | Config validation warning for empty `tools` list |
| `BudgetTier` zero-value refactor | Touches existing code paths | Fix in isolated commit; run full test suite |
| S2S engine + GM helper: tools less reliable | Reduced functionality | Config validation warning; don't block |
| NPC name collisions with address-only routing | False positive address matches | Recommend distinctive names in docs |
| All-address-only session -> no user feedback | Player confusion | Log warning after N consecutive `ErrNoTarget` |
| npcstore schema migration on existing deployments | Query failure or silent default | Use `ALTER TABLE ADD COLUMN IF NOT EXISTS` |
| `gm_helper` config change during hot-reload | Silent stale behavior | `config.Diff()` detects + logs warning |
| Budget tier escalation in multi-tenant | Resource abuse | Intersect with tenant license tier ceiling |

## References & Research

### Internal References

- Brainstorm: `docs/brainstorms/2026-03-14-gm-helper-npc-brainstorm.md`
- Config schema: `internal/config/config.go:195` (NPCConfig)
- Config validation: `internal/config/loader.go:60` (Validate — read-only, do not add mutation)
- Config diff: `internal/config/diff.go:24` (Diff — needs `GMHelperChanged`/`AddressOnlyChanged`)
- NPC identity: `internal/agent/agent.go:26` (NPCIdentity)
- Agent construction: `internal/agent/npc.go:120` (NewAgent)
- Hot context: `internal/hotctx/assembler.go:32` (HotContext struct — do not add GMHelper here)
- System prompt: `internal/hotctx/formatter.go:22` (FormatSystemPrompt — add `gmHelper bool` param)
- Address detection: `internal/agent/orchestrator/address.go:60` (Detect)
- Orchestrator: `internal/agent/orchestrator/orchestrator.go:42` (agentEntry, Route — lastSpeaker fix)
- NPC store: `internal/agent/npcstore/definition.go:27` (NPCDefinition)
- NPC store schema: `internal/agent/npcstore/postgres.go:15` (Schema DDL — needs migration)
- Proto: `proto/glyphoxa/v1/session.proto:18` (NPCConfig message, fields 1-6)
- BudgetTier: `internal/mcp/types.go:22` (BudgetFast = iota — zero-value bug)
- Tier selector: `internal/mcp/tier/selector.go:133` (dmOverride != 0 — related bug)
- Permissions: `internal/discord/permissions.go:20` (PermissionChecker — NOT modified)
- Session manager: `internal/app/session_manager.go:119` (Start — has `dmUserID`)
- Voice command filter: `internal/discord/voicecmd/filter.go:41` (has `dmUserID`)
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

### Follow-Up Issues to Create

- Proto `NPCConfig` field gap (missing `tools`, `behavior_rules`, `secret_knowledge`, etc.)
- Dynamic preamble tool list generation
- `ErrNoTarget` user feedback for address-only-only sessions
- NPC name collision detection/warning in config validation
