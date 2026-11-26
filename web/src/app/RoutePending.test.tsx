import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";

import { I18nProvider } from "@/i18n";
import { RoutePending } from "./RouteFallbacks";

// What shows while a lazily loaded screen's code is on its way. It used to be
// the AuthGate's full-viewport boot placeholder — inside the shell, a 100vh
// blank under the topbar.

describe("RoutePending", () => {
  it("is the outline of a page in the content column, announced once as loading", () => {
    const { container } = render(<RoutePending />);
    // One status region for a screen reader, however many bars are drawn.
    const status = screen.getAllByRole("status");
    expect(status).toHaveLength(1);
    expect(status[0]).toHaveAccessibleName("Loading…");
    expect(status[0]).toHaveAttribute("aria-busy", "true");
    // The page container every screen uses, so nothing jumps when it arrives.
    expect(container.querySelector(".gx-page")).toBeInTheDocument();
    expect(container.querySelectorAll(".gx-skeleton").length).toBeGreaterThan(4);
    // Not the boot placeholder, which claims the whole viewport.
    expect(container.querySelector(".gx-auth-boot")).not.toBeInTheDocument();
  });

  it("is localized", () => {
    localStorage.setItem("gx-lang", "de");
    try {
      render(
        <I18nProvider>
          <RoutePending />
        </I18nProvider>,
      );
      expect(screen.getByRole("status")).toHaveAccessibleName("Wird geladen…");
    } finally {
      localStorage.removeItem("gx-lang");
    }
  });
});
