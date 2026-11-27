import type { MessageKey, TFunc } from "@/i18n";
import { errorMessage, errorReason } from "@/lib/connectError";

// Why a Start or Stop was refused, in the GM's language (#695).
//
// The server's message is an English sentence, so every refusal it classifies
// also carries a stable reason in the error metadata (internal/rpc/session.go,
// the constants in allowance_error.go). The reason is the contract: the known
// ones render our translated sentence, and anything else — an unclassified
// failure, a reason this build does not know yet — is shown exactly as the
// server wrote it, so an unknown cause stays readable.

const REASON_KEYS = new Map<string, MessageKey>([
  // StartSession.
  ["session_active", "session.errSessionActive"],
  ["discord_not_configured", "session.errDiscordNotConfigured"],
  ["bot_token_missing", "session.errBotTokenMissing"],
  ["bot_token_undecryptable", "session.errBotTokenUndecryptable"],
  ["voice_unavailable", "session.errVoiceUnavailable"],
  // The plan-allowance refusal: the one sentence every paid surface shows.
  ["allowance_exhausted", "common.errAllowance"],
  ["session_limit", "session.errSessionLimit"],
  ["shutting_down", "session.errShuttingDown"],
  ["worker_pending", "session.errWorkerPending"],
  ["start_cancelled", "session.errStartCancelled"],
  // StopSession.
  ["no_active_session", "session.errNoActiveSession"],
  ["stop_pending", "session.errStopPending"],
]);

/**
 * A failed Start/Stop as a sentence for the GM: localized for the reasons the
 * server marks, the server's own message for everything else.
 */
export function sessionActionErrorMessage(err: unknown, t: TFunc): string {
  const key = REASON_KEYS.get(errorReason(err));
  return key ? t(key) : errorMessage(err);
}
