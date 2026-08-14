import type { DescMethodUnary } from "@bufbuild/protobuf";
import { createConnectQueryKey } from "@connectrpc/connect-query";
import { QueryClient } from "@tanstack/react-query";

// One root QueryClient (ADR-0018): staleTime 30s, refetchOnWindowFocus true.
export function makeQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        refetchOnWindowFocus: true,
      },
    },
  });
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
