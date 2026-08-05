import { describe, it, expect } from "vitest";

import { OPERATOR, operatorTodos, isPlaceholder } from "./operator";

// The pre-deploy check an operator runs before the instance is reachable:
//
//     cd web && npm run check:operator
//
// It calls the same operatorTodos() the legal pages call for their red "Hinweis
// an den Betreiber" banner, so a green run here and a clean page are the same
// fact rather than two things that can drift.
//
// It replaces the grep over the built bundle that docs/deploy/legal-pages.md
// used to prescribe. That grep looked for "BITTE AUSFÜLLEN", which is
// PLACEHOLDER_PREFIX — a constant compiled into EVERY bundle whether or not any
// field uses it, so the check fired on correctly filled deployments and trained
// operators to ignore it. A grep also cannot see a required field left EMPTY,
// which is every bit as unfilled as one still carrying template text.
//
// Nothing in this file may assert the canonical deployment's own values: a fork
// that correctly filled in its OWN identity has to pass it unchanged. The
// shipped identity is pinned in legal.test.tsx instead.

describe("operator identity (npm run check:operator)", () => {
  it("has every required field filled in", () => {
    const todos = operatorTodos();
    expect(
      todos,
      `unfilled required fields in web/src/screens/legal/operator.ts: ${todos.join(", ")}`,
    ).toEqual([]);
  });

  it("leaves no field — required or optional — on a placeholder value", () => {
    // operatorTodos ignores the optional fields, because an EMPTY optional
    // field is a decision (no phone, no VAT id, no DPO, reached directly). A
    // PLACEHOLDER one is not: it renders on the page as red template text
    // without raising the banner, so it is caught here instead.
    const marked = Object.entries(OPERATOR)
      .filter(([, value]) => isPlaceholder(value))
      .map(([field]) => field);
    expect(marked, `fields still on template text: ${marked.join(", ")}`).toEqual([]);
  });

  it("counts an empty required field as a TODO and ignores optional ones", () => {
    const filled = {
      ...OPERATOR,
      legalName: "Rin Okabe",
      street: "Beispielweg 1",
      city: "10115 Berlin",
      country: "Deutschland",
      email: "hallo@example.org",
      supervisoryAuthority: "Berliner Beauftragte für Datenschutz",
      hostingLocation: "Deutschland",
      lastUpdated: "2026-07-25",
      phone: "", // optional — an empty value is a decision, not an omission
    };
    expect(operatorTodos(filled)).toEqual([]);
    expect(operatorTodos({ ...filled, email: "  " })).toEqual(["email"]);
    expect(
      operatorTodos({ ...filled, street: "[BITTE AUSFÜLLEN: Straße und Hausnummer]" }),
    ).toEqual(["street"]);
  });
});
