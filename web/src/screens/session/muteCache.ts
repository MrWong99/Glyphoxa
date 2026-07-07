import { useCallback, useMemo } from "react";
import { createConnectQueryKey, useTransport } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";

import { SessionService } from "@gen/glyphoxa/management/v1/management_pb";
import type { GetSessionResponse } from "@gen/glyphoxa/management/v1/management_pb";

// useMuteCache patches the muted-Agent set on the SHARED getSession connect-query
// cache — the single source of truth the Voice control panel reads (ADR-0018: no
// parallel React state tree). Both mute surfaces converge here without a reload
// (#211, AC5): a mute mutation `replace`s the set from its authoritative response,
// and an SSE "mute" frame (a Discord/web mute reaching this browser) flips ONE
// Agent via `patchOne`. The interval getSession refetch reconciles against the
// server's true set, so a patch is only an instant-update optimisation.
export function useMuteCache() {
  const transport = useTransport();
  const queryClient = useQueryClient();

  // The EXACT key useQuery(getSession, {}) writes — transport + empty input +
  // finite cardinality — so setQueryData hits that same cache entry. Memoised on
  // the (stable) transport so the returned setters stay referentially stable and
  // never re-run the SSE effect that depends on patchOne.
  const key = useMemo(
    () =>
      createConnectQueryKey({
        schema: SessionService.method.getSession,
        transport,
        input: {},
        cardinality: "finite",
      }),
    [transport],
  );

  const replace = useCallback(
    (mutedAgentIds: string[]) =>
      queryClient.setQueryData<GetSessionResponse>(key, (prev) => (prev ? { ...prev, mutedAgentIds } : prev)),
    [queryClient, key],
  );

  const patchOne = useCallback(
    (agentId: string, muted: boolean) =>
      queryClient.setQueryData<GetSessionResponse>(key, (prev) => {
        if (!prev) return prev;
        const set = new Set(prev.mutedAgentIds);
        if (muted) set.add(agentId);
        else set.delete(agentId);
        return { ...prev, mutedAgentIds: [...set] };
      }),
    [queryClient, key],
  );

  // patchSpendCap flips the live spend-cap state on the same getSession cache from
  // an SSE "spendcap" frame (#130), so the Session screen shows the
  // spend-cap-reached state instantly. The interval getSession refetch reconciles
  // the exact state AND the estimated_spend_usd figure the frame does not carry, so
  // this is an instant-update optimisation over the authoritative reload truth.
  const patchSpendCap = useCallback(
    (level: string) =>
      queryClient.setQueryData<GetSessionResponse>(key, (prev) =>
        prev ? { ...prev, spendCapState: level } : prev,
      ),
    [queryClient, key],
  );

  return { replace, patchOne, patchSpendCap };
}
