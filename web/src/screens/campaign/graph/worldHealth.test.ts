import { describe, it, expect } from "vitest";
import { create } from "@bufbuild/protobuf";

import {
  AgentSchema,
  EdgeType,
  GraphEdgeSchema,
  GraphNodeSchema,
  NodeType,
} from "@gen/glyphoxa/management/v1/management_pb";
import { worldHealth } from "./worldHealth";

type NodeInit = Parameters<typeof create<typeof GraphNodeSchema>>[1];
const node = (o: NodeInit) => create(GraphNodeSchema, { nodeType: NodeType.NOTE, ...o });

const byKey = (cats: ReturnType<typeof worldHealth>, key: string) =>
  cats.find((c) => c.key === key);

describe("worldHealth", () => {
  // The worst failure in the epic: ADR-0008's second amendment creates an NPC Node
  // on Character-NPC create with an EMPTY body and never copies the Persona, so
  // "voiced character with nothing to say" is the DEFAULT state after adding an
  // NPC — currently discovered live, mid-session.
  it("flags a voiced NPC whose entry is empty", () => {
    const cats = worldHealth(
      [
        node({ id: "bart", nodeType: NodeType.NPC, name: "Bart", agentId: "a1" }),
        node({ id: "ilva", nodeType: NodeType.NPC, name: "Ilva", agentId: "a2", bodyLen: 20 }),
        node({ id: "zed", nodeType: NodeType.NPC, name: "Zed", agentId: "a3", aspectCount: 2 }),
      ],
      [],
      [],
    );
    const empty = byKey(cats, "empty-voiced");
    expect(empty?.findings.map((f) => f.name)).toEqual(["Bart"]);
  });

  // An orphan can never enter an NPC's Hot Context through the neighbour walk —
  // unless the Node IS an Agent's own linked Node, which reaches the prompt
  // directly. So a linked orphan is not the problem this category names.
  it("flags unconnected entries but not a linked NPC's own entry", () => {
    const cats = worldHealth(
      [
        node({ id: "lonely", name: "A lost ring", nodeType: NodeType.ITEM }),
        node({ id: "bart", nodeType: NodeType.NPC, name: "Bart", agentId: "a1", bodyLen: 5 }),
        node({ id: "town", nodeType: NodeType.LOCATION, name: "Saltmarsh" }),
      ],
      [
        create(GraphEdgeSchema, {
          id: "e1",
          fromNodeId: "town",
          toNodeId: "lonely",
          edgeType: EdgeType.OWNS,
        }),
      ],
      [],
    );
    const orphans = byKey(cats, "orphans");
    // "lonely" and "town" are connected to each other; "bart" is unconnected but
    // reachable as its Agent's own node.
    expect(orphans).toBeUndefined();
  });

  it("flags a plot thread nobody participates in", () => {
    const cats = worldHealth(
      [
        node({ id: "heist", nodeType: NodeType.PLOT_THREAD, name: "The heist" }),
        node({ id: "war", nodeType: NodeType.PLOT_THREAD, name: "The war" }),
        node({ id: "ilva", nodeType: NodeType.NPC, name: "Ilva", agentId: "a1", bodyLen: 3 }),
      ],
      [
        create(GraphEdgeSchema, {
          id: "e1",
          fromNodeId: "ilva",
          toNodeId: "war",
          edgeType: EdgeType.PARTICIPATED_IN,
        }),
      ],
      [],
    );
    expect(byKey(cats, "dangling-threads")?.findings.map((f) => f.name)).toEqual(["The heist"]);
  });

  it("flags NPC entries with no voice, and cast with no entry", () => {
    const cats = worldHealth(
      [node({ id: "bart", nodeType: NodeType.NPC, name: "Bart", bodyLen: 5 })],
      [],
      [
        create(AgentSchema, { id: "a1", name: "Voiceless", role: "character" }),
        create(AgentSchema, { id: "b1", name: "Glyphoxa", role: "butler" }),
      ],
    );
    expect(byKey(cats, "unlinked-npcs")?.findings.map((f) => f.name)).toEqual(["Bart"]);
    // The Butler is exempt: it is Address-Only and has no linked Node by design.
    expect(byKey(cats, "cast-without-entry")?.findings.map((f) => f.name)).toEqual(["Voiceless"]);
  });

  // A wall of "0 problems" sections trains the GM to skim past the one that is
  // not zero.
  it("drops empty categories entirely", () => {
    const cats = worldHealth(
      [
        node({ id: "bart", nodeType: NodeType.NPC, name: "Bart", agentId: "a1", bodyLen: 5 }),
        node({ id: "town", nodeType: NodeType.LOCATION, name: "Saltmarsh", bodyLen: 5 }),
      ],
      [
        create(GraphEdgeSchema, {
          id: "e1",
          fromNodeId: "bart",
          toNodeId: "town",
          edgeType: EdgeType.RESIDES_IN,
        }),
      ],
      [create(AgentSchema, { id: "a1", name: "Bart", role: "character" })],
    );
    expect(cats).toHaveLength(0);
  });

  it("names a consequence for every category, not just a label", () => {
    const cats = worldHealth([node({ id: "x", name: "Loose end" })], [], []);
    for (const c of cats) {
      expect(c.why.length, `${c.key} has no stated consequence`).toBeGreaterThan(20);
    }
  });
});
