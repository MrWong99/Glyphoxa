import type { ReactNode } from "react";
import { TransportProvider } from "@connectrpc/connect-query";
import { QueryClientProvider, type QueryClient } from "@tanstack/react-query";
import type { Transport } from "@connectrpc/connect";

// Wraps the app in the Connect transport + one root QueryClient (ADR-0018).
// Both are injectable so the vitest render test can swap a router transport for
// the live one and a fresh QueryClient per test (no shared cache, no network).
export function Providers({
  transport,
  queryClient,
  children,
}: {
  transport: Transport;
  queryClient: QueryClient;
  children: ReactNode;
}) {
  return (
    <TransportProvider transport={transport}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </TransportProvider>
  );
}
