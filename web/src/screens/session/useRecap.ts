import { useMutation } from "@connectrpc/connect-query";
import { skipToken, useMutationState, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { SessionService } from "@gen/glyphoxa/management/v1/management_pb";
import type { GenerateRecapRequest } from "@gen/glyphoxa/management/v1/management_pb";
import { apiErrorMessage } from "@/lib/connectError";
import { useI18n } from "@/i18n";

// Session recaps (#274, epic #252), held for as long as the tab lives.
//
// GenerateRecap is REGENERATED per call and never persisted server-side (gate
// #271), spends provider money, and takes up to two minutes for a long session.
// So a recap the GM has paid and waited for must not be thrown away by looking
// at another session or opening the wiki: the result is written to the query
// cache under the session it covers, with no garbage collection, and read back
// from there. It lives until the tab is closed or reloaded — exactly as long
// as an unpersisted recap honestly can.
//
// Two details make that hold:
//
//   - the write happens in the MUTATION's own onSuccess (the options given to
//     useMutation), not in a callback passed to mutate(): React Query drops the
//     latter when the component that started the call has unmounted, which is
//     precisely the "went to the wiki while it was generating" case;
//   - the in-flight state is read from the mutation cache by key, not from the
//     component's own mutation observer, so a GM who comes back mid-generation
//     sees it still generating instead of an idle button they might press
//     (and pay for) twice.

export type Recap = {
  text: string;
  /** When the result arrived, epoch ms. */
  generatedAt: number;
};

const RECAP_SCOPE = "sessionRecap";
const recapKey = (sessionId: string) => [RECAP_SCOPE, sessionId] as const;
const RECAP_MUTATION_KEY = ["generateRecap"] as const;

function storeRecap(queryClient: QueryClient, sessionId: string, recap: Recap) {
  // Never collected: an entry with no observer is otherwise dropped after the
  // default five minutes, i.e. while the GM is reading the wiki.
  queryClient.setQueryDefaults([RECAP_SCOPE], { gcTime: Infinity, staleTime: Infinity });
  queryClient.setQueryData(recapKey(sessionId), recap);
}

/** The recap held for one session, if one was generated in this tab. */
export function useRecapResult(sessionId: string | null): Recap | undefined {
  return useQuery<Recap>({
    queryKey: recapKey(sessionId ?? ""),
    // Nothing to fetch: the entry is only ever written by a finished generation.
    queryFn: skipToken,
    gcTime: Infinity,
    staleTime: Infinity,
  }).data;
}

/** The ids of the sessions a recap is being generated for right now. */
export function useRecapsInFlight(): Set<string> {
  const pending = useMutationState({
    filters: { mutationKey: RECAP_MUTATION_KEY, status: "pending" },
    select: (mutation) => (mutation.state.variables as GenerateRecapRequest | undefined)?.sessionIds ?? [],
  });
  return new Set(pending.flat());
}

/**
 * Starts a recap for one session. The result lands in the cache (see
 * useRecapResult); a failure surfaces as a toast (ADR-0017).
 */
export function useGenerateRecap(): (sessionId: string) => void {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const generate = useMutation(SessionService.method.generateRecap, {
    mutationKey: RECAP_MUTATION_KEY,
    onSuccess: (res, req) => {
      const sessionId = req.sessionIds?.[0];
      if (sessionId) storeRecap(queryClient, sessionId, { text: res.text, generatedAt: Date.now() });
    },
    onError: (err: Error) =>
      toast.error(t("session.couldntGenerateRecap", { message: apiErrorMessage(err, t) })),
  });
  return (sessionId: string) => generate.mutate({ sessionIds: [sessionId] });
}
