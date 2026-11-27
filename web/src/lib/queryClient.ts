import type { DescMethodUnary } from "@bufbuild/protobuf";
import { createConnectQueryKey } from "@connectrpc/connect-query";
import { MutationCache, QueryCache, QueryClient, type QueryKey } from "@tanstack/react-query";

import { AuthService } from "@gen/glyphoxa/management/v1/management_pb";
import { isPermissionDenied, isUnauthenticated } from "@/lib/connectError";
import { connectionStore } from "@/lib/connectionStatus";

// The session probe's cache key (every variant of it).
const currentUserKey = createConnectQueryKey({
  schema: AuthService.method.getCurrentUser,
  cardinality: "finite",
});

function isCurrentUserProbe(queryKey: QueryKey): boolean {
  const meta = queryKey[1] as { serviceName?: string; methodName?: string } | undefined;
  return (
    queryKey[0] === "connect-query" &&
    meta?.serviceName === AuthService.typeName &&
    meta.methodName === AuthService.method.getCurrentUser.name
  );
}

// One root QueryClient (ADR-0018): staleTime 30s, refetchOnWindowFocus true.
//
// Expired-session handling lives here, once, for every read and write: when
// ANY call comes back CodeUnauthenticated the session probe (GetCurrentUser)
// is invalidated. The AuthGate observes that probe, so it re-runs, fails the
// same way and redirects to /login — instead of each screen showing its own
// "couldn't load: unauthenticated" card on a session that quietly lapsed. The
// probe's own failure is excluded (it IS the signal; re-invalidating it would
// loop), which also keeps the public pages — where an Unauthenticated probe is
// the normal signed-out answer — untouched.
//
// A CodePermissionDenied re-runs the probe too (ADR-0065). What a session may
// do can change under it — a GM edits a Campaign Guest's rights or removes
// them, a Platform Admin revokes — and the first sign of that is a call the
// shell thought was allowed coming back refused. The probe's fresh answer
// carries the new Guest Rights (or no Workspace at all), and the shell redraws
// itself for them instead of leaving a screen of refused reads on display.
//
// The same two caches feed the connection status the shell's banner shows
// (lib/connectionStatus.ts): a READ that nobody answered raises "can't reach
// Glyphoxa", and any call the server did answer lowers it again. Only reads
// raise it — a failed write already reports itself where the GM pressed the
// button — but a write that succeeds is as good a sign of life as a read.
export function makeQueryClient(): QueryClient {
  const reprobeSession = (error: unknown) => {
    if (isUnauthenticated(error) || isPermissionDenied(error)) {
      void client.invalidateQueries({ queryKey: currentUserKey });
    }
  };
  const client: QueryClient = new QueryClient({
    queryCache: new QueryCache({
      onError: (error, query) => {
        connectionStore(client).noteError(error);
        if (!isCurrentUserProbe(query.queryKey)) reprobeSession(error);
      },
      onSuccess: () => connectionStore(client).noteSuccess(),
    }),
    mutationCache: new MutationCache({
      onError: reprobeSession,
      onSuccess: () => connectionStore(client).noteSuccess(),
    }),
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        refetchOnWindowFocus: true,
        // A refusal is an answer, not a blip: asking again cannot change it, and
        // three backed-off retries would hold the re-probe above (and the page
        // that explains what happened) back for seven seconds. Everything else
        // keeps React Query's three retries; a query that sets its own `retry`
        // is not affected.
        retry: (failureCount, error) => !isPermissionDenied(error) && failureCount < 3,
      },
    },
  });
  return client;
}

// invalidateMethodQueries drops every cached variant of the given unary reads.
// Each key is built WITHOUT an input so it prefix-matches all cached entries
// for that method (every search string, every node id). Returns the aggregate
// promise so a caller — or a test — can await the refetch settling.
export function invalidateMethodQueries(
  queryClient: QueryClient,
  ...methods: DescMethodUnary[]
): Promise<void> {
  return Promise.all(
    methods.map((schema) =>
      queryClient.invalidateQueries({
        queryKey: createConnectQueryKey({ schema, cardinality: "finite" }),
      }),
    ),
  ).then(() => undefined);
}
