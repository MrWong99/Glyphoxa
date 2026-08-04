import { EdgeType, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { GraphNode, KnowledgeProposal } from "@gen/glyphoxa/management/v1/management_pb";

import type { Layout } from "./layout";

// Pending Knowledge Proposals, resolved against the drawn graph (#537, ADR-0052).
//
// The Proposals panel presents an Agent's proposed write as a naked sentence, but
// the GM's real question — "does this belong here, next to what the world already
// says?" — is a question about POSITION. This module answers the only two hard
// parts of drawing that question:
//
//   1. Resolution. A proposal names its endpoints by NAME, not by id (ADR-0052:
//      names are resolved at approval so nothing auto-merges). Drawing one means
//      doing that resolution in the client, and being honest when it fails: an
//      ambiguous or unknown name is a proposal that will be REFUSED at approval,
//      which is worth showing the GM before they click.
//   2. Placement. A proposed Node does not exist yet, so it has no layout position.
//      It is placed next to whatever the same queue proposes attaching it to.
//
// Everything here is pure, so the picture is snapshot-testable and — the point of
// the graph view — stable across renders.

/**
 * An endpoint a proposal names, resolved against the campaign.
 *
 * `ghost` is a name that no Node carries YET but that another pending proposal in
 * the same queue proposes creating — a Butler that proposes "the Sunken Chapel"
 * and then proposes an edge into it files two proposals, and the pair only makes
 * sense drawn together.
 */
export type Anchor =
  | { at: "node"; id: string }
  | { at: "ghost"; name: string }
  | { at: "unknown"; name: string; reason: string };

/** The proposed write, resolved. `unreadable` is a proposal whose payload did not parse. */
export type ResolvedWrite =
  | { kind: "fact"; anchor: Anchor; aspectKey: string; fact: string }
  | { kind: "edge"; from: Anchor; to: Anchor; relation: EdgeType }
  | { kind: "node"; name: string; nodeType: NodeType }
  | { kind: "unreadable" };

/** One pending proposal, resolved. `proposal` is carried through for the review card. */
export type ResolvedProposal = {
  id: string;
  agentName: string;
  proposal: KnowledgeProposal;
  write: ResolvedWrite;
};

/**
 * resolveProposals maps the pending queue onto the campaign's Nodes.
 *
 * Resolution runs against ALL campaign nodes, not the filtered subset: a name that
 * resolves fine but happens to be filtered out of the current view is a DRAWING
 * problem, not a proposal problem, and conflating the two would tell the GM their
 * Butler had hallucinated an entry that exists.
 */
export function resolveProposals(
  proposals: readonly KnowledgeProposal[],
  nodes: readonly GraphNode[],
): ResolvedProposal[] {
  const byID = new Map(nodes.map((n) => [n.id, n]));
  // Names collide in real campaigns ("Gundren" the NPC and "Gundren" the item), so
  // the index keeps every candidate and resolution below refuses to guess.
  const byName = new Map<string, GraphNode[]>();
  for (const n of nodes) {
    const key = n.name.trim().toLowerCase();
    const list = byName.get(key);
    if (list) list.push(n);
    else byName.set(key, [n]);
  }

  // The names this same queue proposes creating. A proposed edge into a proposed
  // Node resolves to a ghost rather than to "unknown".
  const proposedNames = new Set<string>();
  for (const p of proposals) {
    if (p.write.case === "node") proposedNames.add(p.write.value.name.trim().toLowerCase());
  }

  const resolve = (id: string, name: string): Anchor => {
    if (id && byID.has(id)) return { at: "node", id };
    const key = name.trim().toLowerCase();
    if (!key) {
      return { at: "unknown", name, reason: "the suggestion names no entry" };
    }
    const candidates = byName.get(key) ?? [];
    if (candidates.length === 1) return { at: "node", id: candidates[0].id };
    if (candidates.length > 1) {
      // Prefer an exact-case match when it is unique — that is a real
      // disambiguation, not a coin flip.
      const exact = candidates.filter((c) => c.name === name);
      if (exact.length === 1) return { at: "node", id: exact[0].id };
      return {
        at: "unknown",
        name,
        reason: `${candidates.length} entries are called "${name}" — approval can't tell which`,
      };
    }
    if (proposedNames.has(key)) return { at: "ghost", name };
    // An id that resolves to nothing means the anchor Node was deleted after the
    // Agent filed; say so, because approving will fail with exactly that.
    if (id) return { at: "unknown", name, reason: "the entry this was filed against is gone" };
    return { at: "unknown", name, reason: `no entry called "${name}" yet` };
  };

  return proposals.map((p): ResolvedProposal => {
    const base = { id: p.id, agentName: p.authoringAgentName, proposal: p };
    const w = p.write;
    switch (w.case) {
      case "fact":
        return {
          ...base,
          write: {
            kind: "fact",
            anchor: resolve(w.value.nodeId, w.value.subject),
            aspectKey: w.value.aspectKey,
            fact: w.value.fact,
          },
        };
      case "edge":
        return {
          ...base,
          write: {
            kind: "edge",
            from: resolve(w.value.nodeId, w.value.subject),
            to: resolve("", w.value.target),
            relation: w.value.relation,
          },
        };
      case "node":
        return {
          ...base,
          write: { kind: "node", name: w.value.name, nodeType: w.value.nodeType },
        };
      default:
        return { ...base, write: { kind: "unreadable" } };
    }
  });
}

/** A proposed Node, placed. `key` is stable across renders (the proposal id). */
export type GhostNode = {
  key: string;
  proposalID: string;
  name: string;
  nodeType: NodeType;
  agentName: string;
  x: number;
  y: number;
};

/** A proposed Edge, placed between two drawn (or ghosted) endpoints. */
export type GhostEdge = {
  key: string;
  proposalID: string;
  relation: EdgeType;
  agentName: string;
  x1: number;
  y1: number;
  x2: number;
  y2: number;
};

/** A proposed fact, marking the Node it would land on. */
export type FactMark = {
  key: string;
  proposalID: string;
  nodeID: string;
  agentName: string;
  x: number;
  y: number;
};

/** A proposal that could not be drawn, and why. It is listed, never dropped. */
export type UnplacedProposal = { proposal: ResolvedProposal; reason: string };

export type PlacedProposals = {
  ghostNodes: GhostNode[];
  ghostEdges: GhostEdge[];
  factMarks: FactMark[];
  unplaced: UnplacedProposal[];
  /** The layout bounds, grown to include any ghost placed outside them. */
  bounds: Layout["bounds"];
};

/** Placement tuning, exported so the view and its tests agree on one set of numbers. */
export const GHOST = {
  /** How far a ghost sits from the neighbour(s) it attaches to. */
  attachOffset: 62,
  /** How far outside the node cloud an unattached ghost is ringed. */
  orbitMargin: 70,
  /** Padding added around a ghost when growing the bounds, so its label is not clipped. */
  labelPad: 90,
} as const;

/**
 * placeProposals positions the resolved queue on top of an existing layout.
 *
 * It never moves a laid-out Node: ghosts are an OVERLAY. That is what makes
 * approving a suggestion leave the GM's spatial bearings intact — the picture
 * underneath is the same picture, with one dashed thing turned solid.
 */
export function placeProposals(
  resolved: readonly ResolvedProposal[],
  laid: Layout,
): PlacedProposals {
  const pos = new Map(laid.nodes.map((p) => [p.node.id, { x: p.x, y: p.y }]));

  const ghostNodes: GhostNode[] = [];
  const ghostEdges: GhostEdge[] = [];
  const factMarks: FactMark[] = [];
  const unplaced: UnplacedProposal[] = [];

  // Centre and radius of the drawn cloud, for ghosts with nothing to attach to.
  let cx = 0;
  let cy = 0;
  for (const p of laid.nodes) {
    cx += p.x;
    cy += p.y;
  }
  if (laid.nodes.length > 0) {
    cx /= laid.nodes.length;
    cy /= laid.nodes.length;
  }
  let cloudRadius = 0;
  for (const p of laid.nodes) {
    cloudRadius = Math.max(cloudRadius, Math.hypot(p.x - cx, p.y - cy));
  }

  // Pass 1: place proposed Nodes, each next to the drawn endpoints the queue
  // proposes attaching it to. A ghost with no attachments is ringed outside the
  // cloud — visibly not yet part of the world, and never buried under it.
  const ghostByName = new Map<string, GhostNode>();
  const proposedNodes = resolved.filter((r) => r.write.kind === "node");
  proposedNodes.forEach((r, i) => {
    if (r.write.kind !== "node") return; // narrowing only
    const key = r.write.name.trim().toLowerCase();
    const neighbours: Array<{ x: number; y: number }> = [];
    for (const other of resolved) {
      if (other.write.kind !== "edge") continue;
      const ends = [other.write.from, other.write.to];
      // This ghost is one end; the OTHER end, if drawn, is what it attaches to.
      if (!ends.some((a) => a.at === "ghost" && a.name.trim().toLowerCase() === key)) continue;
      for (const a of ends) {
        if (a.at !== "node") continue;
        const at = pos.get(a.id);
        if (at) neighbours.push(at);
      }
    }

    // The offset direction comes from the ghost's INDEX, not from anything
    // render-order dependent, so two ghosts on the same neighbour separate
    // deterministically instead of stacking.
    const angle = i * 2.399963229728653; // the golden angle, in radians
    let x: number;
    let y: number;
    if (neighbours.length > 0) {
      const mx = neighbours.reduce((s, n) => s + n.x, 0) / neighbours.length;
      const my = neighbours.reduce((s, n) => s + n.y, 0) / neighbours.length;
      x = Math.round(mx + GHOST.attachOffset * Math.cos(angle));
      y = Math.round(my + GHOST.attachOffset * Math.sin(angle));
    } else {
      const r = cloudRadius + GHOST.orbitMargin;
      x = Math.round(cx + r * Math.cos(angle));
      y = Math.round(cy + r * Math.sin(angle));
    }

    const ghost: GhostNode = {
      key: r.id,
      proposalID: r.id,
      name: r.write.name,
      nodeType: r.write.nodeType,
      agentName: r.agentName,
      x,
      y,
    };
    ghostNodes.push(ghost);
    // First ghost wins a duplicated name: two proposals for the same new entry are
    // a dedup problem for the GM, not a reason to draw two circles on one spot.
    if (!ghostByName.has(key)) ghostByName.set(key, ghost);
  });

  // Pass 2: everything that hangs off a position.
  const locate = (a: Anchor): { x: number; y: number } | { miss: string } => {
    if (a.at === "unknown") return { miss: a.reason };
    if (a.at === "ghost") {
      const g = ghostByName.get(a.name.trim().toLowerCase());
      return g ? { x: g.x, y: g.y } : { miss: `"${a.name}" is only a suggestion itself` };
    }
    const at = pos.get(a.id);
    return at ?? { miss: "the entry it points at is filtered out of this view" };
  };

  for (const r of resolved) {
    switch (r.write.kind) {
      case "node":
        break; // placed above
      case "unreadable":
        unplaced.push({ proposal: r, reason: "the suggestion's payload could not be read" });
        break;
      case "fact": {
        const at = locate(r.write.anchor);
        if ("miss" in at) {
          unplaced.push({ proposal: r, reason: at.miss });
          break;
        }
        factMarks.push({
          key: r.id,
          proposalID: r.id,
          nodeID: r.write.anchor.at === "node" ? r.write.anchor.id : "",
          agentName: r.agentName,
          x: at.x,
          y: at.y,
        });
        break;
      }
      case "edge": {
        const from = locate(r.write.from);
        const to = locate(r.write.to);
        if ("miss" in from) {
          unplaced.push({ proposal: r, reason: from.miss });
          break;
        }
        if ("miss" in to) {
          unplaced.push({ proposal: r, reason: to.miss });
          break;
        }
        ghostEdges.push({
          key: r.id,
          proposalID: r.id,
          relation: r.write.relation,
          agentName: r.agentName,
          x1: from.x,
          y1: from.y,
          x2: to.x,
          y2: to.y,
        });
        break;
      }
    }
  }

  // Grow the viewBox around any ghost that landed outside it. Growing the BOX is
  // not relaying out the graph: every Node keeps the coordinate it already had.
  let { minX, minY, maxX, maxY } = laid.bounds;
  for (const g of ghostNodes) {
    minX = Math.min(minX, g.x - GHOST.labelPad);
    minY = Math.min(minY, g.y - GHOST.labelPad);
    maxX = Math.max(maxX, g.x + GHOST.labelPad);
    maxY = Math.max(maxY, g.y + GHOST.labelPad);
  }

  return { ghostNodes, ghostEdges, factMarks, unplaced, bounds: { minX, minY, maxX, maxY } };
}
