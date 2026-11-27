import { describe, it, expect } from "vitest";

import { en, type TFunc } from "@/i18n";
import { de as configDe } from "@/i18n/messages/configuration";
import { integrationFailureText } from "./integrationDetail";

const tEn: TFunc = (key) => en[key];
const tDe: TFunc = (key) => (configDe as Record<string, string>)[key] ?? en[key];

// The details exactly as the server writes them for a "failed" integration:
// wirenpc.FatalError.Error() over the four reasons classifyFatal can emit
// (internal/wirenpc gatewayfatal.go).
const SERVER_DETAILS: Record<string, string> = {
  invalid_bot_token: "invalid_bot_token: gateway rejected identify (close 4004)",
  disallowed_intents: "disallowed_intents: wirenpc: open gateway: websocket: close 4014: Disallowed intent(s)",
  bot_not_authorized: "bot_not_authorized: GET /users/@me: 403 Forbidden",
  gateway_rejected: "gateway_rejected: wirenpc: open gateway: websocket: close 4012: Invalid API version",
};

describe("integrationFailureText", () => {
  it("renders a sentence for every code the server classifies, in English and German", () => {
    for (const [code, detail] of Object.entries(SERVER_DETAILS)) {
      const english = integrationFailureText(detail, tEn);
      const german = integrationFailureText(detail, tDe);
      for (const text of [english, german]) {
        expect(text, code).not.toBe("");
        expect(text, code).not.toContain(code);
        expect(text, code).not.toMatch(/wirenpc|websocket|close 4\d\d\d|403/);
      }
      expect(german, code).not.toBe(english);
    }
  });

  it("says what to do on the Setup screen itself, not to go there", () => {
    expect(integrationFailureText(SERVER_DETAILS.invalid_bot_token, tEn)).toBe(
      "Discord rejected the bot token. Paste a current token under “Use your own Discord bot” below.",
    );
    for (const detail of Object.values(SERVER_DETAILS)) {
      expect(integrationFailureText(detail, tEn)).not.toMatch(/under setup|start again/i);
      expect(integrationFailureText(detail, tDe)).not.toMatch(/unter einrichtung|starte/i);
    }
  });

  it("keeps a detail it has no sentence for exactly as the server wrote it", () => {
    expect(integrationFailureText("something_new: boom", tEn)).toBe("something_new: boom");
    expect(integrationFailureText("The Bot gave up: nobody answered", tDe)).toBe("The Bot gave up: nobody answered");
    // A Voice Session code is not an integration failure: no borrowed sentence.
    expect(integrationFailureText("voice_join_timeout: 3 times", tEn)).toBe("voice_join_timeout: 3 times");
    expect(integrationFailureText("", tEn)).toBe("");
  });
});
