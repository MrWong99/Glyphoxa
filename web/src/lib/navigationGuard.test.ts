import { describe, it, expect } from "vitest";

import {
  guardedSearchChanged,
  hasUnsavedEdits,
  registerGuardedSearchKeys,
  registerUnsavedEditsProbe,
} from "./navigationGuard";

describe("unsaved-edits register", () => {
  it("reports dirty while any probe says so, and forgets a removed probe", () => {
    let dirty = false;
    const remove = registerUnsavedEditsProbe(() => dirty);
    expect(hasUnsavedEdits()).toBe(false);
    dirty = true;
    expect(hasUnsavedEdits()).toBe(true);
    remove();
    expect(hasUnsavedEdits()).toBe(false);
  });
});

describe("guarded selection keys", () => {
  it("ignores every search change while nothing is declared", () => {
    expect(guardedSearchChanged({ node: "a" }, { node: "b" })).toBe(false);
    expect(guardedSearchChanged({ node: "a" }, {})).toBe(false);
  });

  it("flags a change of a declared key, and only of a declared key", () => {
    const release = registerGuardedSearchKeys(["node"]);
    try {
      expect(guardedSearchChanged({ node: "a" }, { node: "b" })).toBe(true);
      // Clearing the selection replaces the editor just the same.
      expect(guardedSearchChanged({ node: "a" }, {})).toBe(true);
      expect(guardedSearchChanged(undefined, { node: "a" })).toBe(true);
      // Same selection, another key rewritten: nothing is unmounted.
      expect(guardedSearchChanged({ node: "a" }, { node: "a", line: "7" })).toBe(false);
      expect(guardedSearchChanged({ node: "a", thread: "t1" }, { node: "a", thread: "t2" })).toBe(false);
    } finally {
      release();
    }
    expect(guardedSearchChanged({ node: "a" }, { node: "b" })).toBe(false);
  });

  it("keeps a key guarded until its LAST declarer is gone", () => {
    const first = registerGuardedSearchKeys(["map"]);
    const second = registerGuardedSearchKeys(["map", "thread"]);
    first();
    // A replayed release must not eat the second declaration.
    first();
    expect(guardedSearchChanged({ map: "m1" }, { map: "m2" })).toBe(true);
    second();
    expect(guardedSearchChanged({ map: "m1" }, { map: "m2" })).toBe(false);
    expect(guardedSearchChanged({ thread: "t1" }, {})).toBe(false);
  });
});
