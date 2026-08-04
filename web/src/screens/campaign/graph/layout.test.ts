import { describe, it, expect } from "vitest";
import { create } from "@bufbuild/protobuf";

import {
  EdgeType,
  GraphEdgeSchema,
  GraphNodeSchema,
  NodeType,
} from "@gen/glyphoxa/management/v1/management_pb";
import { LAYOUT, egoNetwork, filterGraph, layout, seededRandom } from "./layout";

// A small fixed world: an innkeeper in a town, a faction he belongs to, a secret
// the GM keeps, and one entry with no relationships at all.
function fixture() {
  const nodes = [
    create(GraphNodeSchema, { id: "bart", nodeType: NodeType.NPC, name: "Bart" }),
    create(GraphNodeSchema, { id: "town", nodeType: NodeType.LOCATION, name: "Saltmarsh" }),
    create(GraphNodeSchema, { id: "guild", nodeType: NodeType.FACTION, name: "Smugglers" }),
    create(GraphNodeSchema, {
      id: "secret",
      nodeType: NodeType.NOTE,
      name: "The bribe",
      gmPrivate: true,
    }),
    create(GraphNodeSchema, { id: "lonely", nodeType: NodeType.ITEM, name: "A lost ring" }),
  ];
  const edges = [
    create(GraphEdgeSchema, {
      id: "e1",
      fromNodeId: "bart",
      toNodeId: "town",
      edgeType: EdgeType.RESIDES_IN,
    }),
    create(GraphEdgeSchema, {
      id: "e2",
      fromNodeId: "bart",
      toNodeId: "guild",
      edgeType: EdgeType.MEMBER_OF,
    }),
    create(GraphEdgeSchema, {
      id: "e3",
      fromNodeId: "guild",
      toNodeId: "secret",
      edgeType: EdgeType.KNOWS,
    }),
  ];
  return { nodes, edges };
}

describe("graph layout", () => {
  // The load-bearing property of this module: the same payload must always give
  // the same picture. Without it a GM's mental map of their world is reshuffled
  // every time they open the tab, and none of this view is snapshot-testable.
  it("is a deterministic pure function of (nodes, edges)", () => {
    const { nodes, edges } = fixture();
    const a = layout(nodes, edges);
    const b = layout(nodes, edges);
    expect(a.nodes.map((p) => [p.node.id, p.x, p.y])).toEqual(
      b.nodes.map((p) => [p.node.id, p.x, p.y]),
    );
    expect(a.bounds).toEqual(b.bounds);
  });

  it("pins the laid-out coordinates", () => {
    const { nodes, edges } = fixture();
    const laid = layout(nodes, edges);
    expect(laid.nodes.map((p) => `${p.node.id}@${p.x},${p.y}`)).toMatchSnapshot();
  });

  // Determinism must come from the seeded source, not from luck: a different seed
  // has to move the picture, or the "seeded" claim is untested.
  it("uses a seeded random source, not Math.random", () => {
    const first = seededRandom(1);
    const same = seededRandom(1);
    const other = seededRandom(2);
    const draw = (r: () => number) => [r(), r(), r()];
    expect(draw(first)).toEqual(draw(same));
    expect(draw(seededRandom(1))).not.toEqual(draw(other));
  });

  it("places every node and connects every resolvable edge", () => {
    const { nodes, edges } = fixture();
    const laid = layout(nodes, edges);
    expect(laid.nodes).toHaveLength(5);
    expect(laid.edges).toHaveLength(3);
    for (const p of laid.nodes) {
      expect(Number.isFinite(p.x) && Number.isFinite(p.y)).toBe(true);
    }
  });

  // Filter chips legitimately produce edges whose endpoints are gone; d3-force
  // throws on an unresolvable link id, so they must be dropped, not passed through.
  it("drops edges whose endpoints were filtered out instead of throwing", () => {
    const { nodes, edges } = fixture();
    const laid = layout(nodes.slice(0, 1), edges);
    expect(laid.nodes).toHaveLength(1);
    expect(laid.edges).toHaveLength(0);
  });

  // A one-node campaign has a bounding box of near-zero area; without a floor the
  // viewBox magnifies that single glyph to fill the canvas — a circle the size of
  // a dinner plate with its label running off the edge.
  it("keeps a tiny graph from being magnified to fill the canvas", () => {
    const { nodes } = fixture();
    const laid = layout(nodes.slice(0, 1), []);
    const width = laid.bounds.maxX - laid.bounds.minX;
    const height = laid.bounds.maxY - laid.bounds.minY;
    expect(width).toBeGreaterThanOrEqual(LAYOUT.minExtent);
    expect(height).toBeGreaterThanOrEqual(LAYOUT.minExtent);
    // Still centred on the node it is showing.
    expect((laid.bounds.minX + laid.bounds.maxX) / 2).toBeCloseTo(laid.nodes[0].x, 0);
  });

  it("handles an empty campaign", () => {
    const laid = layout([], []);
    expect(laid.nodes).toHaveLength(0);
    expect(laid.bounds).toEqual({ minX: 0, minY: 0, maxX: 0, maxY: 0 });
  });
});

