import { EdgeType, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { GraphNode, KnowledgeProposal } from "@gen/glyphoxa/management/v1/management_pb";
import type { MessageKey, MsgParams } from "@/i18n";

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
 * A user-facing reason, carried as a message KEY plus params rather than a baked
 * string: this module is not a component, so the rendering surface translates it
 * with t(reason.key, reason.params) at render time (spec rule: never bake a
 * translated string into non-component state).
 */
export type ReasonMsg = { key: MessageKey; params?: MsgParams };

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
  | { at: "unknown"; name: string; reason: ReasonMsg };

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
 * It MIRRORS the server's resolution (storage.resolveProposalAnchor /
 * resolveNodeByName) rule for rule, because every difference is a lie told to the
 * GM at the moment they decide:
 *
 *   - A non-empty node_id is AUTHORITATIVE. The server never falls back to the
 *     name, so neither may this: falling back would draw the halo on a DIFFERENT
 *     entry that happens to share the name, and the GM would approve a write
 *     anchored somewhere they were never shown.
 *   - Any case-insensitive multi-match is refused. Disambiguating by exact case
 *     would promise an approval the server then rejects.
 *
 * `nodes` must be the WHOLE campaign payload, not a filtered subset — a name that
 * resolves fine but is filtered out of the current view is a DRAWING problem, not
 * a proposal problem, and conflating the two would tell the GM their Butler had
 * hallucinated an entry that exists. Callers pass the raw GetKnowledgeGraph nodes.
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
    // An id short-circuits everything, exactly as the server does. No name
    // fallback: an id that names nothing is a deleted anchor, not an invitation to
    // find something similarly named.
    if (id) {
      if (byID.has(id)) return { at: "node", id };
      return { at: "unknown", name, reason: { key: "knowledge.reasonEntryGone" } };
    }
    const key = name.trim().toLowerCase();
    if (!key) {
      return { at: "unknown", name, reason: { key: "knowledge.reasonNoName" } };
    }
    const candidates = byName.get(key) ?? [];
    if (candidates.length === 1) return { at: "node", id: candidates[0].id };
    if (candidates.length > 1) {
      // The server refuses ANY multi-match ("rename one first"), so an exact-case
      // tiebreak here would draw a confident halo on an approval that cannot land.
      return {
        at: "unknown",
        name,
        reason: { key: "knowledge.reasonAmbiguousName", params: { count: candidates.length, name } },
      };
    }
    if (proposedNames.has(key)) return { at: "ghost", name };
    return { at: "unknown", name, reason: { key: "knowledge.reasonNoEntryYet", params: { name } } };
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
export type UnplacedProposal = { proposal: ResolvedProposal; reason: ReasonMsg };

export type PlacedProposals = {
  ghostNodes: GhostNode[];
  ghostEdges: GhostEdge[];
  factMarks: FactMark[];
  unplaced: UnplacedProposal[];
  /** The layout bounds, grown to include any ghost placed outside them. */
  bounds: Layout["bounds"];
};

/**
 * angleOf turns a proposal id into a stable direction.
 *
 * Keying the angle on the ghost's INDEX meant approving or rejecting one node
 * proposal renumbered the rest, so every remaining ghost jumped to a new spot —
 * the same "keep your spatial bearings across a review session" the committed
 * nodes are pinned for, broken for the very things being reviewed. A hash of the
 * id does not renumber.
 */
function hashOf(id: string): number {
  let h = 2166136261;
  for (let i = 0; i < id.length; i++) {
    h ^= id.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

/** Placement tuning, exported so the view and its tests agree on one set of numbers. */
export const GHOST = {
  /** How far a ghost sits from the neighbour(s) it attaches to. */
  attachOffset: 62,
  /**
   * How close a ghost may come to a committed Node before it is pushed out. A
   * dashed circle sitting on top of a solid one is exactly the "mistakable for
   * canon" hazard the dashing exists to prevent.
   */
  minClearance: 38,
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
  proposedNodes.forEach((r) => {
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

    // Direction AND distance both come from a hash of the proposal ID, so two
    // ghosts on the same neighbour separate deterministically and neither moves
    // when a third is approved out of the queue.
    const h = hashOf(r.id);
    const angle = (h / 0x100000000) * Math.PI * 2;
    const ring = h % 4;
    const anchorX = neighbours.length > 0 ? neighbours.reduce((s, n) => s + n.x, 0) / neighbours.length : cx;
    const anchorY = neighbours.length > 0 ? neighbours.reduce((s, n) => s + n.y, 0) / neighbours.length : cy;
    const baseR = neighbours.length > 0 ? GHOST.attachOffset : cloudRadius + GHOST.orbitMargin;

    // Walk outward along the ray until the spot is clear of every committed Node.
    //
    // Only COMMITTED Nodes push a ghost outward — deliberately not other ghosts.
    // Avoiding other ghosts would make each ghost's position depend on which OTHER
    // suggestions are currently pending, so approving one would shift the rest:
    // exactly the instability that keying the angle on the id removes. The two
    // goals genuinely conflict and stability wins, because a ghost overlapping
    // another ghost is untidy whereas a ghost overlapping canon is the "mistakable
    // for committed" hazard AC1 forbids. The per-id ring below spreads ghosts
    // across four radii so an exact stack is vanishingly unlikely anyway.
    let x = 0;
    let y = 0;
    for (let step = 0; step < 6; step++) {
      const rad = baseR + (ring + step) * GHOST.minClearance;
      x = Math.round(anchorX + rad * Math.cos(angle));
      y = Math.round(anchorY + rad * Math.sin(angle));
      const clash = laid.nodes.some((p) => Math.hypot(p.x - x, p.y - y) < GHOST.minClearance);
      if (!clash) break;
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
  const locate = (a: Anchor): { x: number; y: number } | { miss: ReasonMsg } => {
    if (a.at === "unknown") return { miss: a.reason };
    if (a.at === "ghost") {
      const g = ghostByName.get(a.name.trim().toLowerCase());
      return g
        ? { x: g.x, y: g.y }
        : { miss: { key: "knowledge.reasonGhostOnly", params: { name: a.name } } };
    }
    const at = pos.get(a.id);
    return at ?? { miss: { key: "knowledge.reasonFilteredOut" } };
  };

  for (const r of resolved) {
    switch (r.write.kind) {
      case "node":
        break; // placed above
      case "unreadable":
        unplaced.push({ proposal: r, reason: { key: "knowledge.reasonUnreadable" } });
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
