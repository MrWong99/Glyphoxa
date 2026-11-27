import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { en } from "@/i18n";
import { de as sessionDe } from "@/i18n/messages/session";
import { sessionActionErrorMessage } from "./startStopError";
import { tDe, tEn } from "./testT";

// A refusal as the server sends it (internal/rpc/session.go): a code, an
// English sentence, and the machine-readable reason in the error metadata.
function refusal(message: string, code: Code, reason: string): ConnectError {
  return new ConnectError(message, code, new Headers({ "Glyphoxa-Error-Reason": reason }));
}

// Every reason internal/rpc attaches to a Start/Stop refusal (the constants in
// allowance_error.go), with the catalog key it must render.
const SERVER_REASONS = {
  session_active: "session.errSessionActive",
  discord_not_configured: "session.errDiscordNotConfigured",
  bot_token_missing: "session.errBotTokenMissing",
  bot_token_undecryptable: "session.errBotTokenUndecryptable",
  voice_unavailable: "session.errVoiceUnavailable",
  session_limit: "session.errSessionLimit",
  shutting_down: "session.errShuttingDown",
  worker_pending: "session.errWorkerPending",
  start_cancelled: "session.errStartCancelled",
  no_active_session: "session.errNoActiveSession",
  stop_pending: "session.errStopPending",
} as const;

describe("sessionActionErrorMessage", () => {
  it("translates every reason the server marks, in both languages", () => {
    for (const [reason, key] of Object.entries(SERVER_REASONS)) {
      const err = refusal("english server prose", Code.FailedPrecondition, reason);
      expect(sessionActionErrorMessage(err, tEn), reason).toBe(en[key]);
      expect(sessionActionErrorMessage(err, tDe), reason).toBe(sessionDe[key]);
      expect(sessionDe[key], reason).not.toBe(en[key]);
    }
  });

  it("says the session cap in German instead of the server's English sentence", () => {
    const err = refusal(
      "the server is already running the maximum number of concurrent voice sessions; try again once one ends",
      Code.ResourceExhausted,
      "session_limit",
    );
    expect(sessionActionErrorMessage(err, tDe)).toBe(
      "Auf dem Server laufen bereits so viele Voice Sessions, wie er zulässt. Versuche es erneut, sobald eine davon endet.",
    );
  });

  it("shows the plan-allowance refusal as the shared sentence, not as the session cap", () => {
    // Same code as the session cap; only the reason tells them apart.
    const err = refusal(
      "this month's included usage is used up — AI features are paused until next month or a plan change",
      Code.ResourceExhausted,
      "allowance_exhausted",
    );
    expect(sessionActionErrorMessage(err, tEn)).toBe(en["common.errAllowance"]);
  });

  it("keeps the server's message for a reason it does not know", () => {
    const err = refusal("a newer server's refusal", Code.FailedPrecondition, "something_new");
    expect(sessionActionErrorMessage(err, tDe)).toBe("a newer server's refusal");
    // A reason that happens to name an Object.prototype member is still unknown.
    const proto = refusal("odd", Code.FailedPrecondition, "constructor");
    expect(sessionActionErrorMessage(proto, tDe)).toBe("odd");
  });

  it("keeps the message of an error without a reason, without the wire code prefix", () => {
    const noCampaign = new ConnectError("no active campaign", Code.FailedPrecondition);
    expect(sessionActionErrorMessage(noCampaign, tDe)).toBe("no active campaign");
    expect(sessionActionErrorMessage(new Error("network down"), tDe)).toBe("network down");
  });
});
