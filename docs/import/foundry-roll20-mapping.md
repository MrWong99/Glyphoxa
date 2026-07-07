# External-tool converters: Foundry VTT / Roll20 → Glyphoxa mapping (DRAFT)

> Research spike for issue #289 (parent #255, Epic 6/8). This is **research + product
> analysis, not conversion code**. A maintainer should be able to decide from this doc
> alone whether a Foundry→Glyphoxa converter is worth building this year, and which
> directions are dead ends.
>
> Status: DRAFT for review. Author: research agent, 2026-07-08.

---

## 0. TL;DR — go/no-go by direction

| Direction | Verdict | One-line reason |
|-----------|---------|-----------------|
| **Foundry → Glyphoxa** | **GO (narrow, phase 1)** | Clean, supported per-document JSON export exists; journals→KG Nodes and actors→Nodes(+NPC Agent stubs) is a real win even after heavy loss. |
| **Glyphoxa → Foundry** | **NO-GO (defer)** | Glyphoxa's exportable content is a tiny subset of a usable Foundry world; export produces a near-empty world of text handouts. Low value. |
| **Roll20 → Glyphoxa** | **NO-GO (conditional)** | No official/legal bulk export; only an unsupported scraper whose own README warns of account termination. Don't build a first-party importer on that foundation. Revisit only if Roll20 ships an official export API. |
| **Glyphoxa → Roll20** | **NO-GO** | Roll20 has no import surface for arbitrary campaign content (no JSON importer, by design), plus the same content-subset problem. Hard no. |

The converter's natural target is the **Campaign Bundle** (per #287 decisions: gzipped JSON
envelope, `format_version`, mint+remap IDs, secrets excluded, history flag-gated), **not the
database**. A Foundry importer should *emit a Bundle* and let the existing import slice ingest
it — this reuses ID-minting/dedup and keeps the converter decoupled from DB internals.

---

## 1. Glyphoxa's target model (what a converter can actually fill)

From the repo schema (`internal/storage/migrations/`) and `CONTEXT.md`:

| Glyphoxa entity | Fields a converter could populate | Source of the field |
|-----------------|-----------------------------------|---------------------|
| **Campaign** | `name`, `system` (free-text ruleset label), `language` | World/campaign name; system id is a label only |
| **KG Node** (`kg_node`) | `node_type` (`character`/`npc`/`location`/`faction`/`item`/`plot_thread`/`note`), `name`, `body` (prose), `gm_private` | Journal entries, actor prose |
| **KG Edge** (`kg_edge`) | `from_node_id`, `to_node_id`, `edge_type` (`resides_in`/`member_of`/`knows`/`mentioned_in`/…) | Cross-references, scene note pins — heuristic only |
| **Agent** (Character NPC) | `name`, `persona` (markdown), `aliases[]`, `title`, `address_only` | Actor name + biography prose; **stub only** |
| **Character** (PC, `#276`, not yet built) | `name`, `aliases[]`, `discord_user_id`, `linked_user_id` | PC actor name; **Discord IDs have no source** |

**Structurally impossible to source** (no equivalent exists in either VTT):
- **Voice** (TTS provider + voice_id JSONB), **LLM config**, **Tool Grants**, the **Butler** —
  these are Glyphoxa runtime constructs. An imported NPC is a mute, tool-less wiki stub until a
  GM configures it.
- **Transcripts / Transcript Lines / Voice Sessions** — produced by playing in Glyphoxa; a
  source VTT has nothing analogous.
- **Discord bindings** (`discord_user_id`, `linked_user_id`, `guild_id`) — Foundry/Roll20
  identities are their own accounts/player-handles, never Discord snowflakes. A PC import cannot
  satisfy `discord_user_id NOT NULL` without operator input.
