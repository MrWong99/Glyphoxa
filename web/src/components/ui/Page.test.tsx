import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Button } from "./Button";
import { Page, PageHeader } from "./Page";

describe("Page", () => {
  it("is the wide container by default and narrow on request", () => {
    const { rerender } = render(<Page data-testid="page">content</Page>);
    expect(screen.getByTestId("page")).toHaveClass("gx-page");
    expect(screen.getByTestId("page")).not.toHaveClass("gx-page--narrow");

    rerender(
      <Page data-testid="page" width="narrow" className="gx-settings">
        content
      </Page>,
    );
    expect(screen.getByTestId("page")).toHaveClass("gx-page", "gx-page--narrow", "gx-settings");
  });
});

describe("PageHeader", () => {
  it("renders eyebrow, the page's h1, description and actions", () => {
    render(
      <PageHeader
        eyebrow="Campaign"
        title="Settings"
        description="Language and system."
        actions={<Button>Save changes</Button>}
      />,
    );
    const heading = screen.getByRole("heading", { level: 1, name: "Settings" });
    expect(heading.closest("header")).toHaveClass("gx-page-header");
    expect(screen.getByText("Campaign")).toHaveClass("gx-overline");
    expect(screen.getByText("Language and system.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save changes" })).toBeInTheDocument();
  });

  it("omits the optional parts and can title with an h2", () => {
    const { container } = render(<PageHeader title="Cast" titleAs="h2" />);
    expect(screen.getByRole("heading", { level: 2, name: "Cast" })).toBeInTheDocument();
    expect(container.querySelector(".gx-overline")).toBeNull();
    expect(container.querySelector(".gx-page-header__desc")).toBeNull();
    expect(container.querySelector(".gx-page-header__actions")).toBeNull();
  });
});
