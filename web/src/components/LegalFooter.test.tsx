import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";

import { LegalFooter } from "./LegalFooter";

// #518 AC1: the footer links the three legal documents and renders on EVERY
// screen — including the pre-session ones. Mounted here with no router and no
// Providers, which is the property that lets it sit on the login/signup cards
// and inside the app shell alike.

describe("LegalFooter", () => {
  it("links Impressum, Datenschutz and Nutzungsbedingungen", () => {
    render(<LegalFooter />);
    expect(screen.getByRole("link", { name: "Impressum" })).toHaveAttribute("href", "/imprint");
    expect(screen.getByRole("link", { name: "Datenschutz" })).toHaveAttribute("href", "/privacy");
    expect(screen.getByRole("link", { name: "Nutzungsbedingungen" })).toHaveAttribute(
      "href",
      "/terms",
    );
  });

  it("is a labelled landmark so it is reachable by assistive tech", () => {
    render(<LegalFooter />);
    expect(screen.getByRole("navigation", { name: "Rechtliches" })).toBeInTheDocument();
  });

  it("keeps its own class when the caller adds one", () => {
    const { container } = render(<LegalFooter className="gx-legal__footer" />);
    expect(container.firstChild).toHaveClass("gx-legalfooter", "gx-legal__footer");
  });
});
