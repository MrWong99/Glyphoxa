# GM quickstart: from clone to a talking Character NPC

This guide walks a **GM** (who, in a v1.0 self-host deployment, is also the
**Operator**) from a fresh clone to a live **Voice Session** with one **Character
NPC** speaking in a Discord voice channel.

It is the *game-running* guide. Two neighbours it deliberately does not duplicate:

- [docs/configuration.md](configuration.md) — every environment variable, the
  `.env` template, the Discord-OAuth app registration, the operator allowlist,
  and the boot posture. **Step 1 delegates to it entirely.**
- [docs/agents/live-npc-run.md](agents/live-npc-run.md) — a *developer* runbook
  for the `voice`-only loop (`-hardcoded` NPC, cassettes, audio-pipeline
  debugging). Reach for it when the audio misbehaves, not when you want to play.

Architecture context: [docs/architecture.md](architecture.md).

Vocabulary (Mode, Operator, GM, Bot, Guild, Voice Session, Butler, Character NPC,
Provider Config, BYOK, Slash Command, Knowledge Graph, Transcript Line, Address
Detection) is defined in [CONTEXT.md](../CONTEXT.md).

**Order matters.** Each step below is a precondition of the next. The end state:
you speak into a Discord voice channel and an NPC answers out loud.

---

## 1. Setup: build, migrate, seed, first login

Follow [docs/configuration.md](configuration.md) §1–§7 end to end. It covers
prerequisites, `.env`, the build, the schema, the Discord OAuth app, the operator
allowlist, and the first login. This section only calls out what a GM must know
while doing it.

### Modes

The binary runs one **Mode** at a time ([ADR-0005](adr/0005-single-binary-modes-no-audio-rpc.md)):
`voice` (Discord voice loop), `web` (console + admin API), or `all` (both, in one
process — audio never crosses a process boundary).

**A GM wants `all`** — and it is the default, so a bare `glyphoxa` runs it (and
auto-applies migrations at startup). `web` alone serves the console but registers
no **Slash Commands** and cannot join voice.

```sh
./bin/glyphoxa            # -mode all is the default
```

### Schema and demo data

```sh
./bin/glyphoxa migrate up     # apply the schema; `migrate status|version|down` also exist
./bin/glyphoxa seed           # idempotent demo Tenant + Campaign + Character NPC
```

`migrate up` needs `$GLYPHOXA_DATABASE_URL` (or `$DATABASE_URL`). `seed`
additionally needs `$GLYPHOXA_SECRET`, and fails loudly without it.

> **`seed` is mandatory if you want voice in this build.** The voice loop does not
> load the Active Campaign's roster — it hardcodes the seeded demo Tenant
> **Glyphoxa Demo** and its Campaign **The Prancing Pony**
> (`internal/wirenpc/agentspec.go`). Skip the seed, create your own Campaign in
> the console, press **Start session**, and you get
> `wirenpc: load NPCs: find tenant: … not found` plus a **Failed** badge.
>
> Consequence: **the voiced roster is always the seeded Campaign's Character
> NPCs**, whichever Campaign the console calls Active. See §5a.

The console *does* offer **Create your first campaign** on the Configuration
screen when no Campaign exists yet (it mints the Campaign and its **Butler**).
That path is real for the web tier; it is not enough for voice.

### First login is allowlist-gated

The console is Discord-only OAuth, and the operator allowlist is the **single
gate** ([ADR-0041](adr/0041-operator-allowlist-access-policy.md)). There is **no
trust-on-first-use**: a Discord User whose snowflake is not in
`GLYPHOXA_OPERATOR_IDS` is **rejected before any session or Tenant write** and
bounced back to the login screen, which shows:

> This Discord account isn't on the operator allowlist for this instance.

So put your own Discord snowflake in `GLYPHOXA_OPERATOR_IDS` *before* the first
login — you cannot enrol yourself by logging in. Every non-dev `web`/`all` boot
also runs a **session revocation sweep**: sessions belonging to a Discord User no
longer on the allowlist are deleted at startup. Removing a snowflake and
restarting therefore ends that person's access immediately.

