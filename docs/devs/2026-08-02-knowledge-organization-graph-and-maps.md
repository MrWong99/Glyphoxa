# Knowledge & NPC organization: graph views, world maps, and the structural gaps underneath

Status: **proposal, nothing decided.** Written 2026-08-02 from the operator ask
"organize the knowledge and NPCs better — maybe graph visualization, maybe actual
world maps to localize objects, NPCs or places." This note grounds that ask against
the KG as it exists on `main` today, then proposes concrete slices in three tracks:
**A — graph visualization**, **B — world maps and localization**, **C — organization
work neither of us asked for but that the code is asking for.**

Nothing here is an ADR yet. Section 6 lists the ADR amendments and CONTEXT.md terms
each track would force, so the decisions can be taken deliberately rather than
discovered mid-build.

---

## 0. What the Knowledge Graph actually is today

Verified against `main` (72b544e), because the proposals below only make sense
against the real surface:

| Piece | Where |
|---|---|
| `kg_node` — 7 immutable types, `name`, `body`, `gm_private`, nullable `agent_id` | `internal/storage/migrations/00010_kg_node.sql`, `00012_kg_edge.sql` |
| `kg_edge` — 9 relation types, strictly directional, `UNIQUE(from,to,type)`, cascade-delete | `00012_kg_edge.sql` |
| Fulltext (`tsvector`) + vector (`vector(768)`, HNSW cosine) on nodes | `00011_kg_node_fts.sql`, `00030_kg_node_embedding.sql` |
| Node/Edge CRUD, search, similarity, LLM draft-and-apply, proposal review | `CampaignService` RPCs in `proto/glyphoxa/management/v1/management.proto` |
| GM UI — grouped flat list + sticky editor + per-node relations card | `web/src/screens/campaign/KnowledgePanel.tsx`, `NodeRelations.tsx` |
| Prompt injection — the Agent's own Node **plus single-hop neighbours**, gm-public only, ≤20 facts / ≤4000 chars | `internal/kgfacts/kgfacts.go`, `storage.PromptKGView` |
| Agent writes — proposals only, never direct | `internal/knowledge`, ADR-0052 |
| Blob seam for binaries; Gemini image generation | `internal/blob` (ADR-0048), `internal/imagegen` (ADR-0004 amendment) |

ADR-0008 scoped v1.0 as "structured wiki / GM notes. Form-based UI; **no graph
viz**." That was a deliberate v1.0 cut, not a permanent posture — the same ADR's
roadmap already reserves v1.5 (KG in inference, now shipped) and v2.x (temporal
story state). So Track A is the roadmap's next step, not a reversal. It still needs
an amendment note, because "no graph viz" is written down.

---

## 1. The three gaps, named

Before features: what is actually badly organized right now. Everything below hangs
off one of these.

**Gap 1 — the KG is authored as a graph but *read* as a list.** The GM builds typed
directional edges through a dropdown pair in a per-node card, and then can never see
the structure they built. There is no view in which "the thieves' guild has four
members, two of whom reside in the same district" is visible. Edges are write-only
in practice, which quietly discourages creating them — and edges are exactly what
`AgentNodeFacts` walks to build an NPC's Hot Context. Poor edge hygiene degrades
NPC quality invisibly.

**Gap 2 — the world has no space.** `resides_in` is the only spatial primitive, and
it is a pointer, not a position. "Which NPCs are in this city", "what is near the
party right now", "show me the tavern on the map" are all unanswerable. A TTRPG
world model without coordinates is missing the axis GMs think in most.