- **Provider Configs / secrets** — excluded from the Bundle by policy anyway (#287 secrets list).

---

## 2. Foundry VTT

### 2.1 What is exportable, and in what format

Foundry stores a game as a **World**: a directory of documents you fully control on a self-host.
Persistence moved from **NeDB** (plain JSON-lines `.db` files, human-readable but unsafe to
hand-edit) to **LevelDB/ClassicLevel** (binary SSTables) in **v11**; both world documents and
compendium packs now live as LevelDB under `packs/`. ([v11 packaging changes](https://foundryvtt.com/article/v11-leveldb-packs/), [NeDB→LevelDB migration issue #5065](https://github.com/foundryvtt/foundryvtt/issues/5065))

There are four export paths, in decreasing convenience for a converter:

1. **Per-document "Export Data" → single JSON file.** Right-click any Actor, JournalEntry, or
   Scene → *Export Data* yields one self-contained JSON document; *Import Data* on a matching
   document type reads it back. This is the cleanest, fully-supported, per-entity path and the
   one a converter should target first. ([Actors article](https://foundryvtt.com/article/actors/))
2. **Folder → "Export to Compendium"**, then read the compendium pack. ([Compendium packs](https://foundryvtt.com/article/compendium/))
3. **`foundryvtt-cli` (`fvtt package unpack/pack`)** converts LevelDB packs ↔ JSON/YAML files.
   Official CLI; requires the DB unlocked (world shut down). The recommended git-friendly
   workflow keeps content as JSON/YAML and builds LevelDB from it. ([CLI issue #39](https://github.com/foundryvtt/foundryvtt-cli/issues/39), [v11 packaging](https://foundryvtt.com/article/v11-leveldb-packs/))
4. **Raw world directory** on a self-host — you own the files, but LevelDB is binary and must
   not be edited outside its API. Use path 1 or 3 instead.

**All Foundry documents share a top-level JSON shape:** `_id`, `name`, `type`, `img`, `system`
(the system-specific data object), `folder`, `flags`, `sort`, `ownership`, `_stats`. Compendium
packs are single-type and can hold `Actor`, `Item`, `JournalEntry`, `Macro`, `Playlist`,
`RollTable`, `Scene`, and `Adventure` documents. ([Compendium doc types](https://deepwiki.com/foundryvtt/foundryvtt/4.3-compendium-packs), [Document API](https://foundryvtt.com/api/classes/foundry.abstract.Document.html))

Field detail for the three documents that matter to Glyphoxa:

- **JournalEntry** — `name`, `folder`, `ownership`, `flags`, plus a `pages[]` array of
  **JournalEntryPage** documents. Each page: `name`, `type` (`text`/`image`/`pdf`/`video`),
  `title`, `text.content` (**rich HTML**), `text.format` (1 = HTML, 2 = markdown), `src`/`image`
  for media, `ownership`, `sort`, `flags`. Prose lives as HTML in `text.content`. ([JournalEntryPage v13 API](https://foundryvtt.com/api/v13/classes/foundry.documents.JournalEntryPage.html))
- **Actor** — `name`, `type` (system-defined, e.g. `character`/`npc`), `img`, `prototypeToken`,
  `system` (**all stats/attributes — schema is entirely system-specific**, e.g. dnd5e keeps the
  bio as HTML at `system.details.biography.value`), embedded `items[]` and `effects[]`, `folder`,
  `ownership`, `flags`. ([Actors article](https://foundryvtt.com/article/actors/))
- **Scene** — `name`, `background` (image), width/height/grid, and embedded placeables:
  `walls[]`, `lights[]`, `tokens[]` (Actor instances on the map), `tiles[]`, `sounds[]`,
  `drawings[]`, `notes[]` (map pins that link a `JournalEntry`), regions. This is tactical/render
  data. ([Scenes / canvas layers](https://foundryvtt.com/article/canvas-layers/), [Scenes article](https://foundryvtt.com/article/scenes/))

### 2.2 Semantic mapping: Foundry → Glyphoxa

| Foundry source | → Glyphoxa target | Notes / transform |
|----------------|-------------------|-------------------|
| World name / campaign | **Campaign** (`name`; optionally `system` id → `system` free-text) | `language` unknown → default `en`. |
| **JournalEntry** (handout / lore) | **KG Node** | `node_type` inferred from folder name/keywords (npc/location/faction/item/plot → else `note`); `body` ← page `text.content` after **HTML→markdown/plaintext**; `gm_private` ← page `ownership` (GM-only visibility → `true`). Multi-page entries: concatenate into one Node's `body`, or split per page. |
| **Actor** with `type = npc` | **KG Node** `node_type=npc` **(+ optional Character NPC Agent stub)** | Node `body` and Agent `persona` ← biography HTML→markdown; Agent `name`/`aliases` ← actor + token name; set the `kg_node.agent_id` link. **Stub only** — no Voice/LLM/Tool Grants. |
| **Actor** with `type = character` (PC) | **Character** (PC) table — **blocked on #276** | `name`, `aliases` map; **`discord_user_id` cannot be derived** (must be operator-supplied or the row is invalid). Defer until #276 ships. |
| Journal `@UUID` cross-links; Scene `notes[]` pins → journals | **KG Edge** `mentioned_in` (heuristic) | Foundry has almost no first-class relationship graph; edges must be *mined* from embedded links. Low-fidelity, optional. |
| **Scene** (maps, walls, lighting, tokens, tiles, sounds) | **— (dropped)** | No Glyphoxa home. At most, a Scene name → a `location` Node; its journal-note pins → `mentioned_in` edges. The tactical layer is lost by design. |
| **Item / spell / stat block / macro / RollTable / Playlist**, `system` numbers | **— (dropped)** | Glyphoxa is voice + narrative + KG, **not a rules engine**. `Campaign.system` is a label, not a mechanics store. |

**What is LOST (Foundry→Glyphoxa):** all rules mechanics (HP/AC/abilities/items/spells/macros),
all maps/scenes/tokens/lighting, playlists/audio; and on the Glyphoxa side everything with no
source — Voice, Tool Grants, LLM config, the Butler (auto-created by trigger, then merged per
#287's Butler-merge rule), Transcripts, and all Discord bindings.

### 2.3 Precedent

`R20Converter` already converts Roll20 exports into full Foundry worlds (GPLv3, open-sourced),
proving the VTT-to-VTT direction is tractable for a mechanics-carrying target. Glyphoxa is the
opposite: it discards mechanics and keeps only prose + roster, which is a *simpler* transform but
a *lossier* one. ([R20Converter](https://github.com/kakaroto/R20Converter), [README](https://github.com/kakaroto/R20Converter/blob/master/README.md))

### 2.4 Licensing / ToS (Foundry)

- **Your own content** (homebrew NPCs, handouts, notes): the Foundry EULA states *you retain
  ownership of personal data created within the software*. Safe to export and re-import into
  Glyphoxa. ([Software license](https://foundryvtt.com/article/license/))
- **Premium modules** are protected by Foundry's **Premium Content System** (per-purchaser
  content keys); they cannot be re-exported or redistributed. A converter must not attempt to
  unlock or repackage premium module content. ([Premium content](https://foundryvtt.com/article/premium-content/), [Licensing guide](https://foundryvtt.com/article/licensing-guide/))
- **Rules text / stat blocks:** D&D 5e **SRD 5.1 and 5.2 are now CC-BY-4.0** — freely
  redistributable with attribution, so SRD-derived prose is safe. Non-SRD published text/stat
  blocks are not, but Glyphoxa **drops stat blocks anyway**, so this risk is minimal for a
  prose-only import. ([dnd5e system license](https://github.com/foundryvtt/dnd5e), [licensing guide](https://foundryvtt.com/article/licensing-guide/))
- **Net:** Foundry import is the lowest-risk direction — the user exports their own world with
  first-party tooling, and Glyphoxa ingests only prose + roster, which is exactly the content the
  user authored and owns.

---

## 3. Roll20

### 3.1 What is exportable, and in what format

**There is no official downloadable JSON/campaign export — by design.** Official options are all
in-platform or account-locked:

- **Character Vault** — moves a character *snapshot* between games *inside Roll20*; not a file.
  Default access lets only Plus/Pro subscribers import; Free games cap at 3 external character
  exports. ([Character Vault wiki](https://wiki.roll20.net/Character_Vault), [Roll20 Characters help](https://help.roll20.net/hc/en-us/articles/360037258594-Roll20-Characters))
- **Transmogrifier** (Pro-only) — copies content *within a single account*, never cross-account.
- **Sheet-specific import/export** — a handful of sheets (Shadowrun 5E, PF1E community) have
  built-in text/JSON import; not general.

([No official JSON export, by design](https://app.roll20.net/forum/post/10833048/character-sheet-export-or-backup), [import/export forum](https://app.roll20.net/forum/post/1268471/import-slash-export-characters))

**Unofficial / community** tools do produce files:

- **R20Exporter** (kakaroto) — Chrome extension; exports a whole campaign + assets to a **ZIP of
  JSON files** (characters, handouts, page/token data). LGPL; Chrome-only (uses LocalStorage).
  ([R20Exporter repo](https://github.com/kakaroto/R20Exporter), [Chrome store](https://chromewebstore.google.com/detail/r20exporter/apbhfinbjilbkljgcnjjagecnciphnoi))
- **R20Converter** (kakaroto) — desktop app turning an R20Exporter ZIP into a full Foundry world
  (GPLv3). Requires the OGL "D&D 5e by Roll20" or Shaped sheet for stat conversion. ([R20Converter](https://github.com/kakaroto/R20Converter))
- **VTTES / Pauper's Character Vault (BetteR20)** and `justas-d/roll20-character-exporter-importer`
  — per-character JSON to a local file. ([exporter/importer](https://github.com/justas-d/roll20-character-exporter-importer))

The **Roll20 API** is not a viable bulk-export route: writing custom export scripts hits API
heartbeat/rate limits, size caps on bios/notes, and read-only token/tag restrictions. ([API export thread](https://app.roll20.net/forum/post/4608273/export-and-import-sheet-data-api), [bulk import/export thread](https://app.roll20.net/forum/post/8248707/bulk-characater-import-slash-export))

### 3.2 Semantic mapping: Roll20 → Glyphoxa

Content shape is the same as Foundry (handouts → KG Nodes, characters → Nodes / PCs, minus maps
and mechanics), so section 2.2's table applies. The blocker is **not** the mapping — it is the
absence of a clean, supported, legal export path.

### 3.3 Licensing / ToS (Roll20) — the decisive constraint

- **Your own materials:** Roll20's ToS says you retain IP in content you create/upload. ([ToS](https://help.roll20.net/hc/en-us/articles/360037770793-Terms-of-Service-and-Privacy-Policy))
- **Marketplace / provider assets** (tokens, maps, audio, purchased adventures) are provider IP,
  licensed for **personal, non-commercial, in-platform** use only. The **Marketplace Asset EULA**
  forbids reproducing, copying, reverse-engineering, or circumventing content-usage controls on
  Assets, and redistributing campaigns containing them *outside Roll20* requires licensing each
  provider directly. ([Marketplace Asset EULA](https://help.roll20.net/hc/en-us/articles/360037254294-Marketplace-Asset-EULA))
- **The scraper itself is ToS-questionable.** R20Exporter's own README warns it *"may still be
  against the Roll20 Marketplace Asset EULA"* and could be grounds for *account suspension or
  termination* even on owned content. Roll20 staff declined to support it, citing that it *"could
  provide an easy way to pirate content."* ([R20Exporter README](https://github.com/kakaroto/R20Exporter), [staff non-support thread](https://app.roll20.net/forum/post/11457260/r20exporter-is-not-supported-but))
- **Net:** building a first-party Glyphoxa importer *on top of* an unsupported, EULA-questionable
  scraping extension makes Glyphoxa a party to the risk. Not advisable.

---

## 4. Go/no-go per direction (with rationale)

**Foundry → Glyphoxa — GO (narrow, phase 1).**
The source has a clean, supported, per-document JSON export (path 1) and an official CLI (path 3),
and the content maps cleanly onto KG Nodes (journals) and Node+NPC-Agent stubs (actors). The loss
is severe (all mechanics, all maps) but *acceptable*: the surviving prose + NPC roster is exactly
what a fresh Glyphoxa campaign otherwise starts empty on. The converter should emit a **Campaign
Bundle** (per #287), not touch the DB. Recommended follow-up issues: (a) a `foundry-import`
command that reads exported JSON/`fvtt`-unpacked packs and emits a Bundle; (b) HTML→markdown for
`text.content`; (c) `node_type` inference from folder/keywords; (d) `ownership`→`gm_private`
mapping; (e) optional actor→NPC-Agent-stub toggle. PC import waits on #276.

**Glyphoxa → Foundry — NO-GO (defer).**
Glyphoxa can only export what it holds — KG Nodes (as JournalEntries) and NPC personas — which is
a tiny fraction of a usable Foundry world. The result is a near-empty world of text handouts with
no maps, no tokens, no stat blocks, no system data; the user rebuilds the entire tactical and
mechanical layer by hand regardless. The one cheap gesture, if ever wanted, is emitting one
`JournalEntry` `.json` per Node for manual *Import Data* — but that is a thin convenience, not a
migration, and doesn't justify a slice this year.

**Roll20 → Glyphoxa — NO-GO (conditional).**
The mapping is fine; the export path is the problem. No official bulk export exists (by design),
and the only whole-campaign route is an unsupported browser extension that its own author flags as
potentially EULA-violating and account-terminating, and that Roll20 staff refuse to support as a
piracy vector. A first-party importer built on that is a legal and reputational liability. If
demand is real, the safe posture is to *document* a manual path (the user runs R20Exporter
themselves, at their own risk, then feeds the Foundry-shaped JSON through the Foundry importer) —
**without shipping, bundling, or endorsing the scraper.** Reconsider only if Roll20 ships an
official data-export API.

**Glyphoxa → Roll20 — NO-GO.**
Roll20 has no import surface for arbitrary campaign content — no JSON importer, by design — and
Glyphoxa's exportable content is the same thin subset as the Foundry-export case. There is nothing
to build against and little worth sending. Hard no.

---

## 5. Recommended next actions (if the maintainer says "go")

1. File a follow-up issue: **Foundry→Glyphoxa importer (phase 1, prose + NPC roster → Campaign
   Bundle)**, blocked-informed-by #287 (Bundle ADR) and noting #276 for PC import.
2. Do **not** file Roll20 or reverse-direction issues; record them here as declined with rationale.
3. Keep the importer's output = Bundle, so it inherits #287's ID-mint/remap, secrets exclusion,
   and Butler-merge rules for free.

---

## Sources

Foundry:
- https://foundryvtt.com/article/v11-leveldb-packs/ — NeDB→LevelDB, packs, CLI workflow
- https://github.com/foundryvtt/foundryvtt/issues/5065 — NeDB→LevelDB migration rationale
- https://foundryvtt.com/api/v13/classes/foundry.documents.JournalEntryPage.html — page fields
- https://foundryvtt.com/article/actors/ — Actor doc, Export/Import Data
- https://foundryvtt.com/article/canvas-layers/ and https://foundryvtt.com/article/scenes/ — Scene layers
- https://deepwiki.com/foundryvtt/foundryvtt/4.3-compendium-packs — compendium doc types
- https://foundryvtt.com/api/classes/foundry.abstract.Document.html — shared document base
- https://github.com/foundryvtt/foundryvtt-cli/issues/39 — CLI JSON export
- https://foundryvtt.com/article/license/ , /premium-content/ , /licensing-guide/ — EULA, premium, content licensing
- https://github.com/foundryvtt/dnd5e — SRD 5.1/5.2 CC-BY-4.0

Roll20:
- https://wiki.roll20.net/Character_Vault — Character Vault
- https://help.roll20.net/hc/en-us/articles/360037258594-Roll20-Characters — export tiers
- https://app.roll20.net/forum/post/10833048/character-sheet-export-or-backup — no official JSON export
- https://app.roll20.net/forum/post/4608273/export-and-import-sheet-data-api — API export limits
- https://github.com/kakaroto/R20Exporter — R20Exporter (ZIP export, ToS warning)
- https://github.com/kakaroto/R20Converter — R20Converter (Foundry precedent, GPLv3)
- https://github.com/justas-d/roll20-character-exporter-importer — per-character exporter
- https://app.roll20.net/forum/post/11457260/r20exporter-is-not-supported-but — staff non-support
- https://help.roll20.net/hc/en-us/articles/360037254294-Marketplace-Asset-EULA — asset EULA
- https://help.roll20.net/hc/en-us/articles/360037770793-Terms-of-Service-and-Privacy-Policy — ToS
