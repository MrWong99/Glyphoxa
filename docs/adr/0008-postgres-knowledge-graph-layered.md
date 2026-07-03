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
