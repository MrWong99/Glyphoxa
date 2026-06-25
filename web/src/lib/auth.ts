import { Code, ConnectError } from "@connectrpc/connect";

// isUnauthenticated reports whether an error from a Connect call is the
// server's CodeUnauthenticated — the SPA's "no/expired session" signal that
// drives the boot redirect to /login (ADR-0016). ConnectError.from normalizes
// any thrown value; a non-Connect error resolves to Code.Unknown, not
// Unauthenticated, so only a real 401 triggers the redirect.
export function isUnauthenticated(error: unknown): boolean {
  return ConnectError.from(error).code === Code.Unauthenticated;
}
