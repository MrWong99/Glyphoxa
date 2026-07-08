import { describe, it, expect, vi, afterEach } from "vitest";
import { useState } from "react";
import { render, screen, fireEvent } from "@testing-library/react";

import { ConfirmDialog } from "./ConfirmDialog";

afterEach(() => vi.restoreAllMocks());

describe("ConfirmDialog", () => {
  it("renders the title, description, and both buttons when open", () => {
    render(
      <ConfirmDialog
        open
        onOpenChange={() => {}}
        title="Delete the vault?"
        description="This can't be undone."
        confirmLabel="Delete entry"
        onConfirm={() => {}}
      />,
    );
    const dialog = screen.getByRole("alertdialog");
    expect(dialog).toHaveTextContent("Delete the vault?");
    expect(dialog).toHaveTextContent("This can't be undone.");
    expect(screen.getByRole("button", { name: "Delete entry" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /cancel/i })).toBeInTheDocument();
  });

  it("renders nothing when closed", () => {
    render(
      <ConfirmDialog
        open={false}
        onOpenChange={() => {}}
        title="Delete the vault?"
        onConfirm={() => {}}
      />,
    );
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("confirm fires onConfirm exactly once and closes", () => {
    const onConfirm = vi.fn();
    const onOpenChange = vi.fn();
    render(
      <ConfirmDialog
        open
        onOpenChange={onOpenChange}
        title="Delete the vault?"
        confirmLabel="Delete entry"
        onConfirm={onConfirm}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Delete entry" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("cancel closes without firing onConfirm", () => {
    const onConfirm = vi.fn();
    const onOpenChange = vi.fn();
    render(
      <ConfirmDialog
        open
        onOpenChange={onOpenChange}
        title="Delete the vault?"
        onConfirm={onConfirm}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }));
    expect(onConfirm).not.toHaveBeenCalled();
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("Escape closes without firing onConfirm", () => {
    const onConfirm = vi.fn();
    const onOpenChange = vi.fn();
    render(
      <ConfirmDialog
        open
        onOpenChange={onOpenChange}
        title="Delete the vault?"
        onConfirm={onConfirm}
      />,
    );
    fireEvent.keyDown(screen.getByRole("alertdialog"), { key: "Escape" });
    expect(onConfirm).not.toHaveBeenCalled();
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("confirmDisabled disables the confirm button and blocks onConfirm on click", () => {
    const onConfirm = vi.fn();
    render(
      <ConfirmDialog
        open
        onOpenChange={() => {}}
        title="Delete the vault?"
        confirmLabel="Delete entry"
        confirmDisabled
        onConfirm={onConfirm}
      />,
    );
    const confirm = screen.getByRole("button", { name: "Delete entry" });
    expect(confirm).toBeDisabled();
    // A disabled destructive button must not fire the side effect.
    fireEvent.click(confirm);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  // Adversarial focus probe: the Cancel/Action buttons are rendered via Radix
  // `asChild` onto our Button, so Button MUST forward its ref or Radix's Slot
  // drops it — the AlertDialog then can't move focus into the dialog and logs
  // "Function components cannot be given refs" on every render.
  it("does not warn about refs on function components", () => {
    const err = vi.spyOn(console, "error").mockImplementation(() => {});
    render(
      <ConfirmDialog
        open
        onOpenChange={() => {}}
        title="Delete?"
        description="gone forever"
        onConfirm={() => {}}
      />,
    );
    const refWarnings = err.mock.calls.filter((c) =>
      String(c[0]).includes("Function components cannot be given refs"),
    );
    expect(refWarnings).toHaveLength(0);
  });

  it("with confirmText, keeps confirm disabled until the exact string is typed", () => {
    const onConfirm = vi.fn();
    render(
      <ConfirmDialog
        open
        onOpenChange={() => {}}
        title="Delete campaign?"
        confirmLabel="Delete campaign"
        confirmText="Lost Mine"
        onConfirm={onConfirm}
      />,
    );
    const confirm = screen.getByRole("button", { name: "Delete campaign" });
    // Disabled until the name matches — a click does nothing.
    expect(confirm).toBeDisabled();
    fireEvent.click(confirm);
    expect(onConfirm).not.toHaveBeenCalled();

    const input = screen.getByTestId("confirm-text-input");
    // A near-miss stays disabled.
    fireEvent.change(input, { target: { value: "Lost Min" } });
    expect(confirm).toBeDisabled();
    // Exact match enables it.
    fireEvent.change(input, { target: { value: "Lost Mine" } });
    expect(confirm).toBeEnabled();
    fireEvent.click(confirm);
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("resets the typed confirmation when the dialog closes and reopens", () => {
    const onConfirm = vi.fn();
    function Harness() {
      const [open, setOpen] = useState(true);
      return (
        <>
          <button onClick={() => setOpen(true)}>reopen</button>
          <ConfirmDialog
            open={open}
            onOpenChange={setOpen}
            title="Delete campaign?"
            confirmLabel="Delete campaign"
            confirmText="Lost Mine"
            onConfirm={onConfirm}
          />
        </>
      );
    }
    render(<Harness />);

    // Type the exact name so confirm is enabled.
    fireEvent.change(screen.getByTestId("confirm-text-input"), { target: { value: "Lost Mine" } });
    expect(screen.getByRole("button", { name: "Delete campaign" })).toBeEnabled();

    // Cancel (closes), then reopen: the field is empty and confirm is disabled
    // again — a prior match must not carry over.
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }));
    fireEvent.click(screen.getByRole("button", { name: /reopen/i }));
    expect((screen.getByTestId("confirm-text-input") as HTMLInputElement).value).toBe("");
    expect(screen.getByRole("button", { name: "Delete campaign" })).toBeDisabled();
  });

  it("resets the typed confirmation on a PROGRAMMATIC close (bypassing onOpenChange)", () => {
    // The caller closes the dialog itself (e.g. a delete's onSuccess flips `open`
    // to false) — that never flows through Radix onOpenChange, so the reset must
    // be keyed on `open`, not that callback.
    function Harness() {
      const [open, setOpen] = useState(true);
      return (
        <>
          <button onClick={() => setOpen(false)}>programmatic close</button>
          <button onClick={() => setOpen(true)}>reopen</button>
          <ConfirmDialog
            open={open}
            onOpenChange={setOpen}
            title="Delete campaign?"
            confirmLabel="Delete campaign"
            confirmText="Lost Mine"
            onConfirm={() => {}}
          />
        </>
      );
    }
    render(<Harness />);

    fireEvent.change(screen.getByTestId("confirm-text-input"), { target: { value: "Lost Mine" } });
    expect(screen.getByRole("button", { name: "Delete campaign" })).toBeEnabled();

    // Close programmatically (not via Cancel/Escape), then reopen: field empty,
    // confirm disabled — the effect-based reset covered the non-onOpenChange path.
    // The harness buttons sit behind the modal (aria-hidden), so query them with
    // hidden:true.
    fireEvent.click(screen.getByRole("button", { name: /programmatic close/i, hidden: true }));
    fireEvent.click(screen.getByRole("button", { name: /reopen/i }));
    expect((screen.getByTestId("confirm-text-input") as HTMLInputElement).value).toBe("");
    expect(screen.getByRole("button", { name: "Delete campaign" })).toBeDisabled();
  });

  it("moves initial focus onto the Cancel button (AlertDialog contract)", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const trigger = document.createElement("button");
    trigger.textContent = "outside trigger";
    document.body.appendChild(trigger);
    trigger.focus();
    render(
      <ConfirmDialog
        open
        onOpenChange={() => {}}
        title="Delete?"
        description="gone forever"
        onConfirm={() => {}}
      />,
    );
    await screen.findByRole("alertdialog");
    // If the ref never attached, focus stays on the outside trigger behind the
    // now-aria-hidden app — keyboard/SR users are stranded.
    const cancel = screen.getByRole("button", { name: /cancel/i });
    expect(document.activeElement).toBe(cancel);
  });
});
