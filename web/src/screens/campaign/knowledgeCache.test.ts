import { describe, it, expect } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { createConnectQueryKey } from "@connectrpc/connect-query";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { invalidateKnowledgeReads } from "./knowledgeCache";

// The same nodes and edges are served by four queries and mutated from three
// surfaces. Each surface used to invalidate the subset it happened to know about,
// which meant adding the graph read silently left two of the three stale — an edge
// created in the relations editor kept drawing on the graph until staleTime
// expired. This pins that the one helper really covers all four, so a fourth
// reader added later fails HERE rather than in the field.
describe("invalidateKnowledgeReads", () => {
  // Named so a failure says WHICH read was left stale.
  const READS: Record<string, unknown[]> = {
    listNodes: createConnectQueryKey({
      schema: CampaignService.method.listNodes,
      cardinality: "finite",
      input: {},
    }),
    searchNodes: createConnectQueryKey({
      schema: CampaignService.method.searchNodes,
      cardinality: "finite",
      input: { query: "bart" },
    }),
    listNodeEdges: createConnectQueryKey({
      schema: CampaignService.method.listNodeEdges,
      cardinality: "finite",
      input: { nodeId: "n1" },
    }),
    getKnowledgeGraph: createConnectQueryKey({
      schema: CampaignService.method.getKnowledgeGraph,
      cardinality: "finite",
      input: {},
    }),
    // Derived reads: pure functions of the same nodes and edges, and stale for
    // the same reasons.
    getAgentFactPreview: createConnectQueryKey({
      schema: CampaignService.method.getAgentFactPreview,
      cardinality: "finite",
      input: { agentId: "a1" },
    }),
    getRosterReadiness: createConnectQueryKey({
      schema: CampaignService.method.getRosterReadiness,
      cardinality: "finite",
      input: {},
    }),
  };

  it("marks every Knowledge Graph read stale", () => {
    const qc = new QueryClient();
    for (const key of Object.values(READS)) qc.setQueryData(key, {} as never);
    for (const [name, key] of Object.entries(READS)) {
      expect(qc.getQueryState(key)?.isInvalidated, `${name} started fresh`).toBe(false);
    }

    invalidateKnowledgeReads(qc);

    for (const [name, key] of Object.entries(READS)) {
      expect(qc.getQueryState(key)?.isInvalidated, `${name} was left stale`).toBe(true);
    }
  });

  it("prefix-matches every cached variant of a parameterised read", () => {
    const qc = new QueryClient();
    // Two different search strings and two different nodes' edges: a key built with
    // one input would leave the others stale.
    const keys = [
      createConnectQueryKey({
        schema: CampaignService.method.searchNodes,
        cardinality: "finite",
        input: { query: "bart" },
      }),
      createConnectQueryKey({
        schema: CampaignService.method.searchNodes,
        cardinality: "finite",
        input: { query: "inn" },
      }),
      createConnectQueryKey({
        schema: CampaignService.method.listNodeEdges,
        cardinality: "finite",
        input: { nodeId: "n1" },
      }),
      createConnectQueryKey({
        schema: CampaignService.method.listNodeEdges,
        cardinality: "finite",
        input: { nodeId: "n2" },
      }),
    ];
    for (const key of keys) qc.setQueryData(key, {} as never);

    invalidateKnowledgeReads(qc);

    for (const key of keys) {
      expect(qc.getQueryState(key)?.isInvalidated).toBe(true);
    }
  });
});
