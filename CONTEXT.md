# Glyphoxa

A multi-tenant TTRPG voice-and-knowledge platform: AI agents (Butler and Character NPCs) join a Discord voice session, voice campaign NPCs and assist the GM via slash commands and tools, persisting transcripts and a per-campaign knowledge graph.

## Tenancy & People

| Term | Definition | Aliases to avoid |
|------|------------|------------------|
| **Tenant** | The top-level isolation boundary; owns Campaigns, Members, linked Guilds, and Provider Configs. | Organization, Org, Workspace |
| **Member** | A user authenticated via Discord OAuth and bound to a Tenant by a Member Role. | Tenant user, Account holder |
| **Member Role** | A Member's permissions within their Tenant: `owner`, `admin`, `gm`. | Permission, Tier, Role (unqualified) |
| **GM** | A Member with `gm` Member Role; runs and owns Campaigns within their Tenant. | Game Master (spelled out — "GM" is canonical), Dungeon Master |
| **Player** | A human at the table whose Character is bound to a Discord User ID; not a Tenant Member. | Participant, Attendee, Guest |
| **Linked Player** | A Player who has signed in via Discord OAuth, linking their `linked_user_id` on a Character; gains player-tier web app access scoped to their Characters. | Registered player, Account-linked player |
| **Discord User** | An identity in Discord, identified by Discord snowflake; the universal handle for Players regardless of account-linking status. | User, Account |
| **Character** | A Player's player-character (PC) within a Campaign; carries `discord_user_id` (mandatory) and `linked_user_id` (nullable). | PC (use only in user-facing copy) |

## Campaign & World

| Term | Definition | Aliases to avoid |
|------|------------|------------------|
| **Campaign** | A persistent TTRPG game owned by a Tenant and GM'd by one Member; contains Characters, NPCs, Knowledge Graph, Transcripts, and per-campaign config. | Game, Project, World |
| **Active Campaign** | The Campaign a GM is currently operating on; resolved automatically from a Voice Session binding when present, otherwise from the GM's profile. | Selected campaign, Current campaign |
| **NPC** | A non-player character in the Campaign world, modeled as a Knowledge Graph Node and optionally as a Character NPC Agent. | Character (overloaded — Character is reserved for PCs) |
| **System** | The TTRPG ruleset of a Campaign (D&D 5e, Pathfinder 2e, Call of Cthulhu, etc.); consumed by Tools that need rules context. | Ruleset, Game system |

## Knowledge Graph

| Term | Definition | Aliases to avoid |
|------|------------|------------------|
| **Knowledge Graph (KG)** | The Campaign's structured world model — typed Nodes connected by typed Edges. | Wiki, Graph, World model |
| **Node** | A typed KG entity within one Campaign: `Character`, `NPC`, `Location`, `Faction`, `Item`, `PlotThread`, `Note`. | Entity, Page, Record |
| **Edge** | A typed directional relationship between two Nodes in the same Campaign (e.g. `resides_in`, `member_of`, `knows`). | Link, Relationship, Connection |

## Agents

| Term | Definition | Aliases to avoid |
|------|------------|------------------|
| **Agent** | An AI-controlled persona (Butler or Character NPC) with a Persona, Voice, LLM config, Tool Grants, and addressability rules. | Bot (reserved for the Discord identity), AI |
| **Agent Role** | An Agent's archetype: `butler` or `character`. Distinct from Member Role. | Type, Kind, Role (unqualified) |
| **Butler** | The Tenant-scoped Agent (`agent_role=butler`) that assists the GM via slash commands, in-voice address, and Tool calls; exactly one per Tenant. | Assistant, Helper, GM agent |
| **Character NPC** | A Campaign-scoped Agent (`agent_role=character`) that voices a single in-world persona during a Voice Session. | NPC agent, In-world agent |
| **Persona** | Markdown description of an Agent's personality, backstory, and speech style; injected into LLM prompts. | Personality, Character sheet, Profile |
| **Voice** | The TTS Provider + voice-id configuration that produces an Agent's audio output. | Voice profile, TTS config |
| **Tool Grant** | An explicit permission for an Agent to invoke a named MCP Tool, with optional per-grant configuration. | Capability, Permission |
| **Address Detection** | The router logic deciding which Agent (if any) is the target of a given Transcript utterance. | Routing, Targeting |

## Voice & Discord

| Term | Definition | Aliases to avoid |
|------|------------|------------------|
| **Audio Frame** | A fixed-duration window of single-channel signed-16-bit PCM samples that crosses voice pipeline stages (VAD, STT, …); modelled by `pkg/voice/audio.Frame` whose constructor enforces `len(samples) == SampleRate × FrameMs / 1000`. | Chunk, Packet (Packet is RTP/Opus), PCM (unqualified) |
| **Bot** | The single Discord bot identity (one token) shared by the entire Glyphoxa deployment regardless of Tenant. | App, Application, Bot account |
| **Guild** | A Discord server, identified by `guild_id`; a Tenant may have many Guilds linked. | Server, Discord server |
| **Voice Session** | The Bot's presence in one Discord voice channel, bound to a (Guild, Campaign, GM) tuple at start and hosted by exactly one Voice Instance. | Session (alone), Call, Live session |
| **DAVE** | Discord's Audio & Video End-to-end Encryption protocol, mandatory since 2026-03-01; built on MLS. | E2EE, Encryption |
| **Transcript** | The persisted text record of a Voice Session, with speaker attribution by Discord User. | Log, Recording (recording is audio) |
| **Slash Command** | A Discord interaction registered by the Bot; handled by the Butler when reasoning is required, otherwise directly by the Bot binary. | Command, Interaction |

