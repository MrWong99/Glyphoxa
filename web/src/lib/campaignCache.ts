import type { DescMethodUnary } from "@bufbuild/protobuf";
import type { QueryClient } from "@tanstack/react-query";
import { createConnectQueryKey } from "@connectrpc/connect-query";

import { CampaignService, SessionService } from "@gen/glyphoxa/management/v1/management_pb";

// The Active-Campaign cache-invalidation sweep (#267) — the heart of the campaign
// switch. Every read RPC below is scoped to the operator's Active Campaign
// SERVER-SIDE (ADR-0039): the request messages are empty or carry only a
// sub-selector (agent id, node id, search string), never the campaign id, so the
// server resolves the campaign per call. When SetActiveCampaign changes that
// selection the cached results silently go stale — nothing in the request key
// changed, so React Query can't know — and this sweep marks them for refetch so
// every campaign-scoped screen follows the new selection without a reload.
//
// Each key is built WITHOUT an `input` so it prefix-matches every cached query
// for that method regardless of its arguments (mirrors the KnowledgePanel edge/
// search invalidations): switching campaign must drop the roster's tool grants,
// the KG's per-node edges, and any in-flight wiki/transcript search alike.
//
// Deliberately excluded: listCampaigns (the operator's campaign set is unchanged
// by a switch — only the create flow touches it) and the VoiceService catalog /
// ProviderService credentials (Tenant-scoped BYOK, identical across campaigns).
const CAMPAIGN_SCOPED_READS: DescMethodUnary[] = [
  CampaignService.method.getActiveCampaign,
  CampaignService.method.getCampaignRoster,
  CampaignService.method.listNodes,
  CampaignService.method.searchNodes,
  CampaignService.method.listNodeEdges,
  CampaignService.method.listToolGrants,
  SessionService.method.getSession,
  SessionService.method.searchTranscriptLines,
];

// invalidateActiveCampaignScopedQueries marks every Active-Campaign-scoped read
// stale (and refetches the active ones). Returns the aggregate promise so a
// caller — or a test — can await the refetch settling.
export function invalidateActiveCampaignScopedQueries(queryClient: QueryClient): Promise<void> {
  return Promise.all(
    CAMPAIGN_SCOPED_READS.map((schema) =>
      queryClient.invalidateQueries({
        queryKey: createConnectQueryKey({ schema, cardinality: "finite" }),
      }),
    ),
  ).then(() => undefined);
}
