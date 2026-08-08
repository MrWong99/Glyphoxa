import { describe, it, expect } from "vitest";
import { create } from "@bufbuild/protobuf";

import {
  EdgeType,
  GraphNodeSchema,
  KnowledgeProposalSchema,
  NodeType,
} from "@gen/glyphoxa/management/v1/management_pb";
import type { KnowledgeProposal } from "@gen/glyphoxa/management/v1/management_pb";
import { GHOST, placeProposals, resolveProposals } from "./proposalGhosts";
import { layout } from "./layout";

// Resolving and placing the pending queue on the graph (#537).
//
// The resolution half is where the interesting failures live: a proposal names its
// endpoints by NAME (ADR-0052 resolves them at approval, so nothing auto-merges),
// and a name that will be REFUSED at approval should say so on the card rather
// than after the click.

const NODES = [
  create(GraphNodeSchema, { id: "bart", nodeType: NodeType.NPC, name: "Bart" }),
  create(GraphNodeSchema, { id: "town", nodeType: NodeType.LOCATION, name: "Saltmarsh" }),
  create(GraphNodeSchema, { id: "guild", nodeType: NodeType.FACTION, name: "Smugglers" }),
];

function factProposal(over: Partial<{ id: string; subject: string; nodeId: string }> = {}) {
  return create(KnowledgeProposalSchema, {
    id: over.id ?? "p-fact",
    authoringAgentName: "Bart",
    write: {
      case: "fact",
      value: {
        nodeId: over.nodeId ?? "",
        subject: over.subject ?? "Bart",
        fact: "keeps a ledger under the bar",
        aspectKey: "Rumour",
      },
    },
  });
}

function edgeProposal(subject: string, target: string, id = "p-edge"): KnowledgeProposal {
  return create(KnowledgeProposalSchema, {
    id,
    authoringAgentName: "Glyphoxa",
    write: {
      case: "edge",
      value: { nodeId: "", subject, relation: EdgeType.MEMBER_OF, target },
    },
  });
}

function nodeProposal(name: string, id = "p-node"): KnowledgeProposal {
  return create(KnowledgeProposalSchema, {
    id,
    authoringAgentName: "Glyphoxa",
    write: { case: "node", value: { nodeType: NodeType.LOCATION, name, body: "" } },
  });
}