## Providers & Pipeline

| Term | Definition | Aliases to avoid |
|------|------------|------------------|
| **Provider** | An external service implementing one Component (e.g. Anthropic for LLM, Deepgram for STT). | Vendor, Backend, Service |
| **Component** | A Provider category: `llm`, `stt`, `tts`, `embeddings`, `s2s`. | Capability, Type |
| **Provider Config** | A Tenant-scoped, encrypted record binding a Component to a Provider with credentials and model/voice selection. | Credentials, Key, API key |
| **BYOK** | Bring-Your-Own-Keys — the v1.0 Provider Config source where the Tenant supplies their own credentials. | Self-supplied, Customer keys |
| **MCP Tool** | A named callable function (built-in or external) invoked by an Agent via a Tool Grant; speaks the Model Context Protocol. | Function, Action, Plugin |
| **Hot Context** | The recent-Transcript + KG-facts + Persona bundle assembled per-utterance to prime an Agent's LLM call (target <50ms). | Context, Prompt context |

## Process & Deployment

| Term | Definition | Aliases to avoid |
|------|------------|------------------|
| **Mode** | The role a binary process plays: `all` (default), `web`, or `voice`. | Profile, Role (Member/Agent Role only) |
| **Voice Instance** | A process running in `voice` Mode that claims and hosts Voice Sessions via the `voice_sessions` Postgres table. | Worker (v1 term, deprecated in v2) |
| **Web Instance** | A process running in `web` Mode that serves the web app and admin API. | Server, Web server |

## Relationships

- A **Tenant** has many **Members** with a **Member Role**, many **Campaigns**, many linked **Guilds**, and one or more **Provider Configs** per **Component**.
- A **Campaign** has one **GM** (a Member of its Tenant), many **Characters**, one **Knowledge Graph**, exactly one **Butler**,many **Character NPCs**, and many **Transcripts**.
- A **Character** belongs to exactly one **Campaign** and is bound to exactly one **Discord User**; the Discord User may also be a **Linked Player**.
- A **Character NPC** belongs to exactly one **Campaign**.
- A **Voice Session** binds (**Guild**, voice channel, **GM**, **Campaign**) and is hosted by exactly one **Voice Instance**.
- A **Transcript** belongs to exactly one **Voice Session** and transitively to one **Campaign**.
- An **Agent**'s **Tool Grants** reference **MCP Tools**; tool invocations resolve **Active Campaign** for campaign-scoped Tools.

## Example dialogue

> **Dev:** "When the **GM** says 'Glyphoxa, roll a d20' inside a **Voice Session**, what handles the utterance?"

> **Domain expert:** "**Address Detection** routes it to the **Butler** because the **GM** spoke without naming a target. The **Butler** invokes the `dice` **MCP Tool** via its **Tool Grant**."

> **Dev:** "And if the **GM** runs `/roll 1d20` as a **Slash Command** outside a **Voice Session**?"

> **Domain expert:** "Same **Butler** — there is exactly one per **Campaign**. **Active Campaign** is resolved from the **GM**'s profile rather than a **Voice Session** binding. The **Bot** acks the interaction; the **Butler** runs the **MCP Tool**; the **Bot** posts the follow-up."

> **Dev:** "When 'Bart the innkeeper' speaks, what's that?"

> **Domain expert:** "Bart is a **Character NPC** — an **Agent** with `agent_role=character`, scoped to one **Campaign**. He has a **Persona** and a **Voice**, no **Tool Grants** by default. He's *also* an **NPC** **Node** in that **Campaign**'s **Knowledge Graph** — but those are separate records."

> **Dev:** "And a **Player** without a linked account — they can still play?"

> **Domain expert:** "Yes. Their **Character** carries their **Discord User** ID; that's enough for **Transcript** attribution and **Address Detection**. If they later sign in via Discord OAuth, they become a **Linked Player** and gain web app access to their **Characters** — without becoming a **Member** of the **Tenant**."

## Flagged ambiguities

- **"Session"** is overloaded — TTRPG culture uses it for "one night of play" and software uses it for browser/auth sessions. Canonical here: **Voice Session** for the Bot's presence in a voice channel. Don't use "session" alone in domain prose; never use it for auth ("login", "auth session" if needed).
- **"NPC"** is used as both a **Knowledge Graph Node** type AND as a label for **Character NPC** **Agents**. These are distinct: one is a wiki entry, the other is an AI persona that speaks. The Node may exist without an Agent (wiki-only NPCs are normal); when both exist, they describe the same in-world character but are separate records that may drift.
- **"Character"** in plain TTRPG usage means *any* in-world person (PC or NPC). In our schema, **Character** is reserved for Player Characters only; **NPC** for non-player characters. Don't use "Character" generically.
- **"User"** is unsafe unqualified — it could mean **Discord User**, **Member**, **Linked Player**, or the request principal. Always qualify.
- **"Bot"** refers to the *single* Glyphoxa Discord bot identity (one token, shared across all Tenants). Don't say "the Tenant's bot" — say "the Bot acting on behalf of the Tenant."
- **"Worker"** is a v1 term for what v2 calls a **Voice Instance**. Do not use "worker" in v2 — it implies the gateway/worker split that was explicitly removed.
- **"Role"** is overloaded between **Member Role** (`owner`/`admin`/`gm`) and **Agent Role** (`butler`/`character`). Always qualify.
- **"Player role"** is *not* a Member Role — Players are not Tenant Members. Don't add `player` to the Member Role enum.
