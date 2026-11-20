import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import { consumeReturnTo, rememberReturnTo, safeReturnPath } from "./returnTo";

beforeEach(() => sessionStorage.clear());
afterEach(() => vi.useRealTimers());

describe("safeReturnPath", () => {
  it("keeps an in-app path with its query and hash", () => {
    expect(safeReturnPath("/t/default/campaign/knowledge?node=n1#x")).toBe(
      "/t/default/campaign/knowledge?node=n1#x",
    );
  });

  it.each([
    ["an absolute URL", "https://evil.example/t/default/session"],
    ["a protocol-relative URL", "//evil.example/t/default/session"],
    ["a backslash host trick", "/\\evil.example/t/x"],
    ["a scheme", "javascript:alert(1)"],
    ["a path outside the app shell", "/login"],
    ["the root", "/"],
    ["a dot-segment escape out of the shell", "/t/../login"],
    ["a non-string", 42],
    ["nothing", undefined],
  ])("rejects %s", (_name, raw) => {
    expect(safeReturnPath(raw)).toBeNull();
  });
});

describe("return path round trip", () => {
  it("hands the parked path back exactly once", () => {
    rememberReturnTo("/t/default/session?session=s1");
    expect(consumeReturnTo()).toBe("/t/default/session?session=s1");
    expect(consumeReturnTo()).toBeNull();
  });

  it("never parks an unsafe path", () => {
    rememberReturnTo("https://evil.example/");
    expect(consumeReturnTo()).toBeNull();
  });

  it("re-validates on the way out: a tampered entry is dropped", () => {
    sessionStorage.setItem("gx-return-to", JSON.stringify({ path: "//evil.example/", at: Date.now() }));
    expect(consumeReturnTo()).toBeNull();
    sessionStorage.setItem("gx-return-to", "not json");
    expect(consumeReturnTo()).toBeNull();
  });

  it("expires, so a stale entry cannot redirect a much later visit", () => {
    vi.useFakeTimers();
    rememberReturnTo("/t/default/session");
    vi.advanceTimersByTime(16 * 60 * 1000);
    expect(consumeReturnTo()).toBeNull();
  });
});
