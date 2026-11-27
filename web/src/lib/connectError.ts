import { Code, ConnectError } from "@connectrpc/connect";

import type { TFunc } from "@/i18n";

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

// errorMessage renders any thrown value as a display string: a ConnectError's
// rawMessage (the server's reason WITHOUT the "[failed_precondition] " wire
// prefix — gRPC code slugs are internal vocabulary, not user copy), a plain
// Error's message, anything else via String(). The one home for the
// `instanceof Error` narrowing the mutation error handlers used to each
// hand-roll.
export function errorMessage(err: unknown): string {
  if (err instanceof ConnectError) return err.rawMessage;
  return err instanceof Error ? err.message : String(err);
}

// ERROR_REASON_HEADER carries a stable, machine-readable refusal reason in a
// Connect error's metadata (internal/rpc/allowance_error.go). It exists for
// refusals whose code alone is ambiguous: ResourceExhausted is the plan
// allowance, a provider's own 429 AND the concurrent-session cap, and each
// needs a different sentence.
const ERROR_REASON_HEADER = "Glyphoxa-Error-Reason";

// errorReason returns the refusal's machine-readable reason, or "" when the
// error carries none (an unclassified failure, a non-Connect error). Match on
// it, never on the English message, so the server can reword freely.
export function errorReason(error: unknown): string {
  return ConnectError.from(error).metadata.get(ERROR_REASON_HEADER) ?? "";
}

// isAllowanceExhausted reports the server's "this month's plan allowance is
// spent" refusal (ADR-0055 gate (b)), which every AI feature that spends
// provider money can return (#658). It matches the reason, never the English
// message, so the server can reword without breaking this.
export function isAllowanceExhausted(error: unknown): boolean {
  const ce = ConnectError.from(error);
  return ce.code === Code.ResourceExhausted && errorReason(ce) === "allowance_exhausted";
}

// apiErrorMessage is errorMessage for the surfaces in front of a paid AI call:
// a refusal the server marks with a known reason is shown as OUR translated
// sentence (the server's message is English only), anything else as the
// server's own reason. Use it wherever a generate / recap / preview failure is
// rendered, so every one of them says the same thing in the GM's language.
export function apiErrorMessage(err: unknown, t: TFunc): string {
  if (isAllowanceExhausted(err)) return t("common.errAllowance");
  return errorMessage(err);
}

// isNotFound reports the server's CodeNotFound. The campaign surfaces use it as
// the "no campaign exists yet" signal (#267): GetActiveCampaign fails with
// CodeNotFound exactly when the Tenant has zero campaigns (resolution otherwise
// falls back to the most-recently-created one), which the web turns into a
// create-first-campaign flow rather than an error card.
/**
 * isPermissionDenied is true when the server refused the call for THIS
 * principal (CodePermissionDenied) — for a Campaign Guest, a procedure their
 * Guest Rights do not cover (ADR-0065).
 */
export function isPermissionDenied(error: unknown): boolean {
  return ConnectError.from(error).code === Code.PermissionDenied;
}

export function isNotFound(error: unknown): boolean {
  return ConnectError.from(error).code === Code.NotFound;
}
