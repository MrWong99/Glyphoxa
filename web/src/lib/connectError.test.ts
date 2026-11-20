import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { apiErrorMessage, errorMessage, isAllowanceExhausted } from "./connectError";
import type { MessageKey, TFunc } from "@/i18n";

// The allowance refusal as the server sends it (internal/rpc/allowance_error.go):
// ResourceExhausted plus a machine-readable reason in the error metadata.
function allowanceRefusal(): ConnectError {
  return new ConnectError(
    "this month's included usage is used up — AI features are paused until next month or a plan change",
    Code.ResourceExhausted,
    { "Glyphoxa-Error-Reason": "allowance_exhausted" },
  );
}

const t: TFunc = (key: MessageKey) => `«${key}»`;

describe("isAllowanceExhausted", () => {
  it("recognises the server's allowance refusal by its reason, not its wording", () => {
    expect(isAllowanceExhausted(allowanceRefusal())).toBe(true);
  });

  it("does not fire on other ResourceExhausted errors", () => {
    // A provider's own 429 and the concurrent-session cap share the code.
    const providerQuota = new ConnectError(
      "your LLM provider is out of quota or rate-limited — check your plan, or try again shortly",
      Code.ResourceExhausted,
    );
    expect(isAllowanceExhausted(providerQuota)).toBe(false);
  });

  it("does not fire on the reason under a different code, or on a plain Error", () => {
    const wrongCode = new ConnectError("nope", Code.Unavailable, {
      "Glyphoxa-Error-Reason": "allowance_exhausted",
    });
    expect(isAllowanceExhausted(wrongCode)).toBe(false);
    expect(isAllowanceExhausted(new Error("boom"))).toBe(false);
  });
});

describe("apiErrorMessage", () => {
  it("shows the translated allowance sentence instead of the server's English one", () => {
    expect(apiErrorMessage(allowanceRefusal(), t)).toBe("«common.errAllowance»");
  });

  it("falls back to the server's reason, without the wire code prefix", () => {
    const err = new ConnectError("the image provider is having trouble right now", Code.Unavailable);
    expect(apiErrorMessage(err, t)).toBe("the image provider is having trouble right now");
    expect(apiErrorMessage(err, t)).toBe(errorMessage(err));
    expect(apiErrorMessage(err, t)).not.toContain("[unavailable]");
  });
});
