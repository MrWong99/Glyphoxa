import { useRef, useState } from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { Button } from "./Button";
import { Dialog, DialogClose } from "./Dialog";
import { usePopoverDismiss } from "./usePopoverDismiss";

type Variant = "modal" | "sheet";

function Harness({
  variant,
  onOpenChange,
  closeOnOutsideClick,
}: {
  variant: Variant;
  onOpenChange?: (open: boolean) => void;
  closeOnOutsideClick?: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button onClick={() => setOpen(true)}>open it</button>
      <button>behind the dialog</button>
      <Dialog
        open={open}
        onOpenChange={(next) => {
          onOpenChange?.(next);
          setOpen(next);
        }}
        variant={variant}
        title="New NPC"
        description="Who are they?"
        closeOnOutsideClick={closeOnOutsideClick}
        footer={
          <>
            <DialogClose>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button>Create</Button>
          </>
        }
      >
        <input aria-label="Name" />
      </Dialog>
    </>
  );
}

// Radix closes a dialog on the CLICK that follows a primary-button press on the
// backdrop (so a press that turns into a drag does not dismiss), and arms its
// listener a tick after mount.
async function pressBackdrop() {
  await act(() => new Promise((resolve) => setTimeout(resolve, 0)));
  const backdrop = document.querySelector(".gx-confirm__overlay")!;
  fireEvent.pointerDown(backdrop, { button: 0 });
  fireEvent.pointerUp(backdrop, { button: 0 });
  fireEvent.click(backdrop, { button: 0 });
}

function openDialog() {
  const opener = screen.getByRole("button", { name: "open it" });
  act(() => opener.focus());
  fireEvent.click(opener);
  return { opener, dialog: screen.getByRole("dialog", { name: "New NPC" }) };
}

describe.each<Variant>(["modal", "sheet"])("Dialog (%s)", (variant) => {
  it("is a named, described dialog carrying its variant", () => {
    render(<Harness variant={variant} />);
    expect(screen.queryByRole("dialog")).toBeNull();

    const { dialog } = openDialog();
    expect(dialog).toHaveAccessibleDescription("Who are they?");
    expect(dialog).toHaveClass("gx-dialog", `gx-dialog--${variant}`);
    expect(dialog).toHaveAttribute("data-state", "open");
  });

  it("moves focus to the first field, not to Close", async () => {
    render(<Harness variant={variant} />);
    openDialog();
    await waitFor(() => expect(screen.getByRole("textbox", { name: "Name" })).toHaveFocus());
  });

  it("traps focus: Tab wraps at both ends and focus cannot leave", async () => {
    render(<Harness variant={variant} />);
    const { dialog } = openDialog();
    const first = screen.getByRole("textbox", { name: "Name" });
    const last = within(dialog).getByRole("button", { name: "Close" });
    await waitFor(() => expect(first).toHaveFocus());

    // Shift+Tab on the first tabbable wraps to the last …
    fireEvent.keyDown(first, { key: "Tab", shiftKey: true });
    expect(last).toHaveFocus();
    // … and Tab on the last wraps back to the first.
    fireEvent.keyDown(last, { key: "Tab" });
    expect(first).toHaveFocus();

    // Focus forced onto the page behind is pulled straight back in.
    act(() => screen.getByText("behind the dialog").focus());
    expect(dialog.contains(document.activeElement)).toBe(true);
  });

  it("closes on Escape and returns focus to what opened it", async () => {
    const onOpenChange = vi.fn();
    render(<Harness variant={variant} onOpenChange={onOpenChange} />);
    const { opener } = openDialog();

    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    expect(onOpenChange).toHaveBeenLastCalledWith(false);
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(opener).toHaveFocus());
  });

  it("closes from the Close button and from a DialogClose in the footer", () => {
    render(<Harness variant={variant} />);
    let { dialog } = openDialog();
    fireEvent.click(within(dialog).getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).toBeNull();

    ({ dialog } = openDialog());
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("hides the page behind it from assistive technology", () => {
    render(<Harness variant={variant} />);
    openDialog();
    // Radix marks every sibling of the portal aria-hidden; the role query
    // (which honours it) no longer finds the button behind the dialog.
    expect(screen.queryByRole("button", { name: "behind the dialog" })).toBeNull();
  });
});

describe("Dialog", () => {
  it("closes on a click on the backdrop, unless told not to", async () => {
    const { unmount } = render(<Harness variant="modal" />);
    openDialog();
    await pressBackdrop();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    unmount();

    render(<Harness variant="modal" closeOnOutsideClick={false} />);
    openDialog();
    await pressBackdrop();
    await act(() => new Promise((resolve) => setTimeout(resolve, 0)));
    expect(screen.getByRole("dialog", { name: "New NPC" })).toBeInTheDocument();
  });

  it("opens from its own trigger when uncontrolled", () => {
    render(
      <Dialog trigger={<Button>New NPC</Button>} title="New NPC" showClose={false}>
        <p>body</p>
      </Dialog>,
    );
    fireEvent.click(screen.getByRole("button", { name: "New NPC" }));
    const dialog = screen.getByRole("dialog", { name: "New NPC" });
    expect(within(dialog).queryByRole("button", { name: "Close" })).toBeNull();
  });

  // usePopoverDismiss treats an open Radix Dialog as a modal layer (its
  // role=dialog + data-state=open selector): a hand-rolled popover underneath
  // must not close itself on the dialog's own clicks or its Escape.
  it("is seen as a modal layer by usePopoverDismiss", () => {
    function PopoverUnderDialog() {
      const ref = useRef<HTMLDivElement>(null);
      const [popoverOpen, setPopoverOpen] = useState(true);
      usePopoverDismiss(ref, popoverOpen, () => setPopoverOpen(false));
      return (
        <>
          <div ref={ref}>{popoverOpen && <p>popover open</p>}</div>
          <Dialog open onOpenChange={() => {}} title="On top">
            <button>inside dialog</button>
          </Dialog>
        </>
      );
    }
    render(<PopoverUnderDialog />);
    fireEvent.mouseDown(screen.getByRole("button", { name: "inside dialog" }));
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.getByText("popover open")).toBeInTheDocument();
  });
});
