import {
  forceCenter,
  forceCollide,
  forceLink,
  forceManyBody,
  forceSimulation,
  type SimulationLinkDatum,
  type SimulationNodeDatum,
} from "d3-force";

import { EdgeType, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { GraphEdge, GraphNode } from "@gen/glyphoxa/management/v1/management_pb";

// Deterministic force layout for the Knowledge Graph view (#534, ADR-0008
// amendment; d3-force per the ADR-0017/ADR-0013 dependency note).
//
// The whole point of this module is that layout(nodes, edges) is a PURE function:
// the same payload always produces the same coordinates, byte for byte. That is
// what makes the view snapshot-testable, and — more importantly at the table —
// what stops a GM's mental map of their world from being reshuffled every time
// they open the tab.
//
// Two sources of nondeterminism exist in d3-force and both are removed here:
//
//   1. Initial placement. d3-force seeds unplaced nodes on a phyllotaxis spiral
//      already, but only after `nodes()` is called; we place them ourselves from
//      the node's INDEX so placement never depends on object identity or Map
//      iteration order.
//   2. `jiggle()`. d3-force perturbs coincident nodes with its random source. We
//      replace that source with a seeded LCG (`simulation.randomSource`), so the
//      perturbation is reproducible instead of merely small.
//
// The simulation is also run synchronously for a FIXED tick count (`simulation.stop()`
// then N manual ticks) rather than animated: an animated layout would be a
// different picture on a fast machine than a slow one, and a settling graph is
// harder to click than a settled one.

/** One laid-out node: the payload row plus its computed position. */
export type PositionedNode = {
  node: GraphNode;
  x: number;
  y: number;
};

/** One laid-out edge: the payload row plus both endpoints' positions. */
export type PositionedEdge = {
  edge: GraphEdge;
  x1: number;
  y1: number;
  x2: number;
  y2: number;
};

export type Layout = {
  nodes: PositionedNode[];
  edges: PositionedEdge[];
  /** The bounding box of the laid-out nodes, for the SVG viewBox. */
  bounds: { minX: number; minY: number; maxX: number; maxY: number };
};

/** Layout tuning. Exported so the view and its tests agree on one set of numbers. */
export const LAYOUT = {
  /** Fixed tick count. Enough for a few hundred nodes to settle; cheap enough to run inline. */
  ticks: 300,
  /** Radius of a node glyph, and the collision radius that keeps labels apart. */
  nodeRadius: 9,
  collideRadius: 34,
  /** Resting length of an edge. */
  linkDistance: 90,
  /** Node repulsion. Negative is repulsive in d3-force. */
  charge: -220,
  /** Initial spiral spacing, in px. */
  spiralSpacing: 26,
  /** Padding added around the bounding box so glyphs and labels are not clipped. */
  padding: 60,
  /**
   * Minimum view extent. A one- or two-node campaign has a bounding box near zero
   * area, and a viewBox fitted to it magnifies those few glyphs to fill the canvas
   * — a circle the size of a dinner plate with its label running off the edge. The
   * floor keeps a small graph looking like a small graph.
   */
  minExtent: 520,
} as const;

// simNode is the mutable datum d3-force writes x/y/vx/vy onto.
type SimNode = SimulationNodeDatum & { id: string; node: GraphNode };
type SimLink = SimulationLinkDatum<SimNode> & { edge: GraphEdge };

/**
 * seededRandom returns a deterministic [0,1) generator — a 32-bit LCG (Numerical
 * Recipes constants). d3-force only uses its random source for jiggling
 * coincident points, so the quality bar is "reproducible and non-degenerate",
 * not "cryptographic".
 */
export function seededRandom(seed = 0x2545f491): () => number {
  let state = seed >>> 0;
  return () => {
    state = (Math.imul(1664525, state) + 1013904223) >>> 0;
    return state / 0x100000000;
  };
}

/**
 * layout runs the force simulation to completion and returns positioned nodes and
 * edges. Pure: equal inputs give equal outputs, with no dependence on wall clock,
 * Math.random, or object identity.
 *
 * Edges referencing a node absent from `nodes` (a filtered-out endpoint) are
 * dropped rather than crashing d3-force's link resolution — the filter chips
 * legitimately produce that state on every render.
 */
export function layout(nodes: GraphNode[], edges: GraphEdge[]): Layout {
  if (nodes.length === 0) {
    return { nodes: [], edges: [], bounds: { minX: 0, minY: 0, maxX: 0, maxY: 0 } };
  }

  // Deterministic initial placement: a phyllotaxis spiral keyed on the node's
  // index in the (server-ordered) payload.
  const sim: SimNode[] = nodes.map((node, i) => {
    const angle = i * 2.399963229728653; // the golden angle, in radians
    const radius = LAYOUT.spiralSpacing * Math.sqrt(i);
    return {
      id: node.id,
      node,
      x: radius * Math.cos(angle),
      y: radius * Math.sin(angle),
      vx: 0,
      vy: 0,
    };
  });

  const byID = new Map(sim.map((n) => [n.id, n]));
  const links: SimLink[] = [];
  for (const edge of edges) {
    const source = byID.get(edge.fromNodeId);
    const target = byID.get(edge.toNodeId);
    if (!source || !target) continue;
    links.push({ edge, source, target });
  }

  const simulation = forceSimulation(sim)
    .randomSource(seededRandom())
    .force(
      "link",
      forceLink<SimNode, SimLink>(links)
        .id((d) => d.id)
        .distance(LAYOUT.linkDistance),
    )
    .force("charge", forceManyBody().strength(LAYOUT.charge))
    .force("collide", forceCollide(LAYOUT.collideRadius))
    .force("center", forceCenter(0, 0))
    .stop();

  simulation.tick(LAYOUT.ticks);

  // Round to whole pixels. Sub-pixel float noise would make an otherwise identical
  // layout produce a different snapshot across platforms, and no glyph is placed
  // more precisely than a pixel anyway.
  const positioned: PositionedNode[] = sim.map((s) => ({
    node: s.node,
    x: Math.round(s.x ?? 0),
    y: Math.round(s.y ?? 0),
  }));
  const posByID = new Map(positioned.map((p) => [p.node.id, p]));

  const positionedEdges: PositionedEdge[] = [];
  for (const link of links) {
    const from = posByID.get((link.source as SimNode).id);
    const to = posByID.get((link.target as SimNode).id);
    if (!from || !to) continue;
    positionedEdges.push({ edge: link.edge, x1: from.x, y1: from.y, x2: to.x, y2: to.y });
  }

  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  for (const p of positioned) {
    minX = Math.min(minX, p.x);
    minY = Math.min(minY, p.y);
    maxX = Math.max(maxX, p.x);
    maxY = Math.max(maxY, p.y);
  }
  return {
    nodes: positioned,
    edges: positionedEdges,
    bounds: padBounds(minX, minY, maxX, maxY),
  };
}

/**
 * padBounds adds the glyph/label padding and enforces the minimum extent,
 * expanding around the centre so the graph stays centred either way.
 */
function padBounds(minX: number, minY: number, maxX: number, maxY: number): Layout["bounds"] {
  const grow = (lo: number, hi: number) => {
    let a = lo - LAYOUT.padding;
    let b = hi + LAYOUT.padding;
    const short = LAYOUT.minExtent - (b - a);
    if (short > 0) {
      a -= short / 2;
      b += short / 2;
    }
    return [a, b] as const;
  };
  const [x0, x1] = grow(minX, maxX);
  const [y0, y1] = grow(minY, maxY);
  return { minX: x0, minY: y0, maxX: x1, maxY: y1 };
}

/**
 * egoNetwork returns the ids within `depth` hops of `rootID`, walking edges in
 * BOTH directions.
 *
 * Depth 1 is deliberately the same walk `AgentNodeFacts` performs to fill an
 * NPC's Hot Context (ADR-0008 amendment) — so "focus at depth 1" answers "what
 * can this entry's NPC actually see", not merely "what is drawn near it".
 */
export function egoNetwork(
  rootID: string,
  edges: GraphEdge[],
  depth: number,
): ReadonlySet<string> {
  const neighbours = new Map<string, string[]>();
  const link = (a: string, b: string) => {
    const list = neighbours.get(a);
    if (list) list.push(b);
    else neighbours.set(a, [b]);
  };
  for (const e of edges) {
    link(e.fromNodeId, e.toNodeId);
    link(e.toNodeId, e.fromNodeId);
  }

  const seen = new Set<string>([rootID]);
  let frontier = [rootID];
  for (let d = 0; d < depth; d++) {
    const next: string[] = [];
    for (const id of frontier) {
      for (const n of neighbours.get(id) ?? []) {
        if (!seen.has(n)) {
          seen.add(n);
          next.push(n);
        }
      }
    }
    if (next.length === 0) break;
    frontier = next;
  }
  return seen;
}

/**
 * filterGraph applies the view's chips and toggles, returning the sub-payload the
 * layout should run over. Filtering BEFORE layout (rather than dimming after) is
 * what makes a filtered 300-node campaign legible: the survivors get the whole
 * canvas instead of the gaps their hidden neighbours left behind.
 */
export function filterGraph(
  nodes: GraphNode[],
  edges: GraphEdge[],
  opts: {
    types: ReadonlySet<NodeType>;
    relations: ReadonlySet<EdgeType>;
    /** Table view: hide gm_private nodes entirely, so the GM sees the world as their NPCs do. */
    hidePrivate: boolean;
    /** Focus mode: restrict to this ego network, or null for the whole graph. */
    focus: ReadonlySet<string> | null;
  },
): { nodes: GraphNode[]; edges: GraphEdge[] } {
  const keptNodes = nodes.filter((n) => {
    if (!opts.types.has(n.nodeType)) return false;
    if (opts.hidePrivate && n.gmPrivate) return false;
    if (opts.focus && !opts.focus.has(n.id)) return false;
    return true;
  });
  const kept = new Set(keptNodes.map((n) => n.id));
  const keptEdges = edges.filter(
    (e) => opts.relations.has(e.edgeType) && kept.has(e.fromNodeId) && kept.has(e.toNodeId),
  );
  return { nodes: keptNodes, edges: keptEdges };
}
