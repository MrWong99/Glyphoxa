import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { IconButton } from "./IconButton";

describe("IconButton", () => {
  it("is named by its required label and fires onClick", () => {
    const onClick = vi.fn();
    render(
      <IconButton label="Delete entry" onClick={onClick}>
        <svg />
      </IconButton>,
    );
    const button = screen.getByRole("button", { name: "Delete entry" });
    expect(button).toHaveAttribute("type", "button");
    fireEvent.click(button);
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("shows the label as a tooltip on keyboard focus", () => {
    render(
      <IconButton label="Delete entry">
        <svg />
      </IconButton>,
    );
    act(() => screen.getByRole("button", { name: "Delete entry" }).focus());
    expect(screen.getByRole("tooltip")).toHaveTextContent("Delete entry");
  });

  it("can opt out of the tooltip", () => {
    render(
      <IconButton label="Close" tooltip={false}>
        <svg />
      </IconButton>,
    );
    act(() => screen.getByRole("button", { name: "Close" }).focus());
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("carries size and tone as classes", () => {
    render(
      <>
        <IconButton label="small" size="sm">
          <svg />
        </IconButton>
        <IconButton label="danger" tone="danger">
          <svg />
        </IconButton>
      </>,
    );
    expect(screen.getByRole("button", { name: "small" })).toHaveClass("gx-icon-btn", "gx-icon-btn--sm");
    expect(screen.getByRole("button", { name: "danger" })).toHaveClass("gx-icon-btn--md", "gx-icon-btn--danger");
  });

  it("is aria-busy and disabled while loading", () => {
    const onClick = vi.fn();
    render(
      <IconButton label="Refresh" loading onClick={onClick}>
        <svg />
      </IconButton>,
    );
    const button = screen.getByRole("button", { name: "Refresh" });
    expect(button).toHaveAttribute("aria-busy", "true");
    expect(button).toBeDisabled();
    fireEvent.click(button);
    expect(onClick).not.toHaveBeenCalled();
  });

  it("forwards its ref, so it can be a Radix trigger", () => {
    const ref = { current: null as HTMLButtonElement | null };
    render(
      <IconButton label="More" ref={ref}>
        <svg />
      </IconButton>,
    );
    expect(ref.current).toBe(screen.getByRole("button", { name: "More" }));
  });
});
