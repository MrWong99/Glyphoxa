import { createConnectQueryKey } from "@connectrpc/connect-query";
import type { QueryClient } from "@tanstack/react-query";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";

// One invalidation for every read of the Knowledge Graph (#534).
//
// The same nodes and edges are now served by EIGHT queries — the list, the wiki
// search, a node's relations, and the whole-graph payload the Graph view renders —
// and they are mutated from three different surfaces: the Knowledge panel, the
// relations editor inside it, and the proposal review queue. Each surface used to
// invalidate the subset it happened to know about, which is a bug waiting per
// surface × per read: adding the graph read immediately made two of the three
// stale, so an edge created in the relations editor kept drawing on the graph
// until the 30s staleTime expired.
//
// So there is one helper and it drops all eight — including the two derived reads
// (#535's fact preview and #544's roster readiness), which are pure functions of
// the same nodes and edges and go stale for exactly the same reasons. Over-invalidating costs a handful
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
  // Tags and boards REFERENCE nodes, and a node delete cascades their rows away
  // server-side. Leaving these two out left a deleted entry's tag in the campaign
  // vocabulary (click the chip, get an empty list, which reads as a broken filter)
  // and "(deleted entry)" sitting on a board — the exact "a world that does not
  // match your own last edit" this helper exists to prevent.
  void queryClient.invalidateQueries({
    queryKey: createConnectQueryKey({
      schema: CampaignService.method.getCampaignTags,
      cardinality: "finite",
    }),
  });
  void queryClient.invalidateQueries({
    queryKey: createConnectQueryKey({
      schema: CampaignService.method.listBoards,
      cardinality: "finite",
    }),
  });
  // Derived from the same nodes and edges: an edit that changes what an NPC knows
  // must not leave the lens and the readiness marks describing the old world.
  void queryClient.invalidateQueries({
    queryKey: createConnectQueryKey({
      schema: CampaignService.method.getAgentFactPreview,
      cardinality: "finite",
    }),
  });
  void queryClient.invalidateQueries({
    queryKey: createConnectQueryKey({
      schema: CampaignService.method.getRosterReadiness,
      cardinality: "finite",
    }),
  });
}
