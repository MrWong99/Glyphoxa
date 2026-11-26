import { describe, it, expect } from "vitest";

import { failureText, reasonCode, reasonNeedsSetup } from "./failureText";
import { tDe, tEn } from "./testT";

// The reasons exactly as the server writes them (internal/wirenpc
// gatewayfatal.go + connect.go, internal/storage models.go, internal/session
// manager.go, internal/idleclose idleclose.go).
const SERVER_REASONS: Record<string, string> = {
  invalid_bot_token: "invalid_bot_token: wirenpc: open gateway: websocket: close 4004: Authentication failed",
  disallowed_intents: "disallowed_intents: wirenpc: open gateway: websocket: close 4014: Disallowed intent(s)",
  bot_not_authorized: "bot_not_authorized: wirenpc: join voice: 403 Forbidden",
  gateway_rejected: "gateway_rejected: wirenpc: open gateway: websocket: close 4012: Invalid API version",
  voice_join_timeout: "voice_join_timeout: wirenpc: voice join timed out 3 times in a row",
  orphaned: "orphaned: reconciled at startup",
  spend_cap_hard: "spend_cap_hard: estimated spend crossed the hard cap",
  allowance_exhausted: "allowance_exhausted: estimated spend crossed the plan's monthly allowance",
  idle_no_audio: "idle_no_audio: the Voice Session processed no audio for the idle close window",
  resource_ceiling: "resource_ceiling: the Voice Instance crossed its configured resource ceiling",
  reconnect_churn: "reconnect_churn: the Voice Session exceeded its reconnect-cycle ceiling",
  media_path_dead: "media_path_dead: the Voice Session received no audio while participants kept speaking",
};

describe("reasonCode", () => {
  it("reads the machine code in front of the colon", () => {
    expect(reasonCode("invalid_bot_token: close 4004")).toBe("invalid_bot_token");
    expect(reasonCode("voice_join_timeout")).toBe("voice_join_timeout");
  });

  it("finds no code in prose, in an empty reason, or in a sentence with a colon", () => {
    expect(reasonCode("")).toBe("");
    expect(reasonCode(undefined)).toBe("");
    expect(reasonCode("The Bot left: nobody was there")).toBe("");
    expect(reasonCode("Something went wrong")).toBe("");
  });
});

describe("failureText", () => {
  it("renders a sentence for every code the server classifies, in English and German", () => {
    for (const [code, reason] of Object.entries(SERVER_REASONS)) {
      const english = failureText(reason, tEn);
      const german = failureText(reason, tDe);
      // A sentence of its own — not the code, not the server's log prose.
      expect(english, code).not.toContain(code);
      expect(english, code).not.toBe(reason);
      expect(english.length, code).toBeGreaterThan(20);
      expect(german, code).not.toBe(english);
      expect(german, code).not.toContain(code);
    }
  });

  it("gives the next step for a rejected Bot token", () => {
    expect(failureText(SERVER_REASONS.invalid_bot_token, tEn)).toBe(
      "Discord rejected the Bot token. Paste a current token under Setup, then start again.",
    );
  });

  it("names the idle watchdog's end in plain words", () => {
    expect(failureText(SERVER_REASONS.idle_no_audio, tEn)).toBe(
      "It ended by itself because nobody spoke for a while.",
    );
  });

  it("shows a reason it does not know exactly as the server wrote it", () => {
    const unknown = "loop_error: wirenpc: encoder: opus: buffer too small";
    expect(failureText(unknown, tEn)).toBe(unknown);
    expect(failureText(unknown, tDe)).toBe(unknown);
    expect(failureText("plain prose with no code", tEn)).toBe("plain prose with no code");
  });

  it("is empty for no reason", () => {
    expect(failureText("", tEn)).toBe("");
    expect(failureText(undefined, tEn)).toBe("");
  });
});

describe("reasonNeedsSetup", () => {
  it("is true only for the causes fixed on the Setup screen", () => {
    expect(reasonNeedsSetup(SERVER_REASONS.invalid_bot_token)).toBe(true);
    expect(reasonNeedsSetup(SERVER_REASONS.disallowed_intents)).toBe(true);
    expect(reasonNeedsSetup(SERVER_REASONS.bot_not_authorized)).toBe(true);
    expect(reasonNeedsSetup(SERVER_REASONS.idle_no_audio)).toBe(false);
    expect(reasonNeedsSetup(SERVER_REASONS.voice_join_timeout)).toBe(false);
    expect(reasonNeedsSetup("loop_error: boom")).toBe(false);
    expect(reasonNeedsSetup(undefined)).toBe(false);
  });
});
