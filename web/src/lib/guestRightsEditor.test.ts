import { describe, it, expect } from "vitest";

import { GuestRight as R } from "@gen/glyphoxa/management/v1/management_pb";
import {
  GUEST_RIGHTS,
  GUEST_RIGHT_GROUPS,
  GUEST_RIGHT_PRESETS,
  lockedGuestRights,
  matchingPreset,
  normalizeGuestRights,
  sameGuestRights,
  toggleGuestRight,
} from "./guestRights";

// The rules of the Guest Rights editor (ADR-0065). The server normalizes the
// stored set; the editor mirrors that so a GM sees the implied rights ticked
// BEFORE saving. If the two ever disagree, a saved set would come back
// different from what was on screen — these pin the web's half.
describe("guest right implications", () => {
  it.each([
    ["wiki.edit", [R.WIKI_EDIT], [R.WIKI_READ, R.WIKI_SECRETS, R.WIKI_EDIT]],
    ["maps.edit", [R.MAPS_EDIT], [R.MAPS_READ, R.MAPS_EDIT]],
    ["planning.use", [R.PLANNING_USE], [R.WIKI_READ, R.WIKI_SECRETS, R.PLANNING_USE]],
    ["a right that implies nothing", [R.TRANSCRIPTS_READ], [R.TRANSCRIPTS_READ]],
    ["wiki.secrets alone (the server accepts it)", [R.WIKI_SECRETS], [R.WIKI_SECRETS]],
  ])("normalizes %s", (_name, input, want) => {
    expect(normalizeGuestRights(input)).toEqual(want);
  });

  it("puts a set in display order and drops duplicates and unknown rights", () => {
    expect(
      normalizeGuestRights([R.HIGHLIGHTS_READ, R.WIKI_READ, R.WIKI_READ, 99 as R, R.UNSPECIFIED]),
    ).toEqual([R.WIKI_READ, R.HIGHLIGHTS_READ]);
  });

  it("locks an implied right and names every right that holds it", () => {
    const locked = lockedGuestRights([R.WIKI_EDIT, R.PLANNING_USE, R.MAPS_EDIT]);
    expect(locked.get(R.WIKI_READ)).toEqual([R.WIKI_EDIT, R.PLANNING_USE]);
    expect(locked.get(R.WIKI_SECRETS)).toEqual([R.WIKI_EDIT, R.PLANNING_USE]);
    expect(locked.get(R.MAPS_READ)).toEqual([R.MAPS_EDIT]);
    // A right nothing needs stays free to clear.
    expect(locked.has(R.WIKI_EDIT)).toBe(false);
    expect(lockedGuestRights([R.WIKI_READ, R.WIKI_SECRETS]).size).toBe(0);
  });
});

describe("toggling a guest right", () => {
  it("ticking a right ticks what it implies", () => {
    expect(toggleGuestRight([], R.WIKI_EDIT, true)).toEqual([R.WIKI_READ, R.WIKI_SECRETS, R.WIKI_EDIT]);
    expect(toggleGuestRight([R.HIGHLIGHTS_READ], R.MAPS_EDIT, true)).toEqual([
      R.MAPS_READ,
      R.MAPS_EDIT,
      R.HIGHLIGHTS_READ,
    ]);
  });

  it("refuses to clear a right another one needs", () => {
    const set = [R.WIKI_READ, R.WIKI_SECRETS, R.WIKI_EDIT];
    expect(toggleGuestRight(set, R.WIKI_READ, false)).toEqual(set);
    expect(toggleGuestRight(set, R.WIKI_SECRETS, false)).toEqual(set);
  });

  it("clearing the right that held them leaves the implied rights ticked but free", () => {
    const after = toggleGuestRight([R.WIKI_READ, R.WIKI_SECRETS, R.WIKI_EDIT], R.WIKI_EDIT, false);
    expect(after).toEqual([R.WIKI_READ, R.WIKI_SECRETS]);
    expect(toggleGuestRight(after, R.WIKI_SECRETS, false)).toEqual([R.WIKI_READ]);
  });

  it("treats GM secrets as a refinement of reading: it brings reading, and goes with it", () => {
    expect(toggleGuestRight([], R.WIKI_SECRETS, true)).toEqual([R.WIKI_READ, R.WIKI_SECRETS]);
    expect(toggleGuestRight([R.WIKI_READ, R.WIKI_SECRETS, R.MAPS_READ], R.WIKI_READ, false)).toEqual([
      R.MAPS_READ,
    ]);
  });

  it("compares sets by what they grant, not by order", () => {
    expect(sameGuestRights([R.MAPS_READ, R.WIKI_READ], [R.WIKI_READ, R.MAPS_READ])).toBe(true);
    // Implied rights count: [maps.edit] IS [maps.read, maps.edit].
    expect(sameGuestRights([R.MAPS_EDIT], [R.MAPS_READ, R.MAPS_EDIT])).toBe(true);
    expect(sameGuestRights([R.MAPS_READ], [R.MAPS_READ, R.MAPS_EDIT])).toBe(false);
  });
});

describe("guest right presets", () => {
  const preset = (id: string) => normalizeGuestRights(GUEST_RIGHT_PRESETS.find((p) => p.id === id)!.rights);

  it("Player reads the wiki, views maps and highlights — no secrets, no transcripts", () => {
    expect(preset("player")).toEqual([R.WIKI_READ, R.MAPS_READ, R.HIGHLIGHTS_READ]);
  });

  it("Reader is Player plus the transcripts", () => {
    expect(preset("reader")).toEqual([R.WIKI_READ, R.MAPS_READ, R.TRANSCRIPTS_READ, R.HIGHLIGHTS_READ]);
  });

  it("Co-GM is every right there is", () => {
    expect(preset("coGm")).toEqual(GUEST_RIGHTS.map((r) => r.right));
  });

  it("recognizes a set that is exactly a preset, and only then", () => {
    expect(matchingPreset([R.HIGHLIGHTS_READ, R.MAPS_READ, R.WIKI_READ])?.id).toBe("player");
    expect(matchingPreset([R.WIKI_READ, R.MAPS_READ])).toBeUndefined();
    expect(matchingPreset([])).toBeUndefined();
  });
});

describe("guest right groups", () => {
  it("offer every right exactly once", () => {
    const offered = GUEST_RIGHT_GROUPS.flatMap((g) => g.choices.map((c) => c.right));
    expect([...offered].sort()).toEqual(GUEST_RIGHTS.map((r) => r.right).sort());
  });
});