describe("resolveProposals", () => {
  it("resolves a name to the entry that carries it", () => {
    const [r] = resolveProposals([factProposal()], NODES);
    expect(r.write.kind).toBe("fact");
    if (r.write.kind !== "fact") return;
    expect(r.write.anchor).toEqual({ at: "node", id: "bart" });
  });

  it("prefers an id over the name when the proposal carries one", () => {
    const [r] = resolveProposals([factProposal({ nodeId: "town", subject: "Bart" })], NODES);
    if (r.write.kind !== "fact") throw new Error("expected a fact");
    expect(r.write.anchor).toEqual({ at: "node", id: "town" });
  });

  it("an id whose entry is gone reports that, not a missing name", () => {
    const [r] = resolveProposals([factProposal({ nodeId: "deleted", subject: "Nowhere" })], NODES);
    if (r.write.kind !== "fact") throw new Error("expected a fact");
    expect(r.write.anchor.at).toBe("unknown");
    if (r.write.anchor.at !== "unknown") return;
    // Reasons are message keys + params now, translated by the rendering surface.
    expect(r.write.anchor.reason.key).toBe("knowledge.reasonEntryGone");
  });

  it("refuses to guess between two entries with the same name", () => {
    const twins = [
      ...NODES,
      create(GraphNodeSchema, { id: "bart-item", nodeType: NodeType.ITEM, name: "Bart" }),
    ];
    const [r] = resolveProposals([factProposal()], twins);
    if (r.write.kind !== "fact") throw new Error("expected a fact");
    // Both are exactly "Bart", so exact-case cannot disambiguate either. Picking
    // one would silently file the fact on the wrong entry.
    expect(r.write.anchor.at).toBe("unknown");
    if (r.write.anchor.at !== "unknown") return;
    expect(r.write.anchor.reason.key).toBe("knowledge.reasonAmbiguousName");
    expect(r.write.anchor.reason.params?.count).toBe(2);
  });

  it("refuses a case-insensitive collision even when one match is exact-case", () => {
    const twins = [
      ...NODES,
      create(GraphNodeSchema, { id: "bart-lower", nodeType: NodeType.ITEM, name: "bart" }),
    ];
    const [r] = resolveProposals([factProposal({ subject: "Bart" })], twins);
    if (r.write.kind !== "fact") throw new Error("expected a fact");
    // The SERVER refuses any multi-match on lower(name) ("rename one first"), so
    // an exact-case tiebreak here would draw a confident halo on an approval that
    // cannot land — a promise the GM then watches fail.
    expect(r.write.anchor.at).toBe("unknown");
    if (r.write.anchor.at !== "unknown") return;
    expect(r.write.anchor.reason.key).toBe("knowledge.reasonAmbiguousName");
  });

  it("an id that names nothing never falls back to a same-named entry", () => {
    // The server treats node_id as authoritative and NEVER falls back to the name.
    // A client that did would draw the halo on a different entry that happens to
    // share the name, and the GM would approve a write anchored somewhere they
    // were never shown.
    const [r] = resolveProposals([factProposal({ nodeId: "stale-id", subject: "Bart" })], NODES);
    if (r.write.kind !== "fact") throw new Error("expected a fact");
    expect(r.write.anchor.at).toBe("unknown");
  });

  it("an edge into an entry the same queue proposes creating resolves to a ghost", () => {
    const resolved = resolveProposals(
      [nodeProposal("The Sunken Chapel"), edgeProposal("Bart", "The Sunken Chapel")],
      NODES,
    );
    const edge = resolved.find((r) => r.write.kind === "edge")!;
    if (edge.write.kind !== "edge") throw new Error("expected an edge");
    expect(edge.write.from).toEqual({ at: "node", id: "bart" });
    expect(edge.write.to).toEqual({ at: "ghost", name: "The Sunken Chapel" });
  });

  it("a name nothing knows is unknown, with a reason the GM can act on", () => {
    const [r] = resolveProposals([edgeProposal("Bart", "Atlantis")], NODES);
    if (r.write.kind !== "edge") throw new Error("expected an edge");
    expect(r.write.to.at).toBe("unknown");
    if (r.write.to.at !== "unknown") return;
    // The unknown name rides along as a param, so the rendered copy names it.
    expect(r.write.to.reason.key).toBe("knowledge.reasonNoEntryYet");
    expect(r.write.to.reason.params?.name).toBe("Atlantis");
  });

  it("an unparseable proposal survives as unreadable rather than being dropped", () => {
    const blank = create(KnowledgeProposalSchema, { id: "p-x", authoringAgentName: "Bart" });
    const [r] = resolveProposals([blank], NODES);
    expect(r.write.kind).toBe("unreadable");
  });
});

