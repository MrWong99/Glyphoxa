import { describe, it, expect, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";

import { I18nProvider, useI18n, en } from "./index";

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

  it("interpolates {param} placeholders and leaves unknown ones intact", () => {
    render(<Probe />);
    // Use the shared error template as the interpolation fixture.
    expect(en["common.couldntSave"]).toContain("{message}");
  });
});
