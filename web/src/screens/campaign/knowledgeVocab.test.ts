import { describe, expect, it } from "vitest";

import { EdgeType, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import { EDGE_TYPES, FACT_SUGGESTIONS, TYPE_ORDER, isEdgeLegal, legalEdgeTypes } from "./knowledgeVocab";

// isEdgeLegal is a MIRROR of internal/storage/kg_edge.go ValidateEdge. The
// table below is that function's four rules written out, so a change on either
// side that is not made on the other fails here.
describe("isEdgeLegal", () => {
  const loose = [EdgeType.KNOWS, EdgeType.OWNS, EdgeType.ENEMY_OF, EdgeType.ALLY_OF, EdgeType.MENTIONED_IN];

  it("leaves the social relations unconstrained on both ends", () => {
    for (const edge of loose) {
      for (const from of TYPE_ORDER) {
        for (const to of TYPE_ORDER) expect(isEdgeLegal(edge, from, to)).toBe(true);
      }
    }
  });

  it.each([
    [EdgeType.RESIDES_IN, NodeType.LOCATION],
    [EdgeType.MEMBER_OF, NodeType.FACTION],
    [EdgeType.PARTICIPATED_IN, NodeType.PLOT_THREAD],
  ])("constrains only the target of structural relation %s", (edge, target) => {
    for (const from of TYPE_ORDER) {
      for (const to of TYPE_ORDER) expect(isEdgeLegal(edge, from, to)).toBe(to === target);
    }
  });

  it("constrains both ends of parent of to a Character or an NPC", () => {
    const people = new Set([NodeType.CHARACTER, NodeType.NPC]);
    for (const from of TYPE_ORDER) {
      for (const to of TYPE_ORDER) {
        expect(isEdgeLegal(EdgeType.PARENT_OF, from, to)).toBe(people.has(from) && people.has(to));
      }
    }
  });

  it("refuses a relation it does not know", () => {
    expect(isEdgeLegal(EdgeType.UNSPECIFIED, NodeType.NPC, NodeType.NPC)).toBe(false);
  });

  it("always leaves at least one relation to pick between any two entries", () => {
    for (const from of TYPE_ORDER) {
      for (const to of TYPE_ORDER) expect(legalEdgeTypes(from, to).length).toBeGreaterThan(0);
    }
  });

  it("lists legal relations in vocabulary order", () => {
    expect(legalEdgeTypes(NodeType.NPC, NodeType.LOCATION)).toEqual(
      EDGE_TYPES.map((e) => e.value).filter(
        (e) => ![EdgeType.MEMBER_OF, EdgeType.PARENT_OF, EdgeType.PARTICIPATED_IN].includes(e),
      ),
    );
  });
});

describe("FACT_SUGGESTIONS", () => {
  it("starts an NPC's secret GM only and nothing else", () => {
    const npc = FACT_SUGGESTIONS[NodeType.NPC];
    expect(npc.map((s) => s.labelKey)).toEqual([
      "knowledge.factSuggestRole",
      "knowledge.factSuggestAppearance",
      "knowledge.factSuggestWants",
      "knowledge.factSuggestSecret",
    ]);
    expect(npc.filter((s) => s.gmPrivate).map((s) => s.labelKey)).toEqual(["knowledge.factSuggestSecret"]);
  });

  it("covers every authorable type", () => {
    for (const type of TYPE_ORDER) expect(FACT_SUGGESTIONS[type]).toBeDefined();
  });
});
