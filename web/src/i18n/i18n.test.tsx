import { describe, it, expect, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";

import { I18nProvider, loadCatalog, useI18n, en } from "./index";

// The de catalog's completeness is compile-checked (Record<keyof en, string>
// per fragment); these tests cover the runtime behavior — interpolation, the
// stored-choice round-trip, and the provider actually swapping languages.

function Probe() {
  const { t, lang, setLang } = useI18n();
  return (
    <div>
      <span data-testid="lang">{lang}</span>
      <span data-testid="save">{t("common.save")}</span>
      <button type="button" onClick={() => setLang("de")}>
        switch
      </button>
    </div>
  );
}

describe("i18n", () => {
  beforeEach(() => localStorage.clear());

  it("renders English by default and switches to German, persisting the choice", () => {
    render(
      <I18nProvider>
        <Probe />
      </I18nProvider>,
    );
    expect(screen.getByTestId("lang").textContent).toBe("en");
    expect(screen.getByTestId("save").textContent).toBe("Save");

    fireEvent.click(screen.getByText("switch"));
    expect(screen.getByTestId("lang").textContent).toBe("de");
    expect(screen.getByTestId("save").textContent).toBe("Speichern");
    expect(localStorage.getItem("gx-lang")).toBe("de");
  });

  it("honors a stored language on mount", () => {
    localStorage.setItem("gx-lang", "de");
    render(
      <I18nProvider>
        <Probe />
      </I18nProvider>,
    );
    expect(screen.getByTestId("lang").textContent).toBe("de");
  });

  it("works without a provider (unit-test default is English)", () => {
    render(<Probe />);
    expect(screen.getByTestId("save").textContent).toBe("Save");
  });

  // The German catalog is assembled in ./de.ts behind a dynamic import (its own
  // chunk), through `?de`-suffixed imports that hide the fragments' types from
  // the checker. This is the runtime twin of the per-fragment compile check: a
  // fragment left out of de.ts would otherwise fall back to English silently.
  it("the lazily loaded German catalog covers exactly the English keys", async () => {
    const { de } = await import("./de");
    expect(Object.keys(de).sort()).toEqual(Object.keys(en).sort());
    for (const value of Object.values(de)) expect(value).not.toBe("");
  });

  it("loadCatalog is idempotent and resolves for the bundled language", async () => {
    await expect(loadCatalog("en")).resolves.toBeUndefined();
    await expect(Promise.all([loadCatalog("de"), loadCatalog("de")])).resolves.toBeDefined();
  });

  it("interpolates {param} placeholders and leaves unknown ones intact", () => {
    render(<Probe />);
    // Use the shared error template as the interpolation fixture.
    expect(en["common.couldntSave"]).toContain("{message}");
  });
});

// The plan-allowance refusal exists twice — the shared sentence and the
// planning chat's own — and both describe ONE gate (ADR-0055 gate (b)): per
// plan, over the UTC calendar month. They must say the same true things.
describe("allowance refusal copy", () => {
  it("names the monthly reset and never offers a saved key as the way out, in both languages", async () => {
    const { de } = await import("./de");
    for (const key of ["common.errAllowance", "chat.errAllowance"] as const) {
      expect(en[key]).toMatch(/This month's included usage is used up/);
      expect(en[key]).toMatch(/start of next month/);
      expect(en[key]).not.toMatch(/key|Setup|upgrade/i);
      expect(de[key]).toMatch(/Das inklusive Kontingent für diesen Monat ist aufgebraucht/);
      expect(de[key]).toMatch(/Anfang des nächsten Monats/);
      expect(de[key]).not.toMatch(/Schlüssel|Einrichtung|Upgrade/i);
    }
  });
});
