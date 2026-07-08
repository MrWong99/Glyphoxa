import { describe, it, expect } from "vitest";
import { create } from "@bufbuild/protobuf";
import { QueryClient } from "@tanstack/react-query";
import { createConnectQueryKey } from "@connectrpc/connect-query";

import {
  CampaignService,
  SessionService,
  ProviderService,
  GetCampaignRosterResponseSchema,
  SearchNodesResponseSchema,
  GetSessionResponseSchema,
  ListCampaignsResponseSchema,
  ListProviderConfigsResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { invalidateActiveCampaignScopedQueries, watchVoiceSessionEnd } from "./campaignCache";

// Real connect-query keys, exactly as the screens' useQuery calls build them.
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
const sessionKey = createConnectQueryKey({
  schema: SessionService.method.getSession,
  cardinality: "finite",
  input: {},
});
const listKey = createConnectQueryKey({
  schema: CampaignService.method.listCampaigns,
  cardinality: "finite",
  input: {},
});
const providerKey = createConnectQueryKey({
  schema: ProviderService.method.listProviderConfigs,
  cardinality: "finite",
  input: {},
});

// primeAll seeds one representative of each cache population: campaign-scoped
// reads (with and without inputs), campaign-invariant reads, a hypothetical
// FUTURE campaign-scoped read this module has never heard of, and a
// non-connect-query key.
const futureKey = [
  "connect-query",
  {
    serviceName: "glyphoxa.management.v1.CampaignService",
    methodName: "ListKnowledgeProposals",
    cardinality: "finite",
  },
] as const;
const foreignKey = ["not-connect-query", "whatever"] as const;

function primeAll(queryClient: QueryClient) {
  queryClient.setQueryData(rosterKey, create(GetCampaignRosterResponseSchema, { roster: [] }));
  queryClient.setQueryData(searchKey, create(SearchNodesResponseSchema, { nodes: [] }));
  queryClient.setQueryData(sessionKey, create(GetSessionResponseSchema, { active: false }));
  queryClient.setQueryData(listKey, create(ListCampaignsResponseSchema, { campaigns: [] }));
  queryClient.setQueryData(providerKey, create(ListProviderConfigsResponseSchema, {}));
  queryClient.setQueryData(futureKey, { anything: true });
  queryClient.setQueryData(foreignKey, { anything: true });
}

const invalidated = (queryClient: QueryClient, key: readonly unknown[]) =>
  queryClient.getQueryState(key as unknown[])?.isInvalidated;

describe("invalidateActiveCampaignScopedQueries", () => {
  it("marks campaign-scoped reads stale and leaves campaign-invariant ones fresh", async () => {
    const queryClient = new QueryClient();
    primeAll(queryClient);

    await invalidateActiveCampaignScopedQueries(queryClient);

    // Campaign-scoped reads — including keyed inputs — are swept…
    expect(invalidated(queryClient, rosterKey)).toBe(true);
    expect(invalidated(queryClient, searchKey)).toBe(true);
    expect(invalidated(queryClient, sessionKey)).toBe(true);
    // …campaign-invariant reads are not (the campaign set and the Tenant's BYOK
    // config are identical across campaigns)…
    expect(invalidated(queryClient, listKey)).toBe(false);
    expect(invalidated(queryClient, providerKey)).toBe(false);
    // …and keys that aren't connect-query reads are untouched.
    expect(invalidated(queryClient, foreignKey)).toBe(false);
  });

  it("fails SAFE: a future campaign-scoped read unknown to this module is swept too", async () => {
    // The sweep is a deny-list, so a read RPC added later without touching
    // campaignCache.ts gets an extra refetch (harmless) instead of silently
    // serving the previous campaign's data (the failure mode an allow-list has).
    const queryClient = new QueryClient();
    primeAll(queryClient);

    await invalidateActiveCampaignScopedQueries(queryClient);

    expect(invalidated(queryClient, futureKey)).toBe(true);
  });
});

describe("watchVoiceSessionEnd", () => {
  const setSession = (queryClient: QueryClient, active: boolean) =>
    queryClient.setQueryData(sessionKey, create(GetSessionResponseSchema, { active }));

  it("sweeps campaign-scoped caches when the Voice Session ends (active true→false)", () => {
    const queryClient = new QueryClient();
    const unsubscribe = watchVoiceSessionEnd(queryClient);
    queryClient.setQueryData(rosterKey, create(GetCampaignRosterResponseSchema, { roster: [] }));

    // First observation (live) only seeds the watcher — no sweep yet.
    setSession(queryClient, true);
    expect(invalidated(queryClient, rosterKey)).toBe(false);

    // The Voice Session ends: the mid-session switch's "takes effect after it
    // ends" moment — the sweep must fire now.
    setSession(queryClient, false);
    expect(invalidated(queryClient, rosterKey)).toBe(true);
    unsubscribe();
  });

  it("does not sweep on first observing an idle Voice Session, nor on idle→idle", () => {
    const queryClient = new QueryClient();
    const unsubscribe = watchVoiceSessionEnd(queryClient);
    queryClient.setQueryData(rosterKey, create(GetCampaignRosterResponseSchema, { roster: [] }));

    // Mounting onto an already-idle cache must not fire (no transition)…
    setSession(queryClient, false);
    expect(invalidated(queryClient, rosterKey)).toBe(false);
    // …and neither must the sweep's own getSession refresh re-reporting idle
    // (false→false), which is what makes the watcher loop-free.
    setSession(queryClient, false);
    expect(invalidated(queryClient, rosterKey)).toBe(false);
    unsubscribe();
  });

  it("stops watching after unsubscribe", () => {
    const queryClient = new QueryClient();
    const unsubscribe = watchVoiceSessionEnd(queryClient);
    queryClient.setQueryData(rosterKey, create(GetCampaignRosterResponseSchema, { roster: [] }));

    setSession(queryClient, true);
    unsubscribe();
    setSession(queryClient, false);
    expect(invalidated(queryClient, rosterKey)).toBe(false);
  });
});
