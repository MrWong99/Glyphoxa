import { describe, it, expect } from "vitest";
import { create } from "@bufbuild/protobuf";

import {
  AgentSchema,
  EdgeType,
  GraphEdgeSchema,
  GraphNodeSchema,
  NodeType,
} from "@gen/glyphoxa/management/v1/management_pb";
import { UNPLACED_TITLE, rosterPrep } from "./rosterPrep";

const agent = (o: Parameters<typeof create<typeof AgentSchema>>[1]) =>
  create(AgentSchema, { role: "character", ...o });
const node = (o: Parameters<typeof create<typeof GraphNodeSchema>>[1]) =>
  create(GraphNodeSchema, { nodeType: NodeType.NPC, ...o });
const edge = (from: string, to: string, edgeType: EdgeType) =>
  create(GraphEdgeSchema, { id: `${from}-${to}`, fromNodeId: from, toNodeId: to, edgeType });

describe("rosterPrep", () => {
  // The concrete failure this whole slice exists for: the ADR-0008 auto-node is
  // created EMPTY and the Persona is never copied, so a fully-configured NPC can
  // still have nothing to say — and that was only discovered at the table.
  it("flags a fully-configured NPC whose entry is empty", () => {
    const groups = rosterPrep(
      [agent({ id: "a1", name: "Bart", persona: "Gruff.", voice: "v1" })],
      [node({ id: "bart", name: "Bart", agentId: "a1" })],
      [],
    );
    const bart = groups[0].entries[0];
    expect(bart.ready).toBe(false);
    const content = bart.checks.find((c) => c.key === "content");
    expect(content?.ok).toBe(false);
    // Everything else passes, so the ONE unsatisfied mark points at the real gap.
    expect(bart.checks.filter((c) => !c.ok).map((c) => c.key)).toEqual(["content"]);
  });

  it("counts aspects as content, not just prose", () => {
    const groups = rosterPrep(
      [agent({ id: "a1", name: "Bart", persona: "Gruff.", voice: "v1" })],
      [node({ id: "bart", name: "Bart", agentId: "a1", aspectCount: 2 })],
      [],
    );
    expect(groups[0].entries[0].ready).toBe(true);
  });

  it("names each missing piece separately", () => {
    const groups = rosterPrep([agent({ id: "a1", name: "Blank" })], [], []);
    const checks = groups[0].entries[0].checks.filter((c) => !c.ok).map((c) => c.key);
    expect(checks).toEqual(["persona", "voice", "link", "content"]);
  });

  it("groups by faction and location, and lists an NPC under both", () => {
    const groups = rosterPrep(
      [agent({ id: "a1", name: "Bart", persona: "p", voice: "v" })],
      [
        node({ id: "bart", name: "Bart", agentId: "a1", bodyLen: 5 }),
        node({ id: "town", nodeType: NodeType.LOCATION, name: "Saltmarsh" }),
        node({ id: "guild", nodeType: NodeType.FACTION, name: "Smugglers" }),
      ],
      [edge("bart", "town", EdgeType.RESIDES_IN), edge("bart", "guild", EdgeType.MEMBER_OF)],
    );
    expect(groups.map((g) => g.title)).toEqual(["Saltmarsh", "Smugglers"]);
  });

  // Unplaced NPCs go into an explicit group rather than vanishing — an NPC you
  // forgot to place is exactly the one you need reminding about.
  it("puts unplaced NPCs in an explicit group, listed last", () => {
    const groups = rosterPrep(
      [
        agent({ id: "a1", name: "Bart", persona: "p", voice: "v" }),
        agent({ id: "a2", name: "Drifter", persona: "p", voice: "v" }),
      ],
      [
        node({ id: "bart", name: "Bart", agentId: "a1", bodyLen: 5 }),
        node({ id: "drift", name: "Drifter", agentId: "a2", bodyLen: 5 }),
        node({ id: "town", nodeType: NodeType.LOCATION, name: "Saltmarsh" }),
      ],
      [edge("bart", "town", EdgeType.RESIDES_IN)],
    );
    expect(groups.map((g) => g.title)).toEqual(["Saltmarsh", UNPLACED_TITLE]);
    expect(groups[1].entries.map((e) => e.agent.name)).toEqual(["Drifter"]);
  });

  // The Butler is Address-Only, auto-created and undeletable, with no linked Node
  // by design (ADR-0009) — grading it here would be a permanent false alarm.
  it("excludes the Butler from the character checks", () => {
    const groups = rosterPrep(
      [
        create(AgentSchema, { id: "b1", name: "Glyphoxa", role: "butler" }),
        agent({ id: "a1", name: "Bart", persona: "p", voice: "v", }),
      ],
      [node({ id: "bart", name: "Bart", agentId: "a1", bodyLen: 5 })],
      [],
    );
    const names = groups.flatMap((g) => g.entries.map((e) => e.agent.name));
    expect(names).toEqual(["Bart"]);
  });
});
