# Postgres-backed knowledge graph, layered v1.0 → v2.x

The Knowledge Graph lives in Postgres tables, not a graph database. Roadmap is layered:

- **v1.0** — Structured wiki / GM notes. Typed Nodes (`Character`, `NPC`, `Location`, `Faction`, `Item`, `PlotThread`, `Note`) and typed directional Edges (`resides_in`, `member_of`, `owns`, `knows`, `enemy_of`, `ally_of`, `parent_of`, `participated_in`, `mentioned_in`). `gm_private` flag for visibility. Form-based UI; no graph viz. Fulltext search (tsvector) only.
- **v1.5** — NPC memory backbone: KG queries during agent inference.
- **v2.x** — Story-state tracker with temporal/event modeling (the "when did we last see that ogre" feature).

**Why:** a dedicated graph DB adds operational surface that the v1.0 wiki feature doesn't need. Postgres handles typed nodes/edges with foreign keys; tsvector covers v1.0 search; the v2.x temporal layer can add specialised storage if it becomes necessary.

## Amendment: Edge semantics and the NPC-Node ↔ Agent link (2026-07-04, #132)

- **Storage:** one `kg_edge` table `(from_node_id, to_node_id, edge_type)` with `UNIQUE(from, to, type)` and `ON DELETE CASCADE` from both node FKs. The same-campaign constraint is declarative: nodes expose `UNIQUE(id, campaign_id)` and edges carry `campaign_id` in composite FKs to both endpoints — no trigger.
- **Validity: object-side-only.** Structural edge types enforce their *target* (and for `parent_of` both ends): `resides_in` → Location, `member_of` → Faction, `participated_in` → PlotThread, `parent_of` → Character/NPC on both sides. The subject side and the social/loose types (`knows`, `owns`, `enemy_of`, `ally_of`, `mentioned_in`) are unconstrained — TTRPG worlds legitimately contain sentient swords that know kings and ghosts that reside in taverns. A full from/to matrix was rejected as fighting the domain; no constraints at all was rejected as losing typo protection on the structural edges.
- **Strictly directional, no auto-inverse.** Every Edge is a one-way assertion; mutual relationships are two Edges. This keeps one-way social facts expressible (the spy knows the king; A secretly considers B an enemy). UIs list incoming and outgoing edges separately.
- **NPC-Node ↔ Character NPC Agent link:** nullable `kg_node.agent_id` — `UNIQUE`, `ON DELETE SET NULL`, `CHECK (node_type = 'NPC' OR agent_id IS NULL)` — linked manually from the Campaign screen. The wiki side carries the link so the polymorphic `agents` table (ADR-0009) stays untouched; Hot Context resolves Agent→Node via the unique index. Auto-creating a Node when a Character NPC is created was rejected: Node and Agent are deliberately separate records (CONTEXT.md), and wiki-only NPCs stay normal.
- **Visibility interaction:** `gm_private` filtering applies to *neighbour expansion* too — an Edge whose target Node is `gm_private` must not surface that Node into an NPC's Hot Context, even though the Edge itself exists.

## Second amendment: Character-NPC auto-node (2026-07-19, #479) — supersedes the 2026-07-04 rejection