describe("egoNetwork", () => {
  // Depth 1 is deliberately the same walk AgentNodeFacts performs, so "focus at
  // depth 1" answers "what can this entry's NPC actually see".
  it("walks both edge directions at depth 1", () => {
    const { edges } = fixture();
    expect([...egoNetwork("bart", edges, 1)].sort()).toEqual(["bart", "guild", "town"]);
    // Incoming counts too: the guild is reached FROM bart, and secret FROM guild.
    expect([...egoNetwork("secret", edges, 1)].sort()).toEqual(["guild", "secret"]);
  });

  it("expands to two hops at depth 2", () => {
    const { edges } = fixture();
    expect([...egoNetwork("bart", edges, 2)].sort()).toEqual(["bart", "guild", "secret", "town"]);
  });

  it("returns just the root for an unconnected node", () => {
    const { edges } = fixture();
    expect([...egoNetwork("lonely", edges, 2)]).toEqual(["lonely"]);
  });
});

describe("filterGraph", () => {
  const allTypes = new Set([
    NodeType.NPC,
    NodeType.LOCATION,
    NodeType.FACTION,
    NodeType.NOTE,
    NodeType.ITEM,
  ]);
  const allRelations = new Set([EdgeType.RESIDES_IN, EdgeType.MEMBER_OF, EdgeType.KNOWS]);

  it("keeps everything when nothing is filtered", () => {
    const { nodes, edges } = fixture();
    const out = filterGraph(nodes, edges, {
      types: allTypes,
      relations: allRelations,
      hidePrivate: false,
      focus: null,
    });
    expect(out.nodes).toHaveLength(5);
    expect(out.edges).toHaveLength(3);
  });

  // Table view is "see the world as your NPCs see it": a gm_private entry is not
  // dimmed, it is absent — and so is every edge that touched it.
  it("table view removes gm_private nodes and their edges entirely", () => {
    const { nodes, edges } = fixture();
    const out = filterGraph(nodes, edges, {
      types: allTypes,
      relations: allRelations,
      hidePrivate: true,
      focus: null,
    });
    expect(out.nodes.map((n) => n.id)).not.toContain("secret");
    expect(out.edges.map((e) => e.id)).not.toContain("e3");
  });

  it("a type chip drops that type and any edge that needed it", () => {
    const { nodes, edges } = fixture();
    const types = new Set(allTypes);
    types.delete(NodeType.FACTION);
    const out = filterGraph(nodes, edges, {
      types,
      relations: allRelations,
      hidePrivate: false,
      focus: null,
    });
    expect(out.nodes.map((n) => n.id)).not.toContain("guild");
    expect(out.edges.map((e) => e.id)).toEqual(["e1"]);
  });

  it("a relation chip hides only the relation, keeping its endpoints", () => {
    const { nodes, edges } = fixture();
    const relations = new Set(allRelations);
    relations.delete(EdgeType.MEMBER_OF);
    const out = filterGraph(nodes, edges, {
      types: allTypes,
      relations,
      hidePrivate: false,
      focus: null,
    });
    expect(out.nodes).toHaveLength(5);
    expect(out.edges.map((e) => e.id)).toEqual(["e1", "e3"]);
  });

  it("focus restricts to the ego network", () => {
    const { nodes, edges } = fixture();
    const out = filterGraph(nodes, edges, {
      types: allTypes,
      relations: allRelations,
      hidePrivate: false,
      focus: egoNetwork("bart", edges, 1),
    });
    expect(out.nodes.map((n) => n.id).sort()).toEqual(["bart", "guild", "town"]);
  });
});

