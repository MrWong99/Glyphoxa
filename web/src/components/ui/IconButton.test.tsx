import { useState } from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { IconButton } from "./IconButton";
import { Menu, MenuItem } from "./Menu";

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

  // Swapping the tooltip wrapper in and out would replace the <button> node,
  // and the click that set `loading` would lose keyboard focus to <body>.
  it("stays the same element, with focus, when loading starts and ends", () => {
    function Refresh() {
      const [loading, setLoading] = useState(false);
      return (
        <>
          <IconButton label="Refresh" loading={loading} onClick={() => setLoading(true)}>
            <svg />
          </IconButton>
          <button onClick={() => setLoading(false)}>done</button>
        </>
      );
    }
    render(<Refresh />);
    const button = screen.getByRole("button", { name: "Refresh" });
    act(() => button.focus());
    fireEvent.click(button);

    expect(screen.getByRole("button", { name: "Refresh" })).toBe(button);
    expect(button).toHaveAttribute("aria-busy", "true");
    expect(document.activeElement).toBe(button);
    expect(screen.queryByRole("tooltip")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "done" }));
    expect(screen.getByRole("button", { name: "Refresh" })).toBe(button);
    expect(button).toBeEnabled();
  });

  it("holds its tooltip shut while the menu it opens is expanded", () => {
    render(
      <Menu
        trigger={
          <IconButton label="Campaign actions">
            <svg />
          </IconButton>
        }
      >
        <MenuItem>Settings</MenuItem>
      </Menu>,
    );
    const trigger = screen.getByRole("button", { name: "Campaign actions" });
    act(() => trigger.focus());
    expect(screen.getByRole("tooltip")).toHaveTextContent("Campaign actions");

    fireEvent.keyDown(trigger, { key: "Enter" });
    expect(screen.getByRole("menu")).toBeInTheDocument();
    expect(screen.queryByRole("tooltip")).toBeNull();
  });
});
