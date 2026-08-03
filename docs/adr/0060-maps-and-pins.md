# World Maps and Pins: positions for the Knowledge Graph, not a VTT

A Campaign gets **Maps** (an uploaded or generated image, nestable) and **Pins** (a normalized position on a Map, always referencing a KG Node). Two tables, `campaign_map` and `map_pin`; the image lives in the blob seam.

**Why:** `resides_in` is a pointer, not a position. "Which NPCs are in this city", "what is near the party", "show me the tavern" are all unanswerable today, and space is the axis GMs think in most. The Knowledge Graph already knows *what* exists and *how things relate*; it has no way to say *where*.

## Decisions

- **A Pin always references a KG Node.** Pins are not a parallel content model — they are a *position* attached to something the wiki already knows. That is what makes "localize objects, NPCs or places" fall out for free from one source of truth, and what keeps a Map from becoming a second, unvalidated place to write the world down. Pinning something with no entry creates the Node first.
- **Normalized 0..1 coordinates.** A re-uploaded or rescaled map image keeps every pin. Pixel coordinates would silently invalidate a GM's whole map the first time they found a better scan. `width_px`/`height_px` are kept for aspect-ratio rendering only, never for coordinate maths.
- **Same-campaign integrity is declarative.** `campaign_map` exposes `UNIQUE (id, campaign_id)` and `map_pin` carries `campaign_id` in composite FKs to *both* endpoints, so a Pin cannot span Campaigns — the same no-trigger pattern ADR-0008 established for `kg_edge`. The Map's `anchor_node_id` uses it too.
- **`UNIQUE (map_id, node_id)`, not unique per Node.** The same Node legitimately appears on the city map *and* the tavern floor plan.
- **Nesting via `parent_map_id` + `anchor_node_id`** gives the hierarchy GMs actually draw: a pin on the world map whose Location Node anchors a city map, whose pin anchors a tavern floor plan. Breadcrumbs follow the chain in both directions.
- **The image is a blob (ADR-0048), and Maps are its second owner after Highlights.** The row carries only `blob_key`; deletion goes through the seam, not FK cascade, so a dropped row is not what frees the bytes. The 32 MiB cap suits map scans comfortably — a 4000px PNG is well under it.
- **Deleting a Node removes its Pins.** A position with nothing at it is not information.
- **Visibility is per Map and per Pin, and it composes.** A `gm_private` Map or Pin never reaches a Linked Player view (ADR-0056), and a Pin whose *Node* is `gm_private` inherits that — mirroring ADR-0008's rule that gm_private filtering applies to neighbour expansion, not just to direct reads. Filtering happens in the read, not in the UI.

## Explicitly not this

**A hex or square tactical grid with movement.** That is a VTT. Glyphoxa is a voice-and-knowledge platform, and the Map exists to localize the world model — to make an NPC's "where" answerable — not to run combat. Converters to real VTTs (#289) are the answer to that want, and this normalized-coordinate Pin model is their natural landing target: both Foundry and Roll20 export scenes with coordinates.

Spatial questions reach Agents through a read-only **Tool** (#539), never by widening the per-turn prompt: the facts block is capped and already competes with the neighbour walk, so anything added there silently evicts world knowledge. The one exception is the Party Marker's single bounded clause in the ADR-0059 volatile tail (#540).

## Consequences

- The Campaign Bundle (ADR-0053) must carry Maps and Pins or an export silently stops being true; that is #547, and until it lands a bundle is lossy for this data.
- `campaign_map.parent_map_id` is self-referential and `ON DELETE SET NULL`, so deleting a middle map orphans its children into the top level rather than cascading a subtree away.
- Generated maps (#541) reuse this table through the existing draft-review flow; nothing is written until the GM applies.
