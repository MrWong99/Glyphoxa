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
