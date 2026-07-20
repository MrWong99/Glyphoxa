# Glyphoxa

A multi-tenant TTRPG voice-and-knowledge platform: AI agents (Butler and Character NPCs) join a Discord voice session, voice campaign NPCs and assist the GM via slash commands and tools, persisting transcripts and a per-campaign knowledge graph.

## Tenancy & People

| Term | Definition | Aliases to avoid |
|------|------------|------------------|
| **Tenant** | The top-level isolation boundary; owns Campaigns, Members, linked Guilds, and Provider Configs. | Organization, Org, Workspace |
| **Member** | A user authenticated via Discord OAuth and bound to a Tenant by a Member Role. | Tenant user, Account holder |
| **Member Role** | A Member's permissions within their Tenant: `owner`, `admin`, `gm`. | Permission, Tier, Role (unqualified) |
| **GM** | A Member with `gm` Member Role; runs and owns Campaigns within their Tenant. Until `tenant_members` lands, GM identity resolves as the Tenant-bound operator (`tenant.operator_user_id`) union the env allowlist (ADR-0055 interim). | Game Master (spelled out — "GM" is canonical), Dungeon Master |
| **Operator** | The allowlisted Discord User a self-host deployment grants web-tier access to; bound to exactly one Tenant (claims the seeded one or gets a fresh one). In the v1.0 single-operator web tier the Operator fills every Member Role at once. | Admin, Owner (unqualified), First user |
| **Player** | A human at the table whose Character is bound to a Discord User ID; not a Tenant Member. | Participant, Attendee, Guest |
| **Linked Player** | A Player who has signed in via Discord OAuth, linking their `linked_user_id` on a Character; gains player-tier web app access scoped to their Characters, at a Player Access Level (ADR-0056). | Registered player, Account-linked player |
| **Player Invitation** | A GM-minted, expiring, optionally single-use link (`/join/<token>`) that lets a Player authenticate via Discord OAuth and become a Linked Player at a granted Player Access Level (ADR-0056). Never call it an "invite" unqualified — that collides with Discord guild invites (ADR-0047). | Invite (unqualified), Share link, Membership invitation |
| **Player Access Level** | The per-link grant on a Player Invitation deciding what a Linked Player may see: `own-character`, `campaign-highlights`, or `campaign-transcripts` (the last gated by the Campaign's share toggle). Not a Member Role. | Access level (unqualified), Role, Permission tier |
| **Discord User** | An identity in Discord, identified by Discord snowflake; the universal handle for Players regardless of account-linking status. | User, Account |
| **Character** | A Player's player-character (PC) within a Campaign; carries `discord_user_id` (mandatory) and `linked_user_id` (nullable). | PC (use only in user-facing copy) |

## Campaign & World

| Term | Definition | Aliases to avoid |
|------|------------|------------------|
| **Campaign** | A persistent TTRPG game owned by a Tenant and GM'd by one Member; contains Characters, NPCs, Knowledge Graph, Transcripts, and per-campaign config. | Game, Project, World |
| **Active Campaign** | The Campaign a GM is currently operating on; resolved automatically from a Voice Session binding when present, otherwise from the GM's profile. | Selected campaign, Current campaign |
| **NPC** | A non-player character in the Campaign world, modeled as a Knowledge Graph Node and optionally as a Character NPC Agent. | Character (overloaded — Character is reserved for PCs) |
| **System** | The TTRPG ruleset of a Campaign (D&D 5e, Pathfinder 2e, Call of Cthulhu, etc.); consumed by Tools that need rules context. | Ruleset, Game system |
| **Campaign Language** | The natural language a Campaign is played in; selects the phonetic scheme used by Address Detection's name matching and the language hint for STT/TTS. | Locale, Lang |
| **Campaign Bundle** | The versioned gzipped-JSON export of a Campaign's setup (Agents, Tool Grants, KG, Characters; Transcript history flag-gated) with secrets always excluded; import mints fresh IDs (ADR-0053). | Export (unqualified), Backup, Archive (reserved for archived Campaigns) |
| **Recap** | A Butler-Persona-flavored summary of a Voice Session's Transcript Lines, generated on demand and never persisted; delivered voiced, as public text, or GM-only per request (#271). | Summary, Digest |

## Knowledge Graph

| Term | Definition | Aliases to avoid |
|------|------------|------------------|
| **Knowledge Graph (KG)** | The Campaign's structured world model — typed Nodes connected by typed Edges. | Wiki, Graph, World model |
| **Node** | A typed KG entity within one Campaign: `Character`, `NPC`, `Location`, `Faction`, `Item`, `PlotThread`, `Note`. | Entity, Page, Record |
| **Edge** | A typed directional relationship between two Nodes in the same Campaign (e.g. `resides_in`, `member_of`, `knows`). | Link, Relationship, Connection |
| **Knowledge Proposal** | An Agent-authored KG write (fact, Node, or Edge) awaiting GM review; nothing enters the KG without approval (ADR-0052). Character NPCs may propose only on their own linked Node; the Butler campaign-wide. | Suggestion, Pending fact, Draft |

## Agents

| Term | Definition | Aliases to avoid |
|------|------------|------------------|
| **Agent** | An AI-controlled persona (Butler or Character NPC) with a Persona, Voice, LLM config, Tool Grants, and addressability rules. | Bot (reserved for the Discord identity), AI |
| **Agent Role** | An Agent's archetype: `butler` or `character`. Distinct from Member Role. | Type, Kind, Role (unqualified) |
| **Butler** | The Campaign-scoped Agent (`agent_role=butler`) that assists the GM via slash commands and Tool calls; exactly one per Campaign, auto-created and undeletable (ADR-0009). On the live roster and addressable in-voice by explicit name/alias, gated to the GM by the matcher-side GM gate (#299, ADR-0024 amendment). Remains **Address-Only** — never reachable via last-speaker continuation or sole-NPC fallback, never recorded as last-addressed — and is not a mute target. Only partially editable: name/title/persona/voice/aliases mutable; `agent_role` and Address-Only pinned. | Assistant, Helper, GM agent |
| **Character NPC** | A Campaign-scoped Agent (`agent_role=character`) that voices a single in-world persona during a Voice Session. | NPC agent, In-world agent |
| **Persona** | Markdown description of an Agent's personality, backstory, and speech style; injected into LLM prompts. | Personality, Character sheet, Profile |
| **Voice** | The TTS Provider + voice-id configuration that produces an Agent's audio output. | Voice profile, TTS config |
| **Tool Grant** | An explicit permission for an Agent to invoke a named Tool, with optional per-grant configuration that may **narrow the Tool's authority** for that Agent. The same Tool granted to two Agents can carry different scope (e.g. an NPC granted `remember_knowledge` scoped to its own facts vs the Butler granted it campaign-wide). The scope is enforced in the Tool handler, never by the LLM. | Capability, Permission |
| **Address Detection** | The deterministic chain deciding which Agent(s), if any, a Transcript utterance targets: fuzzy name/alias match → last-speaker → single-NPC fallback → no target. Returns a set; multiple targets trigger an Ensemble Turn. | Routing, Targeting |
| **Address-Only** | An Agent reachable only by explicit name/alias, excluded from last-speaker and single-NPC fallback. The Butler is Address-Only by default; Character NPCs are not. | Wake-word-only, Explicit-only |
| **Vocative Flag** | A ranking signal in Address Detection: a matched name is *flagged* when it is punctuation-bracketed as a direct address (a marker rune on each side, none inside — the two-sided rule), distinguishing an addressee ("**Gesa,** …") from a same-scoring topic mention ("was hat **Bart** …"). When one utterance names two Agents, a flagged name outranks an unflagged one before name similarity decides (ADR-0024, #400/#413). | Vocative, Addressee bracket |
| **Ensemble Turn** | The turn taken when one utterance addresses two or more Agents: they generate in parallel, the fastest (the Lead) speaks, and at most one other Agent reacts to it. | Group response, Multi-response |
| **Lead** | In an Ensemble Turn, the Agent whose response text is ready first; it takes the floor and speaks before the Reaction. | Primary speaker, Winner |
| **Cross-talk** | The Lead's delivered text fed to another addressed Agent so it can react. Distinct from Barge-in: the receiving Agent has not spoken yet, so nothing is interrupted. | Barge-in (reserved), Interruption |
| **Reaction** | A follow-up turn an Agent generates in response to the Lead's Cross-talk in an Ensemble Turn; may be a short affirmation, a longer disagreement, or silence. | Follow-up, Reply |

## Voice & Discord

| Term | Definition | Aliases to avoid |
|------|------------|------------------|
| **Audio Frame** | A fixed-duration window of single-channel signed-16-bit PCM samples that crosses voice pipeline stages (VAD, STT, …); modelled by `pkg/voice/audio.Frame` whose constructor enforces `len(samples) == SampleRate × FrameMs / 1000`. | Chunk, Packet (Packet is RTP/Opus), PCM (unqualified) |
| **Bot** | The Discord bot identity acting on behalf of a Tenant — no longer necessarily one token: a Tenant is served either by the deployment's central Bot (one token, shared across Tenants on that mode) or by its own BYOK Bot (ADR-0057). Say "the Tenant's resolved Bot", not "the Bot" unqualified. | App, Application, Bot account |
| **Bot-token mode** | How a Tenant's Bot is sourced: `central` (shares the deployment-operated token with other Tenants) or `byok` (the Tenant supplies its own token). Resolved per Tenant by the Per-tenant client registry (ADR-0057). | Token mode, Bot source |
| **Per-tenant client registry** | The presence layer's map of standing Discord gateway clients, keyed by a Tenant's resolved bot token: central-token Tenants share one client, a BYOK Tenant gets its own. Replaces the pre-0057 single global client (ADR-0057). | Client pool, Connection registry |
| **Session-scoped attribution** | The rule that every voice-event-bus event carries its `SessionID`; consumers resolve session context from the event itself, never from a process-global snapshot. Decided by ADR-0057; implemented by #487. | Global snapshot (the superseded pattern) |
| **Guild** | A Discord server, identified by `guild_id`; a Tenant may have many Guilds linked. | Server, Discord server |
| **Voice Session** | The Tenant's resolved Bot's presence in one Discord voice channel, bound to a (Guild, Campaign, GM) tuple at start and hosted by exactly one Voice Instance. | Session (alone), Call, Live session |
| **DAVE** | Discord's Audio & Video End-to-end Encryption protocol, mandatory since 2026-03-01; built on MLS. | E2EE, Encryption |
| **Transcript** | The persisted text record of a Voice Session, with speaker attribution by Discord User. | Log, Recording (recording is audio) |
| **Partial Transcript** | The mutable interim text of an in-progress utterance streamed by STT; may change until the utterance is committed. Only the committed text becomes an STT final that Address Detection and the Transcript consume. | Interim result, Draft, Live transcript |
| **Transcript Line** | One rendered line of a Transcript — a single human utterance or a coalesced Agent reply — persisted per Voice Session at the LINE grain (`transcript_line`, ADR-0040) for the Session screen's live feed and replay-on-reload. Distinct from a Transcript Chunk (the 3–6-utterance retrieval/embedding grain, ADR-0011): they are separate records of the same speech. A Voice Session's `line_count` is the count of its Transcript Lines. | Transcript Chunk (the retrieval grain), Utterance (a Line may coalesce several) |
| **Slash Command** | A Discord interaction registered by the Bot; handled by the Butler when reasoning is required, otherwise directly by the Bot binary. | Command, Interaction |
| **Turn** | One Agent reply as a floor-holding unit of speech: opened by a routing decision (or a GM /say / clip replay), correlated end-to-end by its TurnID, and terminated by exactly one outcome (delivered, barged, coalesced, muted, text-delivered, error). Its deliver-then-commit lifecycle (ADR-0012: commit exactly the audio the room heard) is owned by the orchestrator's turn module (#444) — sentence dispatch, the delivered/not-delivered/cut classification, and the terminal reason all live there. | Reply (the sentence type), Utterance (human speech) |
| **Barge-in** | A human participant reclaiming the floor while an Agent is speaking: per-participant voiced speech crossing the Barge-in Confirm Window cancels the Agent's whole turn (the entire Ensemble Turn, if one is running). Distinct from Cross-talk. | Interruption, Cut-in |
| **Barge-in Confirm Window** | The duration of continuous voiced speech from one participant required before a Barge-in cancels the Agent (default ~250ms). A per-Agent tunable: longer = harder to interrupt. Voiced bursts shorter than the window are Soft-overlap. | Barge threshold, Interrupt delay |
| **Soft-overlap** | A participant's voiced burst shorter than the Barge-in Confirm Window (a backchannel: "mhm", "yeah", a cough). Does not cancel the Agent, but is still transcribed and committed normally. | Backchannel (use in prose only), Filler |
| **Speaker Lane** | The per-participant VAD/segmentation path (one per active speaker, keyed by Discord snowflake) that attributes each utterance's events via `SpeakerID` (ADR-0050). | Lane (ok once qualified), Channel, Track |
| **Rollover Tape** | The consent-gated, in-memory 120s rolling buffer of consented Speaker Lanes plus Agent speech during a Voice Session; discarded wholesale at session end (ADR-0051). Default OFF. | Recording (implies persistence), Ring buffer (implementation term) |
| **Highlight Candidate** | A detector-flagged moment clipped from the Rollover Tape; ephemeral — held only until the GM's session-end review (7-day safety purge), then promoted or deleted (#305). | Moment, Clip (unqualified) |
| **Highlight** | A GM-promoted Highlight Candidate stored durably with its excerpt and optional generated media; leaves the instance only by explicit GM action (ADR-0051, #310). | Clip (unqualified), Best-of |

## Providers & Pipeline

| Term | Definition | Aliases to avoid |
|------|------------|------------------|
| **Provider** | An external service implementing one Component (e.g. Anthropic for LLM, Deepgram for STT). | Vendor, Backend, Service |
| **Component** | A Provider category: `llm`, `stt`, `tts`, `embeddings`, `s2s`. | Capability, Type |
| **Provider Config** | A Tenant-scoped, encrypted record binding a Component to a Provider with credentials and model/voice selection. | Credentials, Key, API key |
| **BYOK** | Bring-Your-Own-Keys — the v1.0 Provider Config source where the Tenant supplies their own credentials. | Self-supplied, Customer keys |
| **Tool** | A named callable function invoked by an Agent via a Tool Grant. Its backing is either **built-in** (runs in-process, lowest latency) or an **MCP Server** (out-of-process). The Agent only ever sees the uniform Tool interface; the backing is an implementation detail. | MCP Tool (the protocol is one backing, not the category), Function, Action, Plugin |
| **MCP Server** | An out-of-process backing that exposes one or more Tools over the Model Context Protocol (stdio or streamable-HTTP). A Tool source differentiated from built-ins by running in a separate process; pays serialization/IPC cost in exchange for isolation and third-party extensibility. | External tool, Plugin, MCP Tool |
| **Hot Context** | The recent-Transcript + KG-facts + Persona bundle assembled per-utterance to prime an Agent's LLM call (target <50ms). | Context, Prompt context |

## Billing & SaaS

| Term | Definition | Aliases to avoid |
|------|------------|------------------|
| **Plan** | A subscription tier in the deployment's catalog: slug (stable handle), monthly price, Key Source, optional included-usage allowance, and an extensible limits bag. Synced as data from the operator's declarative catalog (ADR-0054); archived, never deleted. | Tier (reserved for prose only), Package, SKU |
| **Plan Catalog** | The operator's declarative JSON (or Helm values) list of Plans, synced into the DB via `glyphoxa billing plans-sync` / the chart's plans hook Job. | Pricing table, Config |
| **Subscription** | A Tenant's binding to a Plan, snapshotting the Plan's slug and monthly price at bind time; at most one active per Tenant, with ended rows as history. The revenue record. | Membership, License |
| **Key Source** | Where a Plan's provider keys come from: `byok` (the Tenant's own keys, ADR-0004) or `platform` (the deployment's env keys, included in the price). | Key mode, Pooling |
| **Platform Keys** | The deployment-operated provider credentials (env `*_API_KEY`) serving `platform`-Plan Tenants via the existing env-fallback path (ADR-0039/0054). | Pooled keys, Shared keys, Operator keys (prose ok) |
| **Usage Ledger** | The durable per-Tenant record of metered provider usage: daily buckets per (component, provider, model) with quantities and a priced ESTIMATE, flushed at Voice Session end (`usage_ledger`). The cost side of billing; never a gate, never billing truth. The `open`-mode monthly allowance gate (ADR-0055) is a separate spend-meter-style mechanism that READS it. | Billing log, Meter (the in-memory ADR-0046 accumulator), Invoice |

## Process & Deployment

| Term | Definition | Aliases to avoid |
|------|------------|------------------|
| **Mode** | The role a binary process plays: `all` (default), `web`, or `voice`. | Profile, Role (Member/Agent Role only) |
| **Admission Mode** | How a deployment admits web identities: `allowlist` (the ADR-0041 operator allowlist; default) or `open` (self-signup founds a fresh Tenant, ADR-0055). A Player Invitation may additionally admit where enabled (ADR-0056). Distinct from Mode (the process role). | Signup mode, Registration, Access mode |
| **Suspension** | The `open`-Admission-Mode revocation mechanism (ADR-0055): `users.suspended_at`, enforced by the per-request session re-check on the next request. Non-destructive — sessions survive dormant and unsuspending restores them. Distinct from the allowlist boot sweep, which runs only in `allowlist` Admission Mode. | Ban, Deactivation, Lockout |
| **Voice Instance** | A process running in `voice` Mode that claims and hosts Voice Sessions via the `voice_session_intents` Postgres claim plane (ADR-0057). A pool of Voice Instances is the "voice-worker pool"; a single process is always a Voice Instance, never "a worker". | Worker (v1 term, deprecated in v2) |
| **Presence owner** | The single Voice Instance elected via the `presence_owner` claim row to dispatch gateway interactions for a shared (central) bot token; every other Voice Instance holding a session on that token still receives the events but drops them, so dispatch is exactly-once (ADR-0057). | Owner pod, Leader (leader election is the mechanism, not the term) |
| **Voice Session Intent** | A durable request row in `voice_session_intents` that a Voice Instance claims (`FOR UPDATE SKIP LOCKED`), runs, heartbeats, and closes; a stale heartbeat means the claiming Voice Instance is dead — the session is never migrated to another instance, only restarted (ADR-0006, ADR-0057). | Claim row, Session claim |
| **Web Instance** | A process running in `web` Mode that serves the web app and admin API. | Server, Web server |

## Relationships

- A **Tenant** has many **Members** with a **Member Role**, many **Campaigns**, many linked **Guilds**, and one or more **Provider Configs** per **Component**.
- A **Tenant** has at most one active **Subscription**, which references a **Plan** and snapshots its price; a Tenant's metered usage accumulates in the **Usage Ledger**.
- A **Campaign** has one **GM** (a Member of its Tenant), many **Characters**, one **Knowledge Graph**, exactly one **Butler**,many **Character NPCs**, and many **Transcripts**.
- A **Character** belongs to exactly one **Campaign** and is bound to exactly one **Discord User**; the Discord User may also be a **Linked Player**.
- A **Player Invitation** is minted by a **Campaign**'s **GM** for a **Character** (or the whole Campaign) at a **Player Access Level**; accepting it makes the Player a **Linked Player**.
- A **Character NPC** belongs to exactly one **Campaign**.
- A **Voice Session** binds (**Guild**, voice channel, **GM**, **Campaign**) and is hosted by exactly one **Voice Instance**.
- A **Transcript** belongs to exactly one **Voice Session** and transitively to one **Campaign**.
- An **Agent**'s **Tool Grants** reference **Tools**; tool invocations resolve **Active Campaign** for campaign-scoped Tools. A Tool's backing is built-in (in-process) or an **MCP Server** (out-of-process).

## Example dialogue

> **Dev:** "When the **GM** says 'Glyphoxa, roll a d20' inside a **Voice Session**, what handles the utterance?"

> **Domain expert:** "**Address Detection** routes it to the **Butler** because the **GM** spoke without naming a target. The **Butler** invokes the `dice` **Tool** via its **Tool Grant**."

> **Dev:** "And if the **GM** runs `/roll 1d20` as a **Slash Command** outside a **Voice Session**?"

> **Domain expert:** "Same **Butler** — there is exactly one per **Campaign**. **Active Campaign** is resolved from the **GM**'s profile rather than a **Voice Session** binding. The **Bot** acks the interaction; the **Butler** runs the **Tool**; the **Bot** posts the follow-up."

> **Dev:** "When 'Bart the innkeeper' speaks, what's that?"

> **Domain expert:** "Bart is a **Character NPC** — an **Agent** with `agent_role=character`, scoped to one **Campaign**. He has a **Persona** and a **Voice**, no **Tool Grants** by default. He's *also* an **NPC** **Node** in that **Campaign**'s **Knowledge Graph** — but those are separate records."

> **Dev:** "And a **Player** without a linked account — they can still play?"

> **Domain expert:** "Yes. Their **Character** carries their **Discord User** ID; that's enough for **Transcript** attribution and **Address Detection**. If they later sign in via Discord OAuth, they become a **Linked Player** and gain web app access to their **Characters** — without becoming a **Member** of the **Tenant**."

## Flagged ambiguities

- **"Session"** is overloaded — TTRPG culture uses it for "one night of play" and software uses it for browser/auth sessions. Canonical here: **Voice Session** for the Bot's presence in a voice channel. Don't use "session" alone in domain prose; never use it for auth ("login", "auth session" if needed).
- **"NPC"** is used as both a **Knowledge Graph Node** type AND as a label for **Character NPC** **Agents**. These are distinct: one is a wiki entry, the other is an AI persona that speaks. The Node may exist without an Agent (wiki-only NPCs are normal); when both exist, they describe the same in-world character but are separate records whose *content* may drift. An NPC **Node** may carry an optional, GM-maintained link to its **Character NPC** Agent (one-to-at-most-one, either side may exist alone); the link is how an Agent finds "its own" Node. Creating a **Character NPC** auto-creates its linked NPC **Node** (ADR-0008 second amendment, #479), and an Agent rename follows to the Node only while the two names still match — bodies and Personas are never synced.
- **"Character"** in plain TTRPG usage means *any* in-world person (PC or NPC). In our schema, **Character** is reserved for Player Characters only; **NPC** for non-player characters. Don't use "Character" generically.
- **"User"** is unsafe unqualified — it could mean **Discord User**, **Member**, **Linked Player**, or the request principal. Always qualify.
- **"Bot"** no longer necessarily means one token shared by every Tenant (amended by ADR-0057): a Tenant's Bot is central-token or BYOK per the Per-tenant client registry. Say "the Tenant's resolved Bot", not "the Bot acting on behalf of the Tenant" — the latter now begs the question of *which* token.
- **"Worker"** is a v1 term for what v2 calls a **Voice Instance**, and stays ambiguous even in v2 prose: "voice-worker pool" is shorthand for a pool of Voice Instances (ADR-0057), but a single process is never "a worker" — call it a **Voice Instance**. It does not imply the gateway/worker split that was explicitly removed (ADR-0005).
- **"Role"** is overloaded between **Member Role** (`owner`/`admin`/`gm`) and **Agent Role** (`butler`/`character`). Always qualify.
- **"Tier"** in prose means a **Plan** (the subscription catalog entry); the schema/API term is always Plan. Never use "tier" for a **Member Role** (already an alias to avoid there).
- **"Player role"** is *not* a Member Role — Players are not Tenant Members. Don't add `player` to the Member Role enum. A **Player Access Level** is also not a Member Role — it scopes a **Linked Player**'s reads, never Tenant administration.
- **"Invite"** is unsafe unqualified — Discord guild invites (`internal/discordinvite`, ADR-0047, `discord.com/invite/{code}`) vs **Player Invitations** (ADR-0056, `/join/<token>`). Always qualify.
- **"Barge-in" vs "Cross-talk"** — **Barge-in** is a *human* interrupting a speaking Agent (VAD-triggered via the Barge-in Confirm Window; cancels the Agent's whole turn — see ADR-0026). **Cross-talk** is one Agent's already-delivered text being fed to another addressed Agent during an Ensemble Turn — the second Agent has not spoken yet, so nothing is interrupted. Never use "barge-in" for the Agent-to-Agent case, and never use "cross-talk" for a human interruption.
