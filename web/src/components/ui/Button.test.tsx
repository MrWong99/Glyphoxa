import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { Button } from "./Button";

describe("Button", () => {
  it("renders its label and fires onClick", () => {
    const onClick = vi.fn();
    render(<Button onClick={onClick}>Save</Button>);
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("leaves the plain button's DOM alone: label and icons are direct children", () => {
    render(<Button iconStart={<svg data-testid="icon" />}>Save</Button>);
    const button = screen.getByRole("button", { name: "Save" });
    expect(button).not.toHaveAttribute("aria-busy");
    expect(button).not.toHaveAttribute("data-loading");
    expect(button.querySelector(".gx-btn__content")).toBeNull();
    expect(screen.getByTestId("icon").parentElement).toHaveClass("gx-btn__icon");
  });

  describe("loading", () => {
    it("is aria-busy and disabled, and does not fire onClick", () => {
      const onClick = vi.fn();
      render(
        <Button loading onClick={onClick}>
          Save
        </Button>,
      );
      const button = screen.getByRole("button", { name: "Save" });
      expect(button).toHaveAttribute("aria-busy", "true");
      expect(button).toBeDisabled();
      fireEvent.click(button);
      expect(onClick).not.toHaveBeenCalled();
    });

    it("keeps the label in the layout (width and accessible name) under a spinner", () => {
      render(<Button loading>Save changes</Button>);
      const button = screen.getByRole("button", { name: "Save changes" });
      // The label is still rendered, inside the kept-in-flow content box …
      expect(button.querySelector(".gx-btn__content")).toHaveTextContent("Save changes");
      // … and the spinner is decorative, so it adds nothing to the name.
      const spinner = button.querySelector(".gx-spinner");
      expect(spinner).not.toBeNull();
      expect(spinner).toHaveAttribute("aria-hidden", "true");
    });

    it("stays disabled when the caller also disables it, and clears with loading", () => {
      const { rerender } = render(
        <Button loading disabled>
          Save
        </Button>,
      );
      expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
      rerender(<Button>Save</Button>);
      const button = screen.getByRole("button", { name: "Save" });
      expect(button).toBeEnabled();
      expect(button).not.toHaveAttribute("aria-busy");
    });
  });

  describe("iconOnly", () => {
    it("is a square icon button named by its aria-label", () => {
      render(
        <Button iconOnly aria-label="Close panel" variant="ghost">
          <svg data-testid="x" />
        </Button>,
      );
      const button = screen.getByRole("button", { name: "Close panel" });
      expect(button).toHaveClass("gx-btn--icon");
      expect(screen.getByTestId("x").parentElement).toHaveClass("gx-btn__icon");
    });

    it("refuses an icon-only button without a name at compile time", () => {
      // @ts-expect-error iconOnly requires aria-label
      render(<Button iconOnly>x</Button>);
    });
  });

  it("forwards its ref to the button element (Radix asChild slots need it)", () => {
    const ref = { current: null as HTMLButtonElement | null };
    render(<Button ref={ref}>Go</Button>);
    expect(ref.current).toBe(screen.getByRole("button", { name: "Go" }));
  });
});
