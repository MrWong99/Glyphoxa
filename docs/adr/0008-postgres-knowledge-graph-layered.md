# Postgres-backed knowledge graph, layered v1.0 → v2.x

The Knowledge Graph lives in Postgres tables, not a graph database. Roadmap is layered:

- **v1.0** — Structured wiki / GM notes. Typed Nodes (`Character`, `NPC`, `Location`, `Faction`, `Item`, `PlotThread`, `Note`) and typed directional Edges (`resides_in`, `member_of`, `owns`, `knows`, `enemy_of`, `ally_of`, `parent_of`, `participated_in`, `mentioned_in`). `gm_private` flag for visibility. Form-based UI; no graph viz. Fulltext search (tsvector) only.
- **v1.5** — NPC memory backbone: KG queries during agent inference.
- **v2.x** — Story-state tracker with temporal/event modeling (the "when did we last see that ogre" feature).

**Why:** a dedicated graph DB adds operational surface that the v1.0 wiki feature doesn't need. Postgres handles typed nodes/edges with foreign keys; tsvector covers v1.0 search; the v2.x temporal layer can add specialised storage if it becomes necessary.
