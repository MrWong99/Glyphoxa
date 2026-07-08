import { describe, it, expect, vi } from "vitest";
import type { DescMethodUnary } from "@bufbuild/protobuf";
import { create } from "@bufbuild/protobuf";
import { QueryClient } from "@tanstack/react-query";
import { createConnectQueryKey } from "@connectrpc/connect-query";

import {
  CampaignService,
  SessionService,
  ProviderService,
  GetCampaignRosterResponseSchema,
  SearchNodesResponseSchema,
  ListCampaignsResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { invalidateActiveCampaignScopedQueries } from "./campaignCache";

// The exact set the sweep MUST cover — every Active-Campaign-scoped read across
// the screens (roster/KG/session/transcript), keyed without an input so each
// prefix-matches every cached argument variant.
const SCOPED: DescMethodUnary[] = [
  CampaignService.method.getActiveCampaign,
  CampaignService.method.getCampaignRoster,
  CampaignService.method.listNodes,
  CampaignService.method.searchNodes,
  CampaignService.method.listNodeEdges,
  CampaignService.method.listToolGrants,
  SessionService.method.getSession,
  SessionService.method.searchTranscriptLines,
];

// Reads that MUST survive a switch: the operator's campaign set and the
// Tenant-scoped BYOK config are identical across campaigns.
const UNTOUCHED: DescMethodUnary[] = [
  CampaignService.method.listCampaigns,
  ProviderService.method.listProviderConfigs,
];

const finiteKey = (schema: DescMethodUnary) => createConnectQueryKey({ schema, cardinality: "finite" });

describe("invalidateActiveCampaignScopedQueries", () => {
  it("invalidates exactly the campaign-scoped reads and nothing else", async () => {
    const queryClient = new QueryClient();
    const spy = vi.spyOn(queryClient, "invalidateQueries");

    await invalidateActiveCampaignScopedQueries(queryClient);

    // Every scoped read is swept…
    for (const schema of SCOPED) {
      expect(spy).toHaveBeenCalledWith(expect.objectContaining({ queryKey: finiteKey(schema) }));
    }
    // …and no campaign-invariant read is.
    for (const schema of UNTOUCHED) {
      expect(spy).not.toHaveBeenCalledWith(expect.objectContaining({ queryKey: finiteKey(schema) }));
    }
    // The sweep is exactly the scoped set — no more, no fewer.
    expect(spy).toHaveBeenCalledTimes(SCOPED.length);
  });

  it("marks a cached campaign-scoped query stale, leaving an unrelated one fresh", async () => {
    const queryClient = new QueryClient();
    // Prime a scoped read under a realistic keyed input (roster's SearchNodes) and
    // an unrelated one (listCampaigns). The sweep key carries no input, so it must
    // prefix-match the primed input variant.
    const rosterKey = createConnectQueryKey({
      schema: CampaignService.method.getCampaignRoster,
      cardinality: "finite",
      input: {},
    });
    const searchKey = createConnectQueryKey({
      schema: CampaignService.method.searchNodes,
      cardinality: "finite",
      input: { query: "orc" },
    });
    const listKey = createConnectQueryKey({
      schema: CampaignService.method.listCampaigns,
      cardinality: "finite",
      input: {},
    });
    queryClient.setQueryData(rosterKey, create(GetCampaignRosterResponseSchema, { roster: [] }));
    queryClient.setQueryData(searchKey, create(SearchNodesResponseSchema, { nodes: [] }));
    queryClient.setQueryData(listKey, create(ListCampaignsResponseSchema, { campaigns: [] }));

    await invalidateActiveCampaignScopedQueries(queryClient);

    expect(queryClient.getQueryState(rosterKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(searchKey)?.isInvalidated).toBe(true);
    // listCampaigns is untouched — a switch doesn't change the campaign set.
    expect(queryClient.getQueryState(listKey)?.isInvalidated).toBe(false);
  });
});
