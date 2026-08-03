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

Tags remain the *other* axis and are deliberately not this: the seven Node types stay a closed schema carrying edge-validity rules, Aspects carry content, and tags (#543) carry organization.
