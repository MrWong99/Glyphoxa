import { Code, ConnectError } from "@connectrpc/connect";

// isUnauthenticated reports whether an error from a Connect call is the
// server's CodeUnauthenticated — the SPA's "no/expired session" signal that
// drives the boot redirect to /login (ADR-0016). ConnectError.from normalizes
// any thrown value; a non-Connect error resolves to Code.Unknown, not
// Unauthenticated, so only a real 401 triggers the redirect.
export function isUnauthenticated(error: unknown): boolean {
  return ConnectError.from(error).code === Code.Unauthenticated;
}

// isNotFound reports whether an error from a Connect call is the server's
// CodeNotFound. The campaign surfaces use it as the "no campaign exists yet"
// signal (#267): GetActiveCampaign fails with CodeNotFound on a fresh, unseeded
// install, which the web turns into a create-first-campaign flow rather than an
// error card. ConnectError.from normalizes any thrown value, so a non-Connect
// error resolves to Code.Unknown and never masquerades as NotFound.
export function isNotFound(error: unknown): boolean {
  return ConnectError.from(error).code === Code.NotFound;
}