describe("placeProposals", () => {
  const laid = layout(NODES, []);

  it("never moves a laid-out node", () => {
    const resolved = resolveProposals(
      [factProposal(), edgeProposal("Bart", "Saltmarsh"), nodeProposal("The Sunken Chapel")],
      NODES,
    );
    const before = laid.nodes.map((p) => [p.node.id, p.x, p.y]);
    placeProposals(resolved, laid);
    expect(laid.nodes.map((p) => [p.node.id, p.x, p.y])).toEqual(before);
  });

  it("draws a proposed edge between the two entries it names", () => {
    const resolved = resolveProposals([edgeProposal("Bart", "Saltmarsh")], NODES);
    const placed = placeProposals(resolved, laid);
    expect(placed.ghostEdges).toHaveLength(1);
    const bart = laid.nodes.find((p) => p.node.id === "bart")!;
    const town = laid.nodes.find((p) => p.node.id === "town")!;
    expect([placed.ghostEdges[0].x1, placed.ghostEdges[0].y1]).toEqual([bart.x, bart.y]);
    expect([placed.ghostEdges[0].x2, placed.ghostEdges[0].y2]).toEqual([town.x, town.y]);
  });

  it("places a proposed entry beside the entry its proposed edge attaches it to", () => {
    const resolved = resolveProposals(
      [nodeProposal("The Sunken Chapel"), edgeProposal("Bart", "The Sunken Chapel")],
      NODES,
    );
    const placed = placeProposals(resolved, laid);
    expect(placed.ghostNodes).toHaveLength(1);
    const bart = laid.nodes.find((p) => p.node.id === "bart")!;
    const ghost = placed.ghostNodes[0];
    // Near enough to read as "this goes here", far enough not to sit on top of it.
    // The exact radius carries a per-id ring offset (so ghosts spread instead of
    // stacking), hence a band rather than a point.
    const d = Math.hypot(ghost.x - bart.x, ghost.y - bart.y);
    expect(d).toBeGreaterThanOrEqual(GHOST.attachOffset);
    expect(d).toBeLessThanOrEqual(GHOST.attachOffset + 4 * GHOST.minClearance);
    // And it is beside THAT neighbour, not merely somewhere on the canvas.
    const nearest = [...laid.nodes].sort(
      (a, b) => Math.hypot(a.x - ghost.x, a.y - ghost.y) - Math.hypot(b.x - ghost.x, b.y - ghost.y),
    )[0];
    expect(nearest.node.id).toBe("bart");
    // And the proposed edge now runs to the ghost.
    expect(placed.ghostEdges).toHaveLength(1);
    expect([placed.ghostEdges[0].x2, placed.ghostEdges[0].y2]).toEqual([ghost.x, ghost.y]);
  });

  it("rings an unattached proposed entry outside the cloud instead of burying it", () => {
    const resolved = resolveProposals([nodeProposal("The Sunken Chapel")], NODES);
    const placed = placeProposals(resolved, laid);
    const ghost = placed.ghostNodes[0];
    for (const p of laid.nodes) {
      expect(
        Math.hypot(ghost.x - p.x, ghost.y - p.y),
        "an unattached ghost landed on top of a real entry",
      ).toBeGreaterThan(20);
    }
    // The viewBox grows to include it — growing the BOX is not relaying out.
    expect(placed.bounds.minX).toBeLessThanOrEqual(laid.bounds.minX);
    expect(placed.bounds.maxX).toBeGreaterThanOrEqual(laid.bounds.maxX);
  });

  it("two ghosts on the same neighbour separate deterministically", () => {
    const resolved = resolveProposals(
      [
        nodeProposal("Chapel", "p1"),
        edgeProposal("Bart", "Chapel", "e1"),
        nodeProposal("Crypt", "p2"),
        edgeProposal("Bart", "Crypt", "e2"),
      ],
      NODES,
    );
    const a = placeProposals(resolved, laid);
    const b = placeProposals(resolved, laid);
    expect(a.ghostNodes.map((g) => [g.name, g.x, g.y])).toEqual(
      b.ghostNodes.map((g) => [g.name, g.x, g.y]),
    );
    const [g1, g2] = a.ghostNodes;
    expect(Math.hypot(g1.x - g2.x, g1.y - g2.y)).toBeGreaterThan(20);
  });

  it("lists what it cannot draw instead of silently dropping it", () => {
    const resolved = resolveProposals(
      [edgeProposal("Bart", "Atlantis"), factProposal({ subject: "Nobody" })],
      NODES,
    );
    const placed = placeProposals(resolved, laid);
    expect(placed.ghostEdges).toHaveLength(0);
    expect(placed.factMarks).toHaveLength(0);
    // Both are reachable, with a reason — a proposal that vanishes from the graph
    // AND has no other surface is a proposal the GM can never review.
    expect(placed.unplaced).toHaveLength(2);
    for (const u of placed.unplaced) expect(u.reason.key).toBeTruthy();
  });

  it("a resolvable entry that is filtered out of the view says so", () => {
    // Bart resolves fine, but the drawn subset excludes him — a DRAWING problem,
    // not a proposal problem, and conflating the two would tell the GM their
    // Butler had hallucinated an entry that exists.
    const withoutBart = layout(
      NODES.filter((n) => n.id !== "bart"),
      [],
    );
    const resolved = resolveProposals([factProposal()], NODES);
    const placed = placeProposals(resolved, withoutBart);
    expect(placed.unplaced).toHaveLength(1);
    expect(placed.unplaced[0].reason.key).toBe("knowledge.reasonFilteredOut");
  });

  it("ghosts keep their spot when another proposal leaves the queue", () => {
    // Keying the angle on the ghost's INDEX meant approving one node proposal
    // renumbered the rest, so every remaining ghost jumped — the same spatial
    // bearings the committed nodes are pinned for, broken for the very things
    // being reviewed.
    const three = resolveProposals(
      [nodeProposal("Chapel", "p1"), nodeProposal("Crypt", "p2"), nodeProposal("Vault", "p3")],
      NODES,
    );
    const before = placeProposals(three, laid);
    const afterApproval = placeProposals(
      three.filter((r) => r.id !== "p1"),
      laid,
    );
    for (const g of afterApproval.ghostNodes) {
      const was = before.ghostNodes.find((b) => b.proposalID === g.proposalID)!;
      expect([g.x, g.y], `${g.name} moved when another suggestion was reviewed`).toEqual([
        was.x,
        was.y,
      ]);
    }
  });

  it("a ghost is never placed on top of a committed entry", () => {
    // A dashed circle sitting on a solid one is exactly the "mistakable for canon"
    // hazard the dashing exists to prevent.
    const many = resolveProposals(
      Array.from({ length: 12 }, (_, i) => nodeProposal(`Ghost ${i}`, `p${i}`)),
      NODES,
    );
    const placed = placeProposals(many, laid);
    for (const g of placed.ghostNodes) {
      for (const p of laid.nodes) {
        expect(
          Math.hypot(g.x - p.x, g.y - p.y),
          `${g.name} landed on ${p.node.name}`,
        ).toBeGreaterThanOrEqual(GHOST.minClearance);
      }
    }
  });

  it("no two ghosts share a spot", () => {
    // Deliberately weaker than the committed-node clearance above. Ghosts do NOT
    // push each other apart, because that would make each one's position depend on
    // which other suggestions are pending — approving one would shift the rest.
    // The per-id ring makes an exact stack vanishingly unlikely instead.
    const many = resolveProposals(
      Array.from({ length: 12 }, (_, i) => nodeProposal(`Ghost ${i}`, `p${i}`)),
      NODES,
    );
    const placed = placeProposals(many, laid);
    const spots = new Set(placed.ghostNodes.map((g) => `${g.x},${g.y}`));
    expect(spots.size).toBe(placed.ghostNodes.length);
  });

  it("places proposed entries even when the campaign has no entries yet", () => {
    // A brand-new campaign whose Butler has proposed its first entries: gating the
    // canvas on committed nodes alone hid every ghost behind "nothing to draw".
    const empty = layout([], []);
    const placed = placeProposals(resolveProposals([nodeProposal("First Light")], []), empty);
    expect(placed.ghostNodes).toHaveLength(1);
    expect(Number.isFinite(placed.ghostNodes[0].x)).toBe(true);
    expect(placed.bounds.maxX).toBeGreaterThan(placed.bounds.minX);
    expect(placed.bounds.maxY).toBeGreaterThan(placed.bounds.minY);
  });

  it("an empty queue changes nothing at all", () => {
    const placed = placeProposals([], laid);
    expect(placed.ghostNodes).toHaveLength(0);
    expect(placed.ghostEdges).toHaveLength(0);
    expect(placed.factMarks).toHaveLength(0);
    expect(placed.unplaced).toHaveLength(0);
    expect(placed.bounds).toEqual(laid.bounds);
  });
});
