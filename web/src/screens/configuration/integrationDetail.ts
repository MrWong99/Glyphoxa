import type { MessageKey, TFunc } from "@/i18n";
import { reasonCode } from "@/screens/session/failureText";

// Why the standing Discord integration failed, in the GM's language (#695).
//
// For state "failed" the server sends "<code>: <raw gateway/REST error>"
// (wirenpc.FatalError, ADR-0043) — the same codes a failed Voice Session
// carries (session/failureText.ts), but the sentences differ: the GM is
// already on Setup, so these say what to do HERE, not "under Setup, then
// start again". Only the four codes classifyFatal emits (internal/wirenpc
// gatewayfatal.go) are mapped; anything else is shown exactly as the server
// wrote it — an unknown cause must stay readable.

const INTEGRATION_FAILURE_KEYS: Record<string, MessageKey> = {
  invalid_bot_token: "config.integrationReasonInvalidBotToken",
  disallowed_intents: "config.integrationReasonDisallowedIntents",
  bot_not_authorized: "config.integrationReasonBotNotAuthorized",
  gateway_rejected: "config.integrationReasonGatewayRejected",
};

/**
 * A failed integration's detail as a sentence for the GM: localized for the
 * known codes, verbatim for everything else, "" for no detail at all.
 */
export function integrationFailureText(detail: string | undefined, t: TFunc): string {
  if (!detail) return "";
  const key = INTEGRATION_FAILURE_KEYS[reasonCode(detail)];
  return key ? t(key) : detail;
}