Open `http://127.0.0.1:8080`, click **Continue with Discord**, approve the
consent screen. You land on the **Configuration** screen (titled *Providers*).

The console has three screens, reachable from the left sidebar:
**Configuration**, **Campaign**, **Session**. The topbar carries the
Active-Campaign switcher.

---

## 2. Invite the Bot to your Guild and pick the voice channel

All of this happens in the **Discord connection** card on the **Configuration**
screen.

### 2a. Save the Bot token

The **Bot token** row is a write-only secret: paste the token, press **Save**.
Once saved it shows `••••••••` plus a **Replace** button — it is never read back.
When the Bot logs in successfully the row also shows `Connected as <bot tag>`.

The **Bot** is one Discord bot identity shared by the whole deployment. If
`DISCORD_BOT_TOKEN` is set in the environment, it is the fallback; the token saved
here is authoritative.

### 2b. Authorize the Bot into the Guild

At the foot of the Discord card is **Add Glyphoxa to your server**. Click it. It
opens Discord's bot-authorization URL, built client-side from the server-provided
Discord application id — the same OAuth app that backs your login
([ADR-0047](adr/0047-discord-invite-resolver-bot-authorization.md)).

The URL requests scope `bot applications.commands` (the second scope is what makes
the `/glyphoxa` Slash Commands appear in a freshly added Guild) and the minimum
permissions a voice Bot needs: **View Channel + Connect + Speak**.

Two things to know:

- **This is a separate, prerequisite step from saving the IDs.** Neither pasted
  link format below joins the Bot. Until the Bot is a member of the Guild, a
  Voice Session cannot join voice.
- If `DISCORD_OAUTH_CLIENT_ID` is unset the button is disabled with a note saying
  so — the link cannot be built.

### 2c. Fill the Guild and voice-channel IDs

The **Paste a Discord link** field takes either format:

