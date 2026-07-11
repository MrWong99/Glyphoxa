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

// failedPreconditionMessage returns the server's message when the error is a
// CodeFailedPrecondition, else null. The Knowledge Proposal review surface (#300)
// uses it to show a rejected-approval's actionable reason ("no wiki entry named
// …") inline on the card, distinct from an unexpected failure. ConnectError.from
// strips the "[failed_precondition] " prefix into .rawMessage, the verbatim
// server reason.
export function failedPreconditionMessage(error: unknown): string | null {
  const ce = ConnectError.from(error);
  return ce.code === Code.FailedPrecondition ? ce.rawMessage : null;
}

// isNotFound reports the server's CodeNotFound. The campaign surfaces use it as
// the "no campaign exists yet" signal (#267): GetActiveCampaign fails with
// CodeNotFound exactly when the Tenant has zero campaigns (resolution otherwise
// falls back to the most-recently-created one), which the web turns into a
// create-first-campaign flow rather than an error card.
export function isNotFound(error: unknown): boolean {
  return ConnectError.from(error).code === Code.NotFound;
}
