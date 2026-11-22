import { useRef, useState } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { Button } from "./Button";
import { Popover, PopoverClose } from "./Popover";
import { usePopoverDismiss } from "./usePopoverDismiss";

function Filter({ onOpenChange }: { onOpenChange?: (open: boolean) => void }) {
  return (
    <Popover
      label="Filter entries"
      trigger={<Button variant="secondary">Filter</Button>}
      onOpenChange={onOpenChange}
    >
      <label>
        Tag <input aria-label="Tag" />
      </label>
      <PopoverClose>
        <Button size="sm">Done</Button>
      </PopoverClose>
    </Popover>
  );
}

describe("Popover", () => {
  it("opens from its trigger, which announces the expanded state", () => {
    render(<Filter />);
    const trigger = screen.getByRole("button", { name: "Filter" });
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("dialog")).toBeNull();

    fireEvent.click(trigger);
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    const panel = screen.getByRole("dialog", { name: "Filter entries" });
    expect(trigger).toHaveAttribute("aria-controls", panel.id);
  });

  it("is keyboard operable: focus moves in, Escape closes and focus returns", async () => {
    const onOpenChange = vi.fn();
    render(<Filter onOpenChange={onOpenChange} />);
    const trigger = screen.getByRole("button", { name: "Filter" });
    act(() => trigger.focus());
    // Enter and Space on a button are a click.
    fireEvent.click(trigger);

    const field = screen.getByRole("textbox", { name: "Tag" });
    await waitFor(() => expect(field).toHaveFocus());

    fireEvent.keyDown(field, { key: "Escape" });
    expect(onOpenChange).toHaveBeenLastCalledWith(false);
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it("closes from a PopoverClose inside it", () => {
    render(<Filter />);
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("does not make the page behind it inert", () => {
    render(
      <>
        <button>elsewhere</button>
        <Filter />
      </>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    expect(screen.getByRole("button", { name: "elsewhere" })).toBeInTheDocument();
  });

  // A Radix Popover's panel is role=dialog + data-state=open, exactly what
  // usePopoverDismiss reads as a MODAL layer. It is not one: a hand-rolled
  // popover elsewhere on the page must keep closing on outside clicks.
  it("is not mistaken for a modal layer by usePopoverDismiss", () => {
    function HandRolled() {
      const ref = useRef<HTMLDivElement>(null);
      const [open, setOpen] = useState(true);
      usePopoverDismiss(ref, open, () => setOpen(false));
      return <div ref={ref}>{open && <p>hand-rolled open</p>}</div>;
    }
    render(
      <>
        <HandRolled />
        <button>elsewhere</button>
        <Filter />
      </>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    expect(screen.getByRole("dialog", { name: "Filter entries" })).toBeInTheDocument();

    // A press inside the portalled panel is not "outside" the other popover …
    fireEvent.mouseDown(screen.getByRole("textbox", { name: "Tag" }));
    expect(screen.getByText("hand-rolled open")).toBeInTheDocument();
    // … and a press on the page still dismisses it while the panel is up.
    fireEvent.mouseDown(screen.getByRole("button", { name: "elsewhere" }));
    expect(screen.queryByText("hand-rolled open")).toBeNull();
  });
});