| You paste | What happens |
|---|---|
| A **channel link** (`https://discord.com/channels/<guild>/<channel>`, from Discord's *Copy Link*) | Parsed in the browser, no network call. Both ID fields fill instantly. |
| An **invite link** (`discord.gg/<code>` or `discord.com/invite/<code>`) | Parsed in the browser to a bare invite code, then resolved server-side (with the decrypted Bot token) to the Guild's voice channels. A **picker** lists them by name and snowflake; clicking one fills both ID fields. |

Invite-resolution errors are specific:

| Message | Meaning |
|---|---|
| *That invite looks invalid or expired.* | Discord does not know that code. |
| *…the Bot is not a member of that server…* (plus a pointer back to **Add Glyphoxa to your server**) | Step 2b hasn't happened yet, or was done for a different Guild. |
| A precondition message about the token | No Bot token is saved yet — do step 2a first. |
| *Couldn't read that link…* | Not a Discord channel or invite link. |

A failed resolve leaves the fields and any previously resolved picker untouched.
Only voice channels are offered (stage channels are excluded).

You can also type the two snowflakes directly into **Guild ID** and **Voice
channel ID** (Discord → Settings → Advanced → Developer Mode, then right-click →
Copy ID).

Press **Save Discord settings**. The button stays disabled until *both* IDs are
non-empty. Saving the token and saving the IDs are independent writes, so
replacing the token never clobbers the IDs.

When a Bot token and a Guild are both configured, the Bot registers its Slash
Commands **per-Guild** at boot (and re-registers when you next save Discord
settings). They appear even with no Voice Session running.

---

## 3. Provider keys, health badges, and spend caps

Still on **Configuration**.

### 3a. BYOK provider keys

v1.0 is **BYOK** — you supply the credentials. The **Provider keys** section holds
two **Provider Config** rows:

| Row | Component | Where to get the key |
|---|---|---|
| **Groq** (LLM) | the Agent's reasoning | <https://console.groq.com> |
| **ElevenLabs** (Speech) | STT **and** TTS | <https://elevenlabs.io> |

Paste and **Save**. Each secret is sealed server-side with `$GLYPHOXA_SECRET` and
never read back; the row switches to `••••••••` + **Replace**. If
`$GLYPHOXA_SECRET` is unset the save is *rejected* and the row shows
`Couldn't save: …` — the key was **not** stored.

The Groq row also carries a **model** combobox listing the live Groq catalog. It
accepts a typed model id that is not in the catalog. Change the model on an
already-saved key and a **Save model** button appears; it re-seals the existing
key untouched.

### 3b. Health badges

Each row's badge reads:

| Badge | Meaning |
|---|---|
| **Key needed** (amber) | No key saved for this Provider. |
| **Healthy** (green) | A key is saved. Shown *immediately*, from key presence. |
| **Degraded** (red) | An asynchronous live test-call failed. Hover for the detail. |

The badge never blocks page load: it starts from key presence and downgrades only
once the real test-call (an ElevenLabs voices fetch, a Groq ping, a live Discord
login) comes back bad. A **Degraded** ElevenLabs badge means voices and speech
will fail in a Voice Session — fix it before starting one.

### 3c. Spend caps

The **Spend caps** card takes two USD figures, per Voice Session
([ADR-0046](adr/0046-spend-meter-price-map-cap-mechanics.md)):

- **Soft cap** — once estimated spend crosses it, no *new* Agent turns are taken.
  In-flight replies finish; transcription keeps running.
- **Hard cap** — the Voice Session ends cleanly. Must be ≥ the soft cap.

Leave a field blank to disable that cap. **Changes apply to the next Voice
Session**: caps snapshot at session start.

Every figure the console shows is an **estimate**, not a billed amount. Spend is
accumulated from provider-reported usage where available and from documented
fallback estimates otherwise (token counts, TTS characters, STT audio seconds —
[ADR-0045](adr/0045-provider-usage-metering-estimates.md)); prices are code
constants with an unknown-model fallback. Treat them as a guard rail, not
accounting. When a cap is reached the **Session** screen shows a banner with the
estimated spend.

---

## 4. Create a Character NPC on the Campaign screen

Open **Campaign** in the sidebar. It has two views, chosen with the segmented
control beside the title: **Cast** and **Knowledge**.

### 4a. Cast — the roster

The roster lists the **Butler** first (gold badge, role locked, cannot be deleted
— exactly one per Campaign) then every **Character NPC**. **+ Add NPC** creates
one named *New NPC* and selects it.

> **The Butler does not speak in this build.** It is a placeholder row: required
> and undeletable ([ADR-0009](adr/0009-single-agent-table-auto-butler.md)),
> editable here — and never loaded into a Voice Session. The voice loop's roster
> is Character NPCs only (`agent_role = 'character'`), so the Butler is never
> registered for **Address Detection** and has no voice. Naming it out loud does
> nothing. `/roll` is answered by the built-in `dice` Tool directly, not by the
> Butler reasoning about it. Everything below about the Butler is about a row you
> can edit, not an Agent you can talk to.

The editor pane on the right holds:

- **Name** and **Title** — the Title is a subtitle ("the innkeeper"). The Name is
  what **Address Detection** fuzzy-matches when a player says it out loud.
- **Persona** — Markdown describing personality, backstory and speech style. It is
  injected into the Agent's LLM prompt. This is the single biggest lever you have
  on how the NPC sounds; write a paragraph, not a word.
- **Voice** — a searchable dropdown of the *live* ElevenLabs voice catalog. Pick
  one, then **Preview voice** to hear a short sample in the browser. A failed
  preview prints `Couldn't preview: …` inline (usually a missing or Degraded
  ElevenLabs key). With no catalog the dropdown still shows the NPC's persisted
  voice id, so a degraded TTS never wedges the screen.
- **Address only — waits to be named** — when on, this Character NPC replies only
  when addressed by name; it is excluded from Address Detection's last-speaker and
  single-NPC fallbacks. The Butler's switch is forced on and disabled — but that
  is bookkeeping, not behaviour: the Butler is in no roster, so naming it gets no
  reply either way.
- **Tools** — the **Tool Grant** toggles. One row per built-in Tool the server
  exposes (today: `dice`). Granting a Tool is what lets that Agent invoke it. A
  Tool that supports per-grant scoping also shows a raw-JSON scope field with a
  **Save scope** button; `dice` supports no scope, so it renders only the switch.
  **Grant changes take effect in the next Voice Session.**

Press **Save changes**. **Delete NPC** removes a Character NPC (never the Butler).

> **Aliases.** A Character NPC's aliases are part of the domain model — Address
> Detection matches them, and `/glyphoxa mute` resolves them — but **this build of
> the console ships no alias editor**. An NPC created in the console has no
> aliases; only its Name matches. Don't plan a session around them.

### 4b. Knowledge — the Knowledge Graph

Switch to **Knowledge**. An "entry" here is a **Node** of the Campaign's
**Knowledge Graph**.

Create one with the editor card on the right: **Type**, **Name**, **Content**, and
the **GM private** switch. Seven Node types:

`Character` · `NPC` · `Location` · `Faction` · `Item` · `Plot thread` · `Note`

**The type is chosen once, on create, and is immutable** — the select is disabled
when editing an existing entry.

- **A public entry reaches an NPC's prompt only through that NPC's Voiced-by
  Node.** The facts an Agent gets are its own linked Node (see below) plus that
  Node's one-hop Edge neighbours, capped, non-`gm_private` only. **An Agent with
  no linked Node gets no Knowledge Graph facts at all** — there is no campaign-wide
  fallback. So: to make an NPC know the tavern's name, give the NPC a Node, link
  it with **Voiced by**, and connect it to the tavern's Node with an Edge.
- **GM private entries never enter an NPC's prompt** and never reach the table.
  They stay searchable for you, and carry a **GM private** badge in the list. (A
  `gm_private` Node is still traversed for its Edges — its neighbours can surface;
  its own content never does.)

The list groups entries by type and has a full-text **search** box (names and
content).

Open an entry to edit it and the editor grows a **Relations** section: the
**Edges** touching this Node. Edges are strictly one-way, so **outgoing** edges
(editable here) and **incoming** edges (dimmed, edited from the other entry) are
listed separately. Nine edge types: `resides_in`, `member_of`, `owns`, `knows`,
`enemy_of`, `ally_of`, `parent_of`, `participated_in`, `mentioned_in`.

An entry of type **NPC** additionally offers a **Voiced by** select, linking that
Node to one of the Campaign's Character NPC Agents. The link is how an Agent finds
"its own" Node — it is *not* an auto-sync. A wiki-only NPC with no Agent is
perfectly normal, and so is an Agent with no Node.

Deleting an entry is a hard delete and **cascades to every Edge that touches it**.
The confirm dialog counts those relationships first and keeps the confirm button
disabled while the count is in flight. If the count *fails*, the button enables
anyway and the dialog says the relationships couldn't be counted — the delete
still cascades server-side.

---

## 5. Run a Voice Session

Preconditions, all from above: Bot token saved, Bot authorized into the Guild
(step 2b), Guild ID + Voice channel ID saved, Groq + ElevenLabs keys **Healthy**,
running in `-mode all`.

Join the voice channel yourself in Discord first — the Bot joins the channel you
configured, and it needs someone to talk to.

### 5a. Start and stop

Two equivalent surfaces. They drive the same in-process session manager and share
one session record, so they can never diverge:

- **Session** screen → **Start session** / **Stop session**.
- In Discord: `/glyphoxa start` and `/glyphoxa end`.

The Campaign a Voice Session binds to is the **Active Campaign**. The Session
screen resolves it from the topbar switcher; the Slash Commands resolve it in this
order: the live Voice Session's Campaign → your durable `/glyphoxa use` selection
→ *fail* with a message telling you to run `/glyphoxa use`. There is no
most-recently-created fallback on the Slash Command surface.

> **The Active Campaign does not choose who speaks — yet.** It binds the
> `voice_sessions` row, the Transcript, and the mute rail. The *voiced roster* is
> always the seeded **Glyphoxa Demo** / **The Prancing Pony** Campaign (§1). With
> two Campaigns you hear the seed Campaign's NPCs while every console surface
> names the other one — and that Campaign's per-row mute toggles then have nothing
> to mute. Run your table on the seeded Campaign until this is fixed.

The status badge reads **Idle**, **Live** (with a **Connecting…** → **Connected**
sub-label while the gateway comes up) or **Failed** (with the reason). Beside it,
an `HH:MM:SS` elapsed timer. When a Voice Session ends the screen shows a one-line
summary: *Last session ended HH:MM · Xh Ym · N lines transcribed.*

If Start refuses, the message says exactly why: a session is already active,
Discord isn't configured, no bot token, the saved token could not be decrypted,
voice isn't available in this Mode, or the server is shutting down.

### 5b. The live transcript feed

The **Live transcript** panel fills as people speak: one **Transcript Line** per
human utterance or per coalesced Agent reply, each with a speaker, an optional tag,
and an `HH:MM:SS` timestamp. Agent lines are named; **every human at the table
shares one anonymous "Player" lane** — STT carries no speaker identity in this
build, so the Transcript cannot tell your players apart. Between an utterance and a reply, a typing indicator
shows which Agent is thinking. Lines are persisted, so reloading the page mid-
session replays them.

Above it, a **search box** searches the Active Campaign's transcript. Clicking a
hit highlights and scrolls to that line — unless the hit belongs to an earlier
Voice Session, in which case the result carries an inline *"From an earlier
session — not in the view"* note rather than jumping to an unrelated line.

### 5c. Per-Agent mute

The right rail of the Session screen is the **Voice control** panel: one row per
Agent of the Active Campaign (Butler first), an `N of M voicing` count, a per-row
mute toggle, and one button whose label flips between **Mute all** and **Unmute
all** depending on whether anything is currently unmuted.

A muted Character NPC **stays in the scene but doesn't speak aloud** — it is still
part of the Campaign, it just produces no audio. Unmute mid-session at any time.

> Two rows in this rail lie by omission. The **Butler** row is listed and labelled
> *Butler · voicing*, but the Butler is in no Voice Session roster and never emits
> audio (§4a) — muting it changes nothing audible. And because the voiced roster is
> always the seeded Campaign (§5a), a row for an NPC of some *other* Active
> Campaign has no voice to mute either.

The toggles are only actionable while a Voice Session is live; with no session
every row shows unmuted and disabled. The `/glyphoxa mute` and `/glyphoxa muteall`
Slash Commands mute the same Agents — **but there is no un-mute Slash Command**.
Un-muting is the web panel's job.

### 5d. Barge-in, from the player's perspective

An Agent never holds the floor against a human
([ADR-0027](adr/0027-barge-in-confirm-window-cancels-turn.md)). What that means at
the table:

- **Just talk over the NPC.** Keep speaking for about a quarter-second (the
  **Barge-in Confirm Window** — fixed at 250 ms of continuous voiced speech in this
  build; ADR-0027's per-Agent tunable is not wired) and the NPC cuts off
  *immediately* — mid-word, no drain, no finishing the sentence. (Barge-in also
  tears down a whole **Ensemble Turn**; Ensemble Turns are deferred and cannot
  occur in this build — one utterance addresses at most one Agent.)
- **A backchannel does not cut it off.** A "mhm", a "yeah", a cough — anything
  shorter than the window — is **Soft-overlap**. The NPC keeps going. Your
  backchannel is still transcribed and still becomes context for the next reply.
- **Anyone at the table can barge in**, not just the person the NPC was answering,
  and not just the GM. ADR-0027 measures the window *per participant*, so parallel
  backchannels never sum into a false trigger — but **this build runs one shared
  VAD session with no speaker identity**, so overlapping table talk is heard as
  one continuous voice and can cross the window together. Per-participant lanes
  land with ADR-0050.
- **The NPC cannot barge in on itself.** Nothing an Agent says is treated as a
  human speaking, and a turn that is still *thinking* (holding the floor before
  its first audio) cannot be barged — you haven't heard anything to react to yet.
- **There is no auto-resume.** The interrupted NPC goes quiet and stays quiet.
  Your interruption is just your next utterance: Address Detection routes it, and
  it may come back to the same NPC — which will then answer freshly, with the
  interruption in its context. An "as I was saying…" is emergent, never scripted.

---

## 6. Slash Command reference

Registered per-Guild at boot against the configured Guild, on the Bot's standing
gateway connection. They are available with **no Voice Session running**.

**They exist only under `-mode all`.** Registration lives in the web tier's boot
path, guarded on that Instance also driving voice — so `-mode web` registers
nothing, and `-mode voice` (which never builds the command registry) registers
nothing either. Run `all`.

Authorization is server-side; Discord's own command-permission settings are a UX
hint only. In v1.0, **GM only** means *the invoking Discord User's snowflake is on
`GLYPHOXA_OPERATOR_IDS`* ([ADR-0010](adr/0010-slash-command-surface.md) amendment,
[ADR-0041](adr/0041-operator-allowlist-access-policy.md)). Interactions from
outside the configured Guild — including DMs — are refused.

| Command | Option | Who | Reply | What it does |
|---|---|---|---|---|
| `/roll <dice>` | `dice` (string, **required**) | anyone in the Guild | public | Rolls an `NdM` expression (`2d6`, `d20` — `N` defaults to 1) with the same built-in `dice` Tool the Agents use. A malformed or out-of-bounds expression gets an ephemeral hint. |
| `/glyphoxa use <campaign>` | `campaign` (string, **required**, autocompletes) | GM only | ephemeral | Durably sets your **Active Campaign**. Pick from the autocomplete (which carries the campaign id) or type an exact name. |
| `/glyphoxa start` | — | GM only | ephemeral | Starts the Voice Session for the Active Campaign. Same manager as the Session screen's **Start session**. |
| `/glyphoxa end` | — | GM only | ephemeral | Ends the active Voice Session. Says so plainly if none is running. |
| `/glyphoxa search <query>` | `query` (string, **required**) | GM only | ephemeral | Searches the Active Campaign's Transcript and quotes the top 5 matching Transcript Lines with speaker and UTC timestamp. Same search path as the Session screen's box. |
| `/glyphoxa mute <npc>` | `npc` (string, **required**, autocompletes) | GM only | ephemeral | Mutes one Agent's voice in the live Voice Session. Despite the option name it accepts **any** Agent, the Butler included; it resolves the autocomplete value, then an exact name, then an alias. Refused when no Voice Session is active. |
| `/glyphoxa muteall` | — | GM only | ephemeral | Mutes every Agent's voice in the live Voice Session. Refused when no Voice Session is active. |

There is deliberately **no unmute Slash Command** — un-muting is done on the
Session screen's Voice control panel (§5c).

This table is the complete registered surface. Keep it current as later epics add
commands.

---

## Troubleshooting the walkthrough

| Symptom | Likely cause |
|---|---|
| Login bounces to *not on the operator allowlist* | Your snowflake isn't in `GLYPHOXA_OPERATOR_IDS`. There is no self-enrolment. |
| The console is a blank page | The web bundle wasn't built before `make build` (configuration.md §3). |
| No `/glyphoxa` commands in Discord | Running `-mode web` **or `-mode voice`** — only `-mode all` registers them; or no Bot token saved; or no Guild ID saved; or the Bot was authorized without `applications.commands`. Re-save Discord settings to retrigger registration. |
| *the Bot is not a member of that server* on an invite paste | Step 2b — **Add Glyphoxa to your server**. |
| **Start session** refuses | Read the message; it names the precondition. |
| Session flips to **Failed** with `find tenant … not found` / `find campaign … not found` | `glyphoxa seed` was never run. The voice loop loads the seeded **Glyphoxa Demo** / **The Prancing Pony** Campaign, not your Active Campaign (§1, §5a). |
| Session goes **Live**, nobody speaks | ElevenLabs badge **Degraded** or **Key needed**; or every Agent is muted; or your NPC is **Address only** and nobody said its Name. |
| The NPC keeps getting cut off | Someone is barging in (§5d). Table chatter longer than ~250 ms of continuous speech from one person cancels the turn. |
| Turns stop mid-session | Soft spend cap reached (§3c). The Session screen banner says so. |
| Audio is broken at the codec level | That's a developer problem — [docs/agents/live-npc-run.md](agents/live-npc-run.md). |
