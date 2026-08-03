import { createConnectQueryKey } from "@connectrpc/connect-query";
import type { QueryClient } from "@tanstack/react-query";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";

// One invalidation for every read of the Knowledge Graph (#534).
//
// The same nodes and edges are now served by FOUR queries — the list, the wiki
// search, a node's relations, and the whole-graph payload the Graph view renders —
// and they are mutated from three different surfaces: the Knowledge panel, the
// relations editor inside it, and the proposal review queue. Each surface used to
// invalidate the subset it happened to know about, which is a bug waiting per
// surface × per read: adding the graph read immediately made two of the three
// stale, so an edge created in the relations editor kept drawing on the graph
// until the 30s staleTime expired.
//
// So there is one helper and it drops all four. Over-invalidating costs a handful
// of small refetches on a single-operator tier; under-invalidating shows the GM a
// world that does not match their own last edit, which reads as a bug in the graph
// rather than a stale cache.
//
// Each key is built WITHOUT an input so it prefix-matches every cached variant
// (every search string, every node id).
export function invalidateKnowledgeReads(queryClient: QueryClient): void {
  void queryClient.invalidateQueries({
    queryKey: createConnectQueryKey({
      schema: CampaignService.method.listNodes,
      cardinality: "finite",
    }),
  });
  void queryClient.invalidateQueries({
    queryKey: createConnectQueryKey({
      schema: CampaignService.method.searchNodes,
      cardinality: "finite",
    }),
  });
  void queryClient.invalidateQueries({
    queryKey: createConnectQueryKey({
      schema: CampaignService.method.listNodeEdges,
      cardinality: "finite",
    }),
  });
  void queryClient.invalidateQueries({
    queryKey: createConnectQueryKey({
      schema: CampaignService.method.getKnowledgeGraph,
      cardinality: "finite",
    }),
  });
}