**Gap 3 — `gm_private` is all-or-nothing, per node.** A node is entirely visible to
NPCs or entirely hidden. In practice every interesting NPC has public facts ("runs
the Rusty Anchor, grumbles about the harbourmaster") and one secret ("took the
bribe"). Today the GM either leaks the secret into every prompt or splits the
character into two entries and loses the graph. This is the highest-friction thing
in the current model and it is invisible in the ask.

---

## 2. Track A — Graph visualization

### A1. The graph view (the core slice)

A second view mode on the existing Knowledge tab — `[ List | Graph ]` — rendering
the campaign's nodes and edges directly.

**Backend.** One new RPC, because 300 `ListNodeEdges` round-trips is not a plan:

```proto
rpc GetKnowledgeGraph(GetKnowledgeGraphRequest) returns (GetKnowledgeGraphResponse);
// nodes: id, type, name, gm_private, agent_id, body_len (not body — payload stays small)
// edges: id, from, to, type
```

Two indexed reads (`kg_node` by campaign, `kg_edge` by campaign) and no new tables.
A campaign is hundreds of nodes, not millions — no pagination, no server-side layout.

**Rendering.** SVG, not canvas: it stays inside the ADR-0017 token/CSS vocabulary,
it is inspectable in jsdom so the existing vitest suite can assert on it, and the
7-type colour palette already in `KnowledgePanel.tsx` transfers unchanged. Layout is
the only thing worth a dependency — `d3-force` (~30 KB, no transitive React) run for
a fixed tick count with a seeded initial placement, so the layout is a **pure,
deterministic function of (nodes, edges)** and therefore snapshot-testable. Writing
our own force simulation is ~150 lines and tempting, but d3-force's collision and
link-distance handling is the part that makes a 300-node graph legible.

**Interactions, in priority order:**

1. Click a node → opens the *existing* `EntryEditor` in the side rail. The graph is a
   navigation surface, not a second editor.
2. Type-filter chips and relation-type filter chips (reuse `TYPE_META` / `EDGE_TYPES`).
3. **Focus mode** — click a node, see only its ego network at depth 1–2. Depth 1 is
   deliberately identical to what `AgentNodeFacts` walks (see A2).
4. Drag from one node to another → the relation picker → `CreateEdge`. Building the
   graph *on* the graph is the thing that fixes Gap 1's authoring friction.
5. `gm_private` nodes rendered with a dashed stroke, and a "table view" toggle that
   hides them entirely so the GM can see the world as their NPCs see it.

**Size:** medium. One RPC + one new panel + one dep. The riskiest part is legibility
at 200+ nodes, which the filters and focus mode exist to answer.

### A2. The "what does Bart actually know?" lens

Pick a Character NPC from the roster; the graph dims to exactly the subgraph
`kgfacts` would inject for it — its linked node plus single-hop public neighbours —
with the `gm_private` neighbours drawn as struck-through ghosts labelled "hidden
from Bart", and a live "≈1,840 / 4,000 chars, 12 / 20 facts" budget readout.

This is the cheapest slice with the highest payoff, because it makes an invisible
system visible. Today the only way to know why an NPC didn't mention something is to
reason about `AgentNodeFacts` from memory. It also turns the truncation rules in
`renderFacts` (deterministic prefix-stop at `MaxBlockChars`) into something a GM can
see and act on instead of a silent quality cliff.

**Backend:** reuse `PromptKGView.AgentNodeFacts` behind a read-only
`GetAgentFactPreview` RPC; the render path is already `renderFacts`. **Size:** small,
once A1 exists.

### A3. World health panel

Pure derivations off the same graph payload, no new storage:

- **Orphans** — nodes with zero edges. They can never enter an NPC's context through
  a neighbour walk; they are only reachable if the node *is* an agent's own node.
- **Unlinked NPC nodes** — `node_type = npc AND agent_id IS NULL`, and the inverse:
  cast Agents whose linked node body is still empty (the ADR-0008 second-amendment
  auto-node starts empty, so this is the common case after adding an NPC).
- **Dangling plot threads** — `plot_thread` nodes with no incoming `participated_in`.
- **Probable duplicates** — `ListSimilarKnowledge` already exists (HNSW cosine over
  `kg_node.embedding`); run it pairwise over the campaign and surface pairs above a
  threshold. No auto-merge — ADR-0052 rejected that for proposals and the reasoning
  holds here.

**Size:** small. Mostly frontend, one optional batch-similarity RPC.

### A4. Proposals on the graph

Pending Knowledge Proposals (ADR-0052) drawn as dashed ghost nodes and edges *in
place* — the GM sees where a proposed fact would land in the world before approving
it, instead of judging it as a naked sentence in `ProposalsPanel`. Approve/reject
inline. **Size:** small after A1; it is the review UX that proposals deserved.

---

## 3. Track B — World maps and localization

### B1. Maps and pins as first-class campaign data

Two tables. The image lives in the blob seam (ADR-0048), never in the row.

```sql
CREATE TABLE campaign_map (
    id, campaign_id, name,
    blob_key text NOT NULL,          -- t/<tenant>/map/<map_id>/image
    width_px, height_px int NOT NULL,
    parent_map_id uuid NULL,         -- nesting: continent → region → city → building
    anchor_node_id uuid NULL,        -- the Location Node this map depicts
    gm_private boolean NOT NULL DEFAULT false,
    ...
);

CREATE TABLE map_pin (
    id, map_id, campaign_id,
    node_id uuid NOT NULL,           -- composite FK to kg_node(id, campaign_id)
    x double precision NOT NULL,     -- normalized 0..1, resolution-independent
    y double precision NOT NULL,
    label_override text NOT NULL DEFAULT '',
    gm_private boolean NOT NULL DEFAULT false,
    UNIQUE (map_id, node_id)
);
```

Two decisions worth stating explicitly:

- **A pin always references a KG Node.** Pins are not a parallel content model —
  they are a *position* attached to something the wiki already knows. This is what
  makes "localize objects, NPCs or places" fall out for free, and it keeps one source
  of truth. If a GM wants a pin for something with no entry, the flow creates a Node.
- **Normalized coordinates.** Storing 0..1 means a re-uploaded or rescaled map image
  keeps every pin.

Nesting via `parent_map_id` + `anchor_node_id` gives the hierarchy GMs actually
draw: a pin on the world map whose Location node anchors a city map, whose pin
anchors a tavern's floor plan. Breadcrumb navigation follows the chain.

**UI.** A new Campaign tab, `Maps`. Pan/zoom is a CSS `transform` on a wrapper — no
mapping library, no tiles. Pins are absolutely-positioned buttons; click opens the
node card; drag repositions (GM only); an "unpinned entries" tray lists Location/
NPC/Item nodes with no pin on this map so placing the world is a drag, not a form.
GM-private pins and maps never reach a Linked Player view (ADR-0056 access levels).

**Size:** medium-large — it is the only track that adds real storage, an upload path,
and a new screen.

### B2. Spatial questions as a Tool, not as Hot Context

The tempting move is to stuff "nearby places" into every NPC prompt. Don't. The
facts block is budgeted at 4000 chars and already competes with the neighbour walk,
and `kgfacts` is deliberately one indexed read per turn (sub-ms, no cache, so a
`gm_private` flip takes effect on the next turn). Proximity queries do not belong on
that path.

Instead, a **read-only built-in Tool** (`pkg/tool`, ADR-0028; read-only runs inline
and is unaffected by ADR-0030's turn-commit refusal):

- `locate_entity(name)` → which map, which region, what it is near.
- `whats_nearby(radius)` → pins within a radius of the party marker (B3).

Granted to the Butler campaign-wide by default; grantable to a Character NPC
narrowed to its own map by the ADR-0029 `SupportsScope` mechanism — an innkeeper
knowing the town's layout is correct, the same innkeeper knowing the enemy capital's
is not.

**Size:** small, and it is the correct seam.

### B3. The party marker

A per-Voice-Session current location: a `voice_session.current_map_id` +
`current_pin_id` (or a free x/y), moved by the GM on the Maps tab or by a `/where`
slash command (ADR-0010 surface).

This unlocks the one piece of map data that *does* earn a place in a prompt: a single
pinned line — "You are in the Rusty Anchor, in Saltmarsh's harbour district" — for
NPCs whose linked node is pinned nearby. It goes in the **volatile Hot Context tail**
(ADR-0059), alongside the per-turn facts and GM Directive blocks, never in the
cache-stable prefix. One short clause, bounded, replaceable per turn — exactly the
shape ADR-0059 built that tail for.

It also gives the Session screen something worth showing between turns, and gives
Highlights (ADR-0051) a location stamp.

**Size:** small-medium, gated on B1.

### B4. Generated maps

`internal/imagegen` already turns a prompt into blob-stored image bytes for Highlight
enrichment. Point it at maps:

- **`GenerateMap(prompt)`** → a draft image the GM previews and either saves as a
  `campaign_map` or discards — the exact review-before-write shape
  `GenerateKnowledge` / `ApplyGeneratedKnowledge` already established in
  `KnowledgeDraftCard`.
- **"Generate a map for this Location"** — seeds the prompt from the node's body and
  its `resides_in` neighbours, so the picture matches the wiki.
- **Suggested pins** — after upload or generation, ask the LLM for named places in the
  prose and offer them as unplaced draft pins the GM drags into position. Suggestion
  only; the GM places. (Coordinates from an image model are not trustworthy, and
  pretending otherwise would put garbage in the spatial layer.)

Note the cost seam: image generation bills through the LLM usage sink
(`internal/spend`), so map generation needs the same spend-cap treatment Highlight
enrichment has. **Size:** small on top of B1.

---

## 4. Track C — the organization work underneath the ask

### C1. Per-aspect visibility (fixes Gap 3) — the one I would build first

Split a Node's prose into optional **aspects**: ordered `(key, value, gm_private)`
rows in a `kg_node_aspect` table, with `body` retained as the free-form remainder so
nothing migrates and nothing breaks.

```
Bart the innkeeper  [NPC]
  Role      Runs the Rusty Anchor                    (public)
  Manner    Grumbles about the harbourmaster         (public)
  Secret    Took the smugglers' bribe in Eastmonth   (GM private)
```

`renderFact` composes public aspects into the fact; private ones never leave the GM
view. That is the difference between "hide the whole character from every prompt"
and "the innkeeper is himself, minus the one thing he'd never say."

It also improves proposal review: an Agent proposing a fact proposes an *aspect*, so
approve means "append this row", not "the GM rewrites a prose blob by hand."

**Size:** medium. One table, changes in `renderFacts`/`renderFact`, the editor, and
the ADR-0052 fact-proposal shape (`kgvocab` `KindFact` payload). High payoff — it is
the constraint every other feature keeps bumping into.

### C2. Tags and saved views

`kg_node_tag (node_id, tag)` — free-form GM labels alongside the closed 7-type
vocabulary. The types are a *schema* (they carry edge validity rules and prompt
semantics, so keeping them closed is right); tags are *organization*, and organization
should not require a migration. Chips filter both the list and the graph.

On top: **session prep boards** — a named saved set of nodes ("Tonight: the harbour
heist") surfaced as a sidebar on the Session screen. GMs already do this in a text
file. **Size:** small.

### C3. Roster organization

The Campaign roster is a flat list of Agents. Make it a prep dashboard: group cast
NPCs by their linked node's faction/location neighbours, and show per-NPC readiness —
persona written · voice set · node linked · node body non-empty · N facts in reach ·
last spoke. Every one of those is already queryable; none of it is shown. The
"created an NPC, its auto-node is empty, it has nothing to say" failure mode
(ADR-0008 second amendment) becomes visible instead of being discovered live at the
table. **Size:** small.

### C4. Appearances — the temporal layer, cheaply (ADR-0008 v2.x, first step)

"When did we last see that ogre" needs far less than a full event model. On transcript
commit, match committed line text against node names (the same phonetic/fuzzy
machinery `pkg/voice/address` already owns) and record `node_appearance (node_id,
session_id, line_id, at)`. Then each node grows an **Appearances** list: which
sessions, when, jump straight to the transcript line.

Deliberately *not* an auto `mentioned_in` edge — that would pollute the graph and the
neighbour walk with noise. Appearances are a separate index; if the GM wants a
`mentioned_in` edge, they promote one. **Size:** medium. This is the honest v2.x
on-ramp: retrieval first, event modelling only if retrieval proves insufficient.

### C5. Edge notes and disposition

`kg_edge.note text` + `disposition smallint` (−2..+2). `knows` today carries no
texture; "knows and despises" versus "knows and owes money to" is the difference
between a flat NPC and a live one. Renders as edge colour/thickness in the graph and
as one short clause in the fact block ("You resent Mira Vance"). Strictly one clause
— it costs fact-block budget. **Size:** small.

### C6. Bundle and converter follow-through

Maps, pins, tags, aspects, and appearances all need Campaign Bundle (ADR-0053)
coverage or export silently drops the new world. Worth noting that the map+pin model
is also the natural target for the Foundry VTT / Roll20 converter spike (#289) —
both tools export scenes with wall/token coordinates, and B1's normalized-coordinate
pin model maps onto them directly. Sequencing B1 before #289 means the converter has
somewhere to land.

---

## 5. Suggested sequencing

| # | Slice | Size | Depends on | Why here |
|---|---|---|---|---|
| 1 | **C1** per-aspect visibility | M | — | Unblocks Gap 3; every later feature is better with it |
| 2 | **A1** graph view | M | — | The headline ask; independent of C1 |
| 3 | **A2** agent-knowledge lens | S | A1 | Cheapest high-value slice in the whole note |
| 4 | **A3** world health + **C3** roster readiness | S | A1 | Same derived-data shape, ship together |
| 5 | **B1** maps + pins | M/L | — | The second headline ask; the only large storage add |
| 6 | **B3** party marker + **B2** locate tool | S/M | B1 | Makes maps matter during play, not just in prep |
| 7 | **B4** generated maps | S | B1 | Reuses the draft-review pattern verbatim |
| 8 | **A4** proposals on the graph, **C2** tags, **C5** edge notes | S each | A1 | Polish tier |
| 9 | **C4** appearances | M | — | The v2.x on-ramp; independent, can move earlier |

If only two things ship: **A2** (see what an NPC knows) and **C1** (per-aspect
visibility). They fix the two places where the current model actively costs quality
at the table.

## 6. Decisions each track forces

**ADR amendments needed**

- **ADR-0008** — "Form-based UI; no graph viz" must be amended for A1. Also the home
  for the aspect model (C1), edge notes (C5), and the appearances index (C4, which
  partially answers the ADR's own v2.x line).
- **ADR-0017 / ADR-0013** — `d3-force` is the first non-Radix, non-icon frontend
  dependency. Small, but it is a posture change worth one paragraph.
- **ADR-0048** — map images are the second blob owner after Highlights; confirm the
  32 MiB cap suits map scans (it does; a 4000px PNG is well under).
- **ADR-0059** — B3's location line is a new volatile-tail block. Must stay in the
  tail, must stay bounded.
- **ADR-0052 / `pkg/kgvocab`** — C1 changes the fact-proposal payload shape; the
  `ProposalWriteVersion` bump path exists for exactly this.
- **ADR-0053** — bundle coverage for every new table (C6).
- **ADR-0029** — the `whats_nearby` grant narrowing (B2).

**New CONTEXT.md terms** (with their aliases-to-avoid, per the glossary's own rules)

- **Map** — a campaign-scoped image with pinned KG Nodes, optionally nested under a
  parent Map. *Avoid:* Scene, Board, Battlemap (a Map here is not a tactical grid).
- **Pin** — a positioned reference from a Map to a KG Node. *Avoid:* Marker (reserved
  for the Party Marker), Token, Location (reserved for the Node type).
- **Party Marker** — the Voice-Session-scoped current position. *Avoid:* Position,
  Cursor.
- **Aspect** — a named, individually-visibility-scoped fact row on a Node. *Avoid:*
  Field, Attribute, Property, Trait.
- **Appearance** — a recorded mention of a Node in a Transcript Line. *Avoid:*
  Mention (collides with the `mentioned_in` Edge type), Sighting, Event.

## 7. Considered and not proposed

- **A graph database.** ADR-0008's reasoning stands and gets stronger: everything
  here is one or two hops over a few hundred rows. The layout problem is a frontend
  problem.
- **Coordinates on `kg_node` directly.** Would tie a Node to exactly one Map. A
  character appears on the city map *and* the tavern floor plan; the join table is
  the honest model.
- **Auto-placing pins from LLM output.** Suggest, never place — see B4.
- **Auto-merging duplicate nodes from embedding similarity.** ADR-0052 rejected this
  for proposals for the right reason; a wrong merge corrupts canon invisibly.
- **A hex/square tactical grid with movement.** That is a VTT. Glyphoxa is a
  voice-and-knowledge platform; the map exists to *localize the world model*, not to
  run combat. Converters to real VTTs (#289) are the answer to that want.
- **Streaming the whole KG to every NPC prompt.** The neighbour walk and its budget
  exist because unbounded context is worse context. Nothing here relaxes it.
