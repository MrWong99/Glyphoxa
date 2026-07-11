# Single Agent table; Butler auto-created per Campaign

A single `agents` table is polymorphic via an `agent_role` enum (`butler` | `character`), so one unified orchestrator and one address-detection code path handle both. A Postgres partial unique index enforces exactly one Butler per Campaign.

Each Campaign auto-creates its own Butler on creation with hardcoded defaults (name "Glyphoxa", default Tool Grants — see amendment below); there is no `tenant_butler_defaults` layer. The GM edits the auto-created Butler post-creation.

Slash command routing resolves Active Campaign in this order:

1. Active Voice Session in this Guild → that Campaign,
2. else `active_campaign_id` on the GM's user profile,
3. else fail with a `/use campaign:<name>` hint.

**Why:** the Butler is a Campaign-scoped concept (per-campaign tools, per-campaign voice). A tenant-default layer would have to be merged in at runtime, complicating routing for no real win. Polymorphism on the Agent table beats two separate tables that would share most columns.

## Amendment (Butler is Address-Only and not voiced in v1, 2026-07-09)

The auto-created Butler is **Address-Only** (ADR-0024) and, in v1, **not part of the voiced Cast**: it never enters the address Matcher or the Cast, because the live session roster is built only from Character NPCs (`agent_role='character'` — see `CharacterAgents` and `loadSeededNPCs`). In v1 it is reached **only by slash command**: no in-voice path resolves to the Butler, since the Matcher roster is hardcoded to `AgentRoleCharacter` and the #280 GM-gated voice-address remains dormant. (If a future version voices in-address Butler replies, wire them through the same voiced-roster path — see the closing note.)

Two consequences follow, and both are enforced:

- **Not a mute target.** The `/glyphoxa mute` / `muteall` surface and the RPC mute path narrow the roster to the voiced Character NPCs (`voicedAgents` in `internal/session`, `voiced` in `internal/presence`). Muting the Butler is refused (`ErrAgentNotInCampaign` → `CodeNotFound`) and never records a phantom id in the session mute set, so `GetSession.muted_agent_ids` never lists it. (Before this was enforced, muting the Butler "succeeded" and showed as muted while silencing nothing.) The web Voice panel (`web/src/screens/session/VoicePanel.tsx`) mirrors this: it renders the Butler row with no mute toggle and a neutral "Butler · address-only" state, and computes its voicing count / mute-all flip over the voiceable NPCs only — so it never issues (nor has to swallow) a Butler mute, and the always-unmuted Butler can't pin the mute-all button open.
- **Only partially editable.** `UpdateAgent` never changes `agent_role` and force-keeps a Butler's `address_only = true`. Name, title, persona, voice, aliases, and provider configs are mutable; **role and Address-Only are pinned**. The Butler is also undeletable (`ErrButlerUndeletable`).

If a future version voices the Butler, wire it through the same voiced-roster path consistently (Cast + Matcher + mute surface) rather than special-casing it.

## Amendment (Butler joins the voiced Cast, 2026-07-10, #299)

The 2026-07-09 "not part of the voiced Cast" amendment is **superseded**: the
Butler now enters the live Voice Session roster (Epic 7, #256/#299). `loadCampaignRoster`
appends the auto-created Butler **last** (the primary Character stays first), so it
joins the address Matcher and the Cast as a regular candidate — but keeps its
`AddressOnly` rule, so ambient heuristics (continuation, single-NPC fallback)
never route to it; only an explicit name address reaches it. Its GM-only
voice-address rule is enforced **matcher-side** (see ADR-0024's #256 amendment).

Two things from the prior amendment still hold and are unchanged:

- **Still not a mute target.** The mute surfaces (`voicedAgents` in
  `internal/session`, `voiced` in `internal/presence`, the web Voice panel) remain
  narrowed to the voiced **Character** NPCs, so muting the Butler is still refused
  (`ErrAgentNotInCampaign`) and a mute-all leaves it unmuted. The Butler is
  Address-Only, not mutable roster state.
- **Role and Address-Only stay pinned** by `UpdateAgent`, and the Butler stays
  undeletable.

Answer modality (#297 decision 2): short answers are spoken through its Voice;
long ones — and a voiceless Butler's — post as text to the voice channel chat via
a `TextSink`. Its default grant set grows to the read-only knowledge Tools
(`transcript_search`, `kg_query`) alongside `dice` (migration 00025), seeded by
the same auto-Butler trigger invariant.

## Amendment (Q14, 2026-05-28)

The original default grant set `dice` + `transcript_search` + `rules_lookup` named tools that do not yet exist. Per Q14 we ship **only the `dice` Tool** in v1.0 (PoC); further tools are added when their building blocks land (transcript search needs the ADR-0011 pgvector path wired into a Tool; rules lookup needs an SRD corpus). The Butler's **as-built default grant is `dice` only**; `transcript_search` and `rules_lookup` join the default set as those tools are built. See the Q14 ADR for the Tool framework.
