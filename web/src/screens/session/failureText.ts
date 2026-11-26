import type { MessageKey, TFunc } from "@/i18n";

// Why a Voice Session failed or ended by itself, in the GM's language (#695).
//
// The server records the cause as "<code>: <English prose>" (ADR-0043): a
// classified gateway rejection on a "failed" session (internal/wirenpc
// gatewayfatal.go, connect.go), the boot reconciliation's "orphaned" (#143),
// or a policy stop on an "ended" one (spend caps, ADR-0046; Idle Close,
// internal/idleclose). The code is the contract; the prose after it is a log
// line. So the known codes render a sentence that says what happened AND what
// to do next, and anything else is shown exactly as the server wrote it — an
// unknown cause must stay readable, never be swallowed into "something went
// wrong".

const FAILURE_KEYS: Record<string, MessageKey> = {
  // A "failed" session: the Bot never got in, or lost the right to be there.
  invalid_bot_token: "session.reasonInvalidBotToken",
  disallowed_intents: "session.reasonDisallowedIntents",
  bot_not_authorized: "session.reasonBotNotAuthorized",
  gateway_rejected: "session.reasonGatewayRejected",
  voice_join_timeout: "session.reasonVoiceJoinTimeout",
  orphaned: "session.reasonOrphaned",
  // An "ended" session that stopped by itself.
  spend_cap_hard: "session.reasonSpendCapHard",
  allowance_exhausted: "session.reasonAllowanceExhausted",
  idle_no_audio: "session.reasonIdleNoAudio",
  resource_ceiling: "session.reasonResourceCeiling",
  reconnect_churn: "session.reasonReconnectChurn",
  media_path_dead: "session.reasonMediaPathDead",
};

// The codes whose fix lives on the Setup screen (the Bot token, the Bot's
// membership of the server), so the screen can offer the way there.
const SETUP_CODES = new Set(["invalid_bot_token", "disallowed_intents", "bot_not_authorized"]);

/**
 * The machine code a reason starts with, or "" when it has none. A code is the
 * run of lower-case letters and underscores before the first colon; a reason
 * that is only a code ("voice_join_timeout") is its own code.
 */
export function reasonCode(reason: string | undefined): string {
  if (!reason) return "";
  const head = reason.split(":", 1)[0].trim();
  return /^[a-z][a-z0-9_]*$/.test(head) ? head : "";
}

/**
 * A reason as a sentence for the GM: localized for the known codes, verbatim
 * for everything else, "" for no reason at all.
 */
export function failureText(reason: string | undefined, t: TFunc): string {
  if (!reason) return "";
  const key = FAILURE_KEYS[reasonCode(reason)];
  return key ? t(key) : reason;
}

/** True when the reason is one the GM fixes on the Setup screen. */
export function reasonNeedsSetup(reason: string | undefined): boolean {
  return SETUP_CODES.has(reasonCode(reason));
}