The 2026-07-04 amendment rejected auto-creating a Node when a Character NPC is created. **Reversed by explicit GM/operator request (#479):** in practice every voiced NPC also wants a wiki entry, and creating it by hand was pure friction.

- **On Character-NPC create** (`CreateAgent`), an NPC Node named after the Agent is created **in the same transaction**, carrying the `agent_id` "voiced by" link. The Node starts with an empty body and `gm_private = false` — the Persona is never copied (Persona is how the character speaks; the Node body is what the world knows).
- **Narrow name-follow, no general sync.** While the Agent's and the linked Node's names still match, renaming the Agent renames the Node too (the "New NPC" placeholder flow renames right after create). Once the GM renames the Node independently the names diverge and the follow stops for good. Bodies/personas are **never** synced — the CONTEXT.md "separate records that may drift" posture stands for content.
- **Everything else is unchanged:** wiki-only NPCs (Node without Agent) stay normal, the link stays manually editable (`SetNodeAgent`), and deleting the Agent leaves the Node as a wiki-only entry (`ON DELETE SET NULL`).

## Third amendment: per-Aspect Node visibility (2026-08-03, #542)

The v1.0 line above gives a Node ONE `gm_private` flag. In practice every interesting NPC has public facts and one secret, so the GM had two bad options: leak the secret into every prompt, or split the character into two entries and lose the graph. Both are worse than the feature they work around, and every later slice of the #533 epic kept bumping into the same constraint.

- **Aspects.** A Node gains an ordered list of `kg_node_aspect (key, value, gm_private)` rows. Visibility is per fact, so a public innkeeper keeps their public role and loses only their bribe. The Node's own `gm_private` is unchanged and still hides the whole entry.
- **`kg_node.body` is retained** as the free-form remainder. Nothing migrates, every existing entry keeps working, and an entry with no Aspects renders byte-identically to before.
- **Enforced at the seam, not the call site.** The private-Aspect exclusion lives in the `PromptKGView` reads' SQL — the same place the whole-Node `gm_private` filter lives (#450) — because the whole-Node filter cannot protect a private fact on a public Node. Pinned by `TestPromptKG_NeverReturnsPrivateAspects` alongside the existing seam test.
- **One bound, not two.** `kgfacts.renderFact` composes header + public Aspects + body and truncates the COMPOSED content to `MaxFactChars`, so a Node with fifty Aspects consumes exactly the budget a Node with a long body does. `MaxBlockChars` and the deterministic prefix-stop are untouched.
- **Aspects are the fact-proposal shape** (ADR-0052): a `kind=fact` proposal names an aspect key plus its value, and approving APPENDS a row instead of concatenating prose the GM must later pull apart by hand. `ProposalWriteVersion` is bumped to 2 for exactly this; a stored v1 row is refused as unreadable rather than reinterpreted under the new shape.
- **A proposed Aspect always lands public.** A secret is a GM authorship act, never an inference from play — an Agent proposes what its character would say out loud.
- **Aspects are part of the Node's embedded text** (private ones included). The vector feeds the GM-facing similarity hints only (ADR-0052), which never reach a prompt, and a secret invisible to the vector would make duplicate detection blind to exactly the facts GMs most want deduped.

**Budget split, not tail truncation.** Aspects compose before the body, so cutting the composed fact from the end deleted the GM's authored prose wholesale once the Aspects reached `MaxFactChars`. Each side is now guaranteed a share of the per-fact budget and donates whatever it does not use, so a one-line body costs the Aspects almost nothing while a long one cannot be evicted outright. A GM who wrote both meant both to reach the table.

**Deployment note.** Migration `00042` adds two generated `tsvector` columns and drops one, which rewrites `kg_node` — rebuilding its indexes, HNSW included. That is a one-time cost, paid once per environment, and it is the price of making Aspects searchable at all; it is called out here so it is not a surprise on a large campaign. Per ADR-0031 the migration is not edited after the fact.

Tags remain the *other* axis and are deliberately not this: the seven Node types stay a closed schema carrying edge-validity rules, Aspects carry content, and tags (#543) carry organization.

## Fourth amendment: the graph is rendered (2026-08-03, #534) — supersedes "no graph viz"

The v1.0 line above scoped the KG as a "structured wiki / GM notes. Form-based UI; **no graph viz**." That was a correct v1.0 cut — a picture of an empty wiki is worth nothing — but it has outlived its reason, and it is reversed here explicitly rather than silently.

What changed is that Edges stopped being decoration. `AgentNodeFacts` walks them every turn to fill an NPC's Hot Context (the 2026-07-04 amendment), so edge hygiene is now a *quality* input to every NPC. Meanwhile Edges were authorable only through a dropdown pair inside a per-node card and were then **never displayed anywhere** — so the GM could not see the structure they had built, could not spot an error in it, and got no reward for building it. An invisible input to prompt quality is the worst kind.

- **One new read**, `GetKnowledgeGraph`: every Node and Edge for the Campaign in one call, two indexed reads, no new tables. 300 per-node `ListNodeEdges` round trips is not a plan. The payload deliberately carries **no prose** — `body_len` and `aspect_count` instead of the text — because no node glyph renders a body.
- **GM-facing, unlike every prompt read.** `gm_private` rows are returned and the client decides how to draw them (dashed, or absent in "table view"). This is the exact opposite of `PromptKGView` (#450) and the difference is the point: the graph is the GM's map of their own world.
- **The graph is a navigation surface, not a second editor.** Clicking a node opens the existing `EntryEditor`. The one thing authored on the graph — drag one node onto another — goes through the existing `CreateEdge` RPC, validity matrix included.
- **Layout is a pure deterministic function of (nodes, edges)**, seeded and run for a fixed tick count. Not for testability first: a graph that reshuffles on every open destroys the spatial memory that makes it useful at all.
- Still **no graph database**. Everything here is one or two hops over a few hundred rows; the original reasoning holds and strengthens.

The v2.x temporal line is untouched by this amendment.

## Fifth amendment: Appearances are an index, not Edges (2026-08-04, #545)

"When did we last see that ogre" is the question a GM asks constantly and the KG cannot answer. A Node records what a thing IS; nothing records where it came up.

The obvious modelling answer is an Edge — `Node -mentioned_in-> Session`. It is the wrong one, twice:

- **An Edge is world truth an NPC recites.** `AgentNodeFacts` walks Edges every turn to fill an NPC's Hot Context (the 2026-07-04 amendment). A mention modelled as an Edge would make "the party talked about the ogre" into "the ogre is connected to the party" in the NPC's head — a fact nobody wrote, spoken back at the table. The edge-type vocabulary is closed for exactly this reason, and widening it here would be widening it for the one case that must not be in it.
- **The volumes are wrong.** Edges are a few hundred hand-authored rows the GM curates. Mentions are thousands of derived rows per campaign, machine-written, never reviewed. Putting them in the same table would drown the curated set that every NPC prompt walks.

So an Appearance is a **retrieval index that lives beside the graph**: `node_appearance(node_id, campaign_id, voice_session_id, line_id, at)`, read by one GM-facing RPC and by nothing in prompt assembly. `PromptKGView` does not gain a method.

- **Derived, never authored.** One durable job per ended Voice Session (ADR-0049) matches the Campaign's entry names against its committed Transcript Lines. Nothing runs on a voice turn; the session-end path only enqueues.
- **Committed lines only**, which needs no filter: a partial never reaches `transcript_line` (ADR-0012/ADR-0040).
- **One matcher, not two.** Matching reuses `pkg/voice/address`'s fuzzy index through a thin exported wrapper, so the Campaign Language's phonetic encoder applies and "was this named" has ONE definition. The confidence bar is deliberately stricter than the live Address Detector's: a live mis-detection costs one NPC answering when it should not have, which the GM hears and shrugs off, while a mis-detection here writes a durable row nobody reviews.
- **Keyed on `(voice_session_id, line_id)`**, the relay's own stable Line key, which `transcript_line` already declares UNIQUE. Not on a surrogate id: `transcript_line.id` exists in SQL but is unreachable from Go, and its insert/scan column lists are order-coupled, so reaching for it would touch every caller of a load-bearing table for no gain.
- **Retraction cascades.** The composite FK to `transcript_line` is `ON DELETE CASCADE`, so a retracted line (#437, ADR-0040) takes its appearances with it. A GM who unsays something at the table must not find it still listed under an entry.
- **Idempotent.** The write is `ON CONFLICT DO NOTHING`, so a retried, replayed or manually re-run job produces the same rows and changes nothing.
- **`gm_private` entries are indexed and shown.** This is the operator's own retrieval over their own campaign (ADR-0039/0041); hiding a GM's secret from the GM's own search would make the index lie about their world. Nothing here is player- or prompt-facing.

The v2.x temporal line is still untouched: this records WHERE something was said, not a timeline of what became true when.
