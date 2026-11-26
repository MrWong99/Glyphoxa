import { describe, it, expect } from "vitest";

import { flushPendingWrites, hasPendingWrites, registerPendingWrites } from "./pendingWrites";

describe("pendingWrites", () => {
  it("lists a held-back write until it is released", () => {
    expect(hasPendingWrites()).toBe(false);
    const release = registerPendingWrites(async () => {});
    expect(hasPendingWrites()).toBe(true);
    release();
    expect(hasPendingWrites()).toBe(false);
  });

  it("sends every held-back write and settles even when one fails", async () => {
    const sent: string[] = [];
    const a = registerPendingWrites(async () => {
      sent.push("a");
    });
    const b = registerPendingWrites(async () => {
      sent.push("b");
      throw new Error("refused");
    });
    await expect(flushPendingWrites()).resolves.toBeUndefined();
    expect(sent.sort()).toEqual(["a", "b"]);
    a();
    b();
  });
});
