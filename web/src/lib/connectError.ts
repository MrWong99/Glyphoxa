import { Code, ConnectError } from "@connectrpc/connect";

// Connect error-code predicates — the one home for `ConnectError.from(...).code`
// checks so screens don't scatter ad-hoc comparisons. ConnectError.from
// normalizes any thrown value; a non-Connect error resolves to Code.Unknown, so
// neither predicate can fire on a plain Error.

// isUnauthenticated reports the server's CodeUnauthenticated — the SPA's
// "no/expired session" signal that drives the boot redirect to /login
// (ADR-0016).
export function isUnauthenticated(error: unknown): boolean {
  return ConnectError.from(error).code === Code.Unauthenticated;
}

// isNotFound reports the server's CodeNotFound. The campaign surfaces use it as
// the "no campaign exists yet" signal (#267): GetActiveCampaign fails with
// CodeNotFound exactly when the Tenant has zero campaigns (resolution otherwise
// falls back to the most-recently-created one), which the web turns into a
// create-first-campaign flow rather than an error card.
export function isNotFound(error: unknown): boolean {
  return ConnectError.from(error).code === Code.NotFound;
}
