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
    expect(r.write.anchor.reason).toMatch(/gone/);
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
    expect(r.write.anchor.reason).toMatch(/2 entries/);
  });

  it("an exact-case match disambiguates a case-insensitive collision", () => {
    const twins = [
      ...NODES,
      create(GraphNodeSchema, { id: "bart-lower", nodeType: NodeType.ITEM, name: "bart" }),
    ];
    const [r] = resolveProposals([factProposal({ subject: "Bart" })], twins);
    if (r.write.kind !== "fact") throw new Error("expected a fact");
    expect(r.write.anchor).toEqual({ at: "node", id: "bart" });
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
    expect(r.write.to.reason).toMatch(/Atlantis/);
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
    // Exactly the attach offset away from its one neighbour — near enough to read
    // as "this goes here", far enough not to sit on top of it.
    expect(Math.hypot(ghost.x - bart.x, ghost.y - bart.y)).toBeCloseTo(GHOST.attachOffset, 0);
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
    for (const u of placed.unplaced) expect(u.reason).toBeTruthy();
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
    expect(placed.unplaced[0].reason).toMatch(/filtered out/);
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
