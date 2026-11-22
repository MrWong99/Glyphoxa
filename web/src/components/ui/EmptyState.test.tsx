import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { Button } from "./Button";
import { EmptyState } from "./EmptyState";

describe("EmptyState", () => {
  it("renders title, description and a working action", () => {
    const onCreate = vi.fn();
    render(
      <EmptyState
        icon={<svg data-testid="icon" />}
        title="No NPCs yet"
        description="NPCs are the characters your players talk to."
        action={<Button onClick={onCreate}>New NPC</Button>}
      />,
    );
    expect(screen.getByText("No NPCs yet")).toHaveClass("gx-empty__title");
    expect(screen.getByText("NPCs are the characters your players talk to.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "New NPC" }));
    expect(onCreate).toHaveBeenCalledTimes(1);
  });

  it("hides the decorative icon from assistive technology", () => {
    render(<EmptyState icon={<svg data-testid="icon" />} title="Nothing here" />);
    expect(screen.getByTestId("icon").parentElement).toHaveAttribute("aria-hidden", "true");
  });

  it("omits the parts it was not given", () => {
    const { container } = render(<EmptyState title="No matches" />);
    expect(container.querySelector(".gx-empty__icon")).toBeNull();
    expect(container.querySelector(".gx-empty__desc")).toBeNull();
    expect(container.querySelector(".gx-empty__action")).toBeNull();
  });

  it("is a panel by default and can be an inline row", () => {
    const { container, rerender } = render(<EmptyState title="Empty" />);
    expect(container.firstElementChild).toHaveClass("gx-empty", "gx-empty--panel");
    rerender(<EmptyState title="Empty" size="inline" />);
    expect(container.firstElementChild).toHaveClass("gx-empty--inline");
  });

  it("can make its title a real heading", () => {
    render(<EmptyState title="No maps yet" titleAs="h3" />);
    expect(screen.getByRole("heading", { level: 3, name: "No maps yet" })).toBeInTheDocument();
  });
});
