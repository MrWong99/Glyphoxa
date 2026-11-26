import { describe, it, expect } from "vitest";

import { invitationLink, joinOAuthStart, joinPath, snowflakeProblem } from "./invitationLink";

// The URL shapes are a Go↔TS contract with internal/auth (ADR-0065): the
// server mints tokens for /join/<token> and reads `join` and `aup` off the
// OAuth start.
describe("invitation link URLs", () => {
  it("builds the join path and the full link from a token", () => {
    expect(joinPath("tok-moonlit-harbor")).toBe("/join/tok-moonlit-harbor");
    expect(invitationLink("tok-moonlit-harbor", "https://glyphoxa.example")).toBe(
      "https://glyphoxa.example/join/tok-moonlit-harbor",
    );
    // Defaults to the origin the minter is looking at.
    expect(invitationLink("tok-moonlit-harbor")).toBe(`${window.location.origin}/join/tok-moonlit-harbor`);
  });

  it("builds the OAuth start that redeems a token, with the acknowledgment flag", () => {
    expect(joinOAuthStart("tok-moonlit-harbor")).toBe("/auth/discord/login?join=tok-moonlit-harbor&aup=1");
  });

  it("leaves a URL-safe token untouched and escapes anything that is not", () => {
    expect(joinPath("aZ09-_")).toBe("/join/aZ09-_");
    expect(joinOAuthStart("a&b=c")).toBe("/auth/discord/login?join=a%26b%3Dc&aup=1");
  });
});

describe("snowflakeProblem", () => {
  it("accepts 17 to 20 decimal digits, with surrounding space", () => {
    expect(snowflakeProblem("10000000000000001")).toBeNull();
    expect(snowflakeProblem(" 100000000000000042 ")).toBeNull();
    expect(snowflakeProblem("18446744073709551615")).toBeNull();
  });

  it("has nothing to say about an empty field", () => {
    expect(snowflakeProblem("")).toBeNull();
    expect(snowflakeProblem("   ")).toBeNull();
  });

  it("names what is wrong: not digits, or not a snowflake's length", () => {
    expect(snowflakeProblem("mira#1234")).toBe("digits");
    expect(snowflakeProblem("1000 0000")).toBe("digits");
    expect(snowflakeProblem("-100000000000000042")).toBe("digits");
    expect(snowflakeProblem("12345")).toBe("length");
    expect(snowflakeProblem("184467440737095516150")).toBe("length");
  });
});
