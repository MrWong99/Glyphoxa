import type { DescMethodUnary } from "@bufbuild/protobuf";
import type { QueryClient, QueryKey } from "@tanstack/react-query";

import {
  AuthService,
  CampaignService,
  ProviderService,
  SessionService,
  VoiceService,
  type GetSessionResponse,
} from "@gen/glyphoxa/management/v1/management_pb";

// The Active-Campaign cache-invalidation sweep (#267) — the heart of the campaign
// switch. Nearly every read RPC is scoped to the operator's Active Campaign
// SERVER-SIDE (ADR-0039): the request messages are empty or carry only a
// sub-selector (agent id, node id, search string), never the campaign id, so the
// server resolves the campaign per call. When SetActiveCampaign changes that
// selection the cached results silently go stale — nothing in the request key
// changed, so React Query can't know — and this sweep marks them for refetch so
// every campaign-scoped screen follows the new selection without a reload.
//
// The sweep is a DENY-list predicate, not an allow-list: it invalidates every
// cached connect-query read EXCEPT the ones enumerated below as campaign-
// INVARIANT. That makes it fail SAFE — a future campaign-scoped read a screen
// adds without touching this file gets one harmless extra refetch on switch,
// instead of silently serving the previous campaign's data (the worst failure
// mode for a GM mid-prep).
//
// Campaign-invariant reads (identical across campaigns, so a switch must NOT
// refetch them): the operator's own campaign set — only the create flow changes
// it — plus the Tenant-scoped BYOK/provider config, the vendor catalogs, and the
// signed-in operator identity.
const CAMPAIGN_INVARIANT_READS: DescMethodUnary[] = [
  AuthService.method.getCurrentUser,
  CampaignService.method.listCampaigns,
  ProviderService.method.listProviderConfigs,
  ProviderService.method.getSpendCaps,
  VoiceService.method.getProviderHealth,
  VoiceService.method.listModels,
  VoiceService.method.listVoices,
];

// methodId renders a method descriptor as the "<service typeName>/<MethodName>"
// pair a connect-query key carries as {serviceName, methodName}.
function methodId(m: DescMethodUnary): string {
  return `${m.parent.typeName}/${m.name}`;
}

const INVARIANT_IDS = new Set(CAMPAIGN_INVARIANT_READS.map(methodId));

// keyMethodId extracts the same "<serviceName>/<MethodName>" pair from a cached
// connect-query key: ["connect-query", {serviceName, methodName, ...}]. Returns
// null for anything that isn't a connect-query method key.
function keyMethodId(queryKey: QueryKey): string | null {
  if (queryKey[0] !== "connect-query") return null;
  const meta = queryKey[1] as { serviceName?: string; methodName?: string } | undefined;
  if (!meta?.serviceName || !meta.methodName) return null;
  return `${meta.serviceName}/${meta.methodName}`;
}

// invalidateActiveCampaignScopedQueries marks every Active-Campaign-scoped read
// stale (and refetches the active ones). Returns the aggregate promise so a
// caller — or a test — can await the refetch settling.
export function invalidateActiveCampaignScopedQueries(queryClient: QueryClient): Promise<void> {
  return queryClient.invalidateQueries({
    predicate: (query) => {
      const id = keyMethodId(query.queryKey);
      return id !== null && !INVARIANT_IDS.has(id);
    },
  });
}

const GET_SESSION_ID = methodId(SessionService.method.getSession);

// watchVoiceSessionEnd runs the sweep when the live Voice Session ends. A switch
// (or create) made while a Voice Session is live writes the durable selection,
// but the server resolves live-first (#222), so the sweep that ran on
// SetActiveCampaign success refetched the OLD campaign and cached it fresh — and
// nothing else re-runs it at the moment the promise "takes effect after this
// session" comes due. This watcher observes the shared getSession cache entry —
// regardless of WHICH observer fetched it (the Session screen's poll, its SSE
// patching, or the switcher's own panel read) — and fires the sweep on the
// active true→false transition. Sweeping on every Voice Session end (even with
// no pending switch) is deliberate: it costs a handful of refetches and
// guarantees every screen reflects post-Voice-Session resolution truth.
//
// Returns the unsubscribe function; mount it once from a long-lived component
// (the topbar CampaignSwitcher owns the promise, so it mounts the watcher).
export function watchVoiceSessionEnd(queryClient: QueryClient): () => void {
  // Last observed `active` per cached getSession entry. Seeded on first sight so
  // mounting onto an already-idle (or already-live) cache never fires a sweep.
  const lastActive = new Map<string, boolean>();
  return queryClient.getQueryCache().subscribe((event) => {
    if (event.type !== "updated" && event.type !== "added") return;
    if (keyMethodId(event.query.queryKey) !== GET_SESSION_ID) return;
    const active = (event.query.state.data as GetSessionResponse | undefined)?.active;
    if (active === undefined) return;
    const prev = lastActive.get(event.query.queryHash);
    lastActive.set(event.query.queryHash, active);
    // true→false only: the sweep's own getSession invalidation re-reports
    // false→false, so this cannot loop.
    if (prev === true && !active) void invalidateActiveCampaignScopedQueries(queryClient);
  });
}
