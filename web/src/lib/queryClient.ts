import type { DescMethodUnary } from "@bufbuild/protobuf";
import { createConnectQueryKey } from "@connectrpc/connect-query";
import { MutationCache, QueryCache, QueryClient, type QueryKey } from "@tanstack/react-query";

import { AuthService } from "@gen/glyphoxa/management/v1/management_pb";
import { isUnauthenticated } from "@/lib/connectError";

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
export function makeQueryClient(): QueryClient {
  const reprobeSession = (error: unknown) => {
    if (isUnauthenticated(error)) void client.invalidateQueries({ queryKey: currentUserKey });
  };
  const client: QueryClient = new QueryClient({
    queryCache: new QueryCache({
      onError: (error, query) => {
        if (!isCurrentUserProbe(query.queryKey)) reprobeSession(error);
      },
    }),
    mutationCache: new MutationCache({ onError: reprobeSession }),
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        refetchOnWindowFocus: true,
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
