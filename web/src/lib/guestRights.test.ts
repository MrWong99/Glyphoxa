import { describe, it, expect } from "vitest";

import { GuestRight } from "@gen/glyphoxa/management/v1/management_pb";
import { en } from "@/i18n";
import * as access from "@/i18n/messages/access";
import { GUEST_RIGHTS, describeGuestRights } from "./guestRights";

// The Guest Right vocabulary is a proto enum the server owns (ADR-0065). If a
// right is added there without a row here, an invitation would grant something
// the join page does not mention — these pin the two in step.
describe("guest rights", () => {
  it("has exactly one row per Guest Right of the contract", () => {
    const contract = Object.values(GuestRight).filter(
      (v): v is GuestRight => typeof v === "number" && v !== GuestRight.UNSPECIFIED,
    );
    expect(GUEST_RIGHTS.map((r) => r.right).sort()).toEqual([...contract].sort());
  });

  it("gives every right a distinct label and a sentence, in English and German", () => {
    // The rows are typed by the whole catalog's keys; the German half is read
    // from the fragment that holds them (the assembled German catalog is a
    // lazily loaded chunk).
    const catalogs: Record<string, string>[] = [en, access.de];
    for (const catalog of catalogs) {
      const labels = GUEST_RIGHTS.map((r) => catalog[r.label]);
      for (const label of labels) expect(label).toMatch(/\S/);
      expect(new Set(labels).size).toBe(labels.length);
      for (const r of GUEST_RIGHTS) expect(catalog[r.description]).toMatch(/\.$/);
    }
  });

  it("describes a set in reading order, whatever order it arrives in", () => {
    const rows = describeGuestRights([GuestRight.PLANNING_USE, GuestRight.MAPS_READ, GuestRight.WIKI_READ]);
    expect(rows.map((r) => r.right)).toEqual([GuestRight.WIKI_READ, GuestRight.MAPS_READ, GuestRight.PLANNING_USE]);
  });

  it("leaves out a right this build does not know rather than showing a number", () => {
    expect(describeGuestRights([GuestRight.UNSPECIFIED, 999 as GuestRight])).toEqual([]);
  });
});

// Every key of the access fragment exists in both languages (also checked for
// the assembled catalogs by i18n.test.tsx; this names the fragment when it
// fails) and keeps its placeholders, so "{name}" is never lost in translation.
describe("access messages", () => {
  it("has the same keys in English and German, none empty", () => {
    expect(Object.keys(access.de).sort()).toEqual(Object.keys(access.en).sort());
    for (const value of Object.values(access.de)) expect(value).not.toBe("");
  });

  it("keeps every {placeholder} in the German string", () => {
    const placeholders = (s: string) => (s.match(/\{\w+\}/g) ?? []).sort();
    for (const key of Object.keys(access.en) as (keyof typeof access.en)[]) {
      expect(placeholders(access.de[key]), key).toEqual(placeholders(access.en[key]));
    }
  });
});