// Position memory (#537). `layout` is pure in its INPUTS, so adding one Node
// reshuffles every other Node — which is exactly what happens when the GM
// approves a suggestion mid-review. Determinism is not stability.
describe("layout position memory", () => {
  it("pinned nodes come back at exactly their pinned coordinates", () => {
    const { nodes, edges } = fixture();
    const first = layout(nodes, edges);
    const pins = new Map(first.nodes.map((p) => [p.node.id, { x: p.x, y: p.y }]));

    // A new entry arrives — an approved suggestion, or any other edit.
    const grown = [
      ...nodes,
      create(GraphNodeSchema, { id: "new", nodeType: NodeType.NPC, name: "Mira" }),
    ];
    const second = layout(grown, edges, { pins });

    for (const before of first.nodes) {
      const after = second.nodes.find((p) => p.node.id === before.node.id);
      expect(after, `${before.node.id} vanished`).toBeDefined();
      expect([after!.x, after!.y], `${before.node.id} moved`).toEqual([before.x, before.y]);
    }
  });

  it("without pins, the same growth DOES reshuffle — which is why pins exist", () => {
    const { nodes, edges } = fixture();
    const first = layout(nodes, edges);
    const grown = [
      ...nodes,
      create(GraphNodeSchema, { id: "new", nodeType: NodeType.NPC, name: "Mira" }),
    ];
    const second = layout(grown, edges);

    const moved = first.nodes.some((before) => {
      const after = second.nodes.find((p) => p.node.id === before.node.id);
      return after && (after.x !== before.x || after.y !== before.y);
    });
    expect(moved, "the unpinned layout was stable by accident, so the pin test proves nothing").toBe(
      true,
    );
  });

  it("a fully pinned layout is idempotent", () => {
    const { nodes, edges } = fixture();
    const first = layout(nodes, edges);
    const pins = new Map(first.nodes.map((p) => [p.node.id, { x: p.x, y: p.y }]));
    const second = layout(nodes, edges, { pins });
    expect(second.nodes.map((p) => [p.node.id, p.x, p.y])).toEqual(
      first.nodes.map((p) => [p.node.id, p.x, p.y]),
    );
    expect(second.bounds).toEqual(first.bounds);
  });

  it("a seed places a brand-new node where its ghost stood", () => {
    const { nodes, edges } = fixture();
    const first = layout(nodes, edges);
    const pins = new Map(first.nodes.map((p) => [p.node.id, { x: p.x, y: p.y }]));
    // The server mints a fresh id at approval, so the NAME is the only thing that
    // survives the ghost→node transition.
    const seeds = new Map([["mira", { x: 400, y: -250 }]]);
    const grown = [
      ...nodes,
      create(GraphNodeSchema, { id: "fresh-uuid", nodeType: NodeType.NPC, name: "Mira" }),
    ];
    const out = layout(grown, edges, { pins, seeds });
    const mira = out.nodes.find((p) => p.node.id === "fresh-uuid")!;
    // EXACTLY where the ghost stood. "Near enough" is not what the GM watched
    // happen: they clicked a dashed circle and it should become a solid one, in
    // that spot, with nothing else moving.
    expect([mira.x, mira.y]).toEqual([400, -250]);
  });

  it("pinned nodes do not drift as the free node settles", () => {
    // forceCenter recentres by translating every node, and a pinned node is snapped
    // back each tick — so the offset never shrinks and the correction is re-applied
    // forever, walking the free nodes away. It must be off once anything is pinned.
    const { nodes, edges } = fixture();
    const pins = new Map(nodes.map((n, i) => [n.id, { x: 1000 + i * 60, y: 1000 }]));
    const grown = [
      ...nodes,
      create(GraphNodeSchema, { id: "new", nodeType: NodeType.NPC, name: "Mira" }),
    ];
    const out = layout(grown, edges, { pins });
    const fresh = out.nodes.find((p) => p.node.id === "new")!;
    expect(Number.isFinite(fresh.x)).toBe(true);
    // Sane means "in the same neighbourhood as the cloud", not "at ±1e12".
    expect(Math.abs(fresh.x - 1000)).toBeLessThan(2000);
    expect(Math.abs(fresh.y - 1000)).toBeLessThan(2000);
  });
});
