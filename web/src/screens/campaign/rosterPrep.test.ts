import { describe, it, expect } from "vitest";
import { create } from "@bufbuild/protobuf";

import {
  AgentSchema,
  EdgeType,
  GraphEdgeSchema,
  GraphNodeSchema,
  NodeType,
} from "@gen/glyphoxa/management/v1/management_pb";
import { UNPLACED_TITLE, lastSpokeLabel, rosterPrep, type Readiness } from "./rosterPrep";

const ready = (o: Partial<Readiness> = {}): Readiness => ({
  linked: true,
  factCount: 3,
  chars: 400,
  maxChars: 4000,
  truncated: false,
  ...o,
});

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
      // Linked, but an empty entry has nothing for the read to return.
      new Map([["a1", ready({ factCount: 0 })]]),
    );
    const bart = groups[0].entries[0];
    expect(bart.ready).toBe(false);
    const content = bart.checks.find((c) => c.key === "content");
    expect(content?.ok).toBe(false);
    // Persona, voice and link all pass, so the unsatisfied marks point at the real
    // gap: the entry is empty and therefore nothing reaches the prompt.
    expect(bart.checks.filter((c) => !c.ok).map((c) => c.key)).toEqual(["content", "facts"]);
  });

  it("counts aspects as content, not just prose", () => {
    const groups = rosterPrep(
      [agent({ id: "a1", name: "Bart", persona: "Gruff.", voice: "v1" })],
      [node({ id: "bart", name: "Bart", agentId: "a1", aspectCount: 2, publicAspectCount: 2 })],
      [],
      new Map([["a1", ready()]]),
    );
    expect(groups[0].entries[0].ready).toBe(true);
  });

  // A secret-only entry is authored but says nothing to its NPC — the readiness
  // marks answer "can this NPC speak to my world", not "did I type something".
  it("does not count a GM-only fact as content", () => {
    const groups = rosterPrep(
      [agent({ id: "a1", name: "Bart", persona: "Gruff.", voice: "v1" })],
      [node({ id: "bart", name: "Bart", agentId: "a1", aspectCount: 1, publicAspectCount: 0 })],
      [],
      new Map([["a1", ready({ factCount: 0 })]]),
    );
    const bart = groups[0].entries[0];
    expect(bart.ready).toBe(false);
    expect(bart.checks.filter((c) => !c.ok).map((c) => c.key)).toEqual(["content", "facts"]);
  });

  // "The entry has words in it" and "the NPC will actually receive them" are
  // different claims: the neighbourhood read and the render caps sit between them.
  // The count comes from the SAME renderer the turn uses (#535).
  it("grades on facts actually in reach, not just on entry content", () => {
    const nodes = [node({ id: "bart", name: "Bart", agentId: "a1", bodyLen: 200 })];
    const configured = [agent({ id: "a1", name: "Bart", persona: "p", voice: "v" })];

    const reaching = rosterPrep(configured, nodes, [], new Map([["a1", ready({ factCount: 4 })]]));
    expect(reaching[0].entries[0].ready).toBe(true);
    expect(reaching[0].entries[0].checks.find((c) => c.key === "facts")?.label).toBe(
      "4 facts in reach",
    );

    const starved = rosterPrep(configured, nodes, [], new Map([["a1", ready({ factCount: 0 })]]));
    expect(starved[0].entries[0].ready).toBe(false);
    expect(starved[0].entries[0].checks.filter((c) => !c.ok).map((c) => c.key)).toEqual(["facts"]);
  });

  it("surfaces when each NPC last spoke", () => {
    const at = new Date("2026-07-04T20:00:00Z");
    const groups = rosterPrep(
      [agent({ id: "a1", name: "Bart", persona: "p", voice: "v" })],
      [node({ id: "bart", name: "Bart", agentId: "a1", bodyLen: 5 })],
      [],
      new Map([["a1", ready({ lastSpokeAt: at })]]),
    );
    expect(groups[0].entries[0].readiness?.lastSpokeAt).toEqual(at);
    expect(lastSpokeLabel(groups[0].entries[0].readiness)).toMatch(/last spoke/);
    expect(lastSpokeLabel(undefined)).toBe("never spoken");
  });

  it("names each missing piece separately", () => {
    const groups = rosterPrep([agent({ id: "a1", name: "Blank" })], [], []);
    const checks = groups[0].entries[0].checks.filter((c) => !c.ok).map((c) => c.key);
    expect(checks).toEqual(["persona", "voice", "link", "content", "facts"]);
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
      new Map([["a1", ready()]]),
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
      new Map([
        ["a1", ready()],
        ["a2", ready()],
      ]),
    );
    expect(groups.map((g) => g.title)).toEqual(["Saltmarsh", UNPLACED_TITLE]);
    expect(groups[1].entries.map((e) => e.agent.name)).toEqual(["Drifter"]);
  });

  // The AC is precise: the Butler is PRESENT and NOT FLAGGED. Dropping it would
  // hide a roster member the GM looks for; grading it against character checks
  // would flag it forever, since it is Address-Only with no linked Node by design
  // (ADR-0009).
  it("lists the Butler without grading it", () => {
    const groups = rosterPrep(
      [
        create(AgentSchema, { id: "b1", name: "Glyphoxa", role: "butler" }),
        agent({ id: "a1", name: "Bart", persona: "p", voice: "v" }),
      ],
      [node({ id: "bart", name: "Bart", agentId: "a1", bodyLen: 5 })],
      [],
      new Map([["a1", ready()]]),
    );
    const entries = groups.flatMap((g) => g.entries);
    const butler = entries.find((e) => e.agent.name === "Glyphoxa");
    expect(butler, "the Butler must be listed").toBeDefined();
    expect(butler?.exempt).toBe(true);
    expect(butler?.checks).toEqual([]);
    // And it must never read as a problem.
    expect(butler?.ready).toBe(true);
  });

  // Each mark must point at a surface where the fix can ACTUALLY be made. The
  // Agent editor has no link control — that lives in the Knowledge tab's
  // relations card — so routing "link" to the Agent editor would strand the GM.
  it("routes each fix to a surface that can perform it", () => {
    const groups = rosterPrep([agent({ id: "a1", name: "Blank" })], [], []);
    const fixes = Object.fromEntries(groups[0].entries[0].checks.map((c) => [c.key, c.fix]));
    expect(fixes).toEqual({
      persona: "agent",
      voice: "agent",
      link: "link",
      content: "entry",
      facts: "entry",
    });
  });
});
