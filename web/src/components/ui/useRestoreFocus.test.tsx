import { StrictMode, useEffect, useState } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ConfirmDialog } from "./ConfirmDialog";
import { Dialog } from "./Dialog";
import { useDirtyGuard } from "./useDirtyGuard";

// Radix returns focus in a timeout after the dialog has unmounted; let it run.
const settle = () => act(() => new Promise<void>((resolve) => setTimeout(resolve, 0)));

describe("useRestoreFocus", () => {
  // The Players panel: a dirty bind form, a click on another row, "Discard" —
  // and the form for the new row mounts with its name field focused. Returning
  // focus to the row button afterwards would send the typing nowhere.
  it("leaves focus where the confirmed action put it", async () => {
    function Panel() {
      const edits = useDirtyGuard();
      const [editing, setEditing] = useState("a");
      useEffect(() => edits.markDirty(true));
      return (
        <>
          <button onClick={() => edits.guard(() => setEditing("b"))}>select b</button>
          <input key={editing} autoFocus aria-label={`name ${editing}`} />
          {edits.dialog}
        </>
      );
    }
    render(<Panel />);
    const row = screen.getByRole("button", { name: "select b" });
    act(() => row.focus());
    fireEvent.click(row);

    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
    await settle();
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(screen.getByRole("textbox", { name: "name b" })).toHaveFocus();
  });

  it("still returns focus to the opener when the confirmed action focuses nothing", async () => {
    function Panel() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button onClick={() => setOpen(true)}>delete</button>
          <ConfirmDialog open={open} onOpenChange={setOpen} title="Delete?" onConfirm={() => {}} />
        </>
      );
    }
    render(<Panel />);
    const opener = screen.getByRole("button", { name: "delete" });
    act(() => opener.focus());
    fireEvent.click(opener);
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    await waitFor(() => expect(opener).toHaveFocus());
  });

  // React applies autoFocus while it commits, before Radix reports that the
  // dialog is about to take focus: "what has focus now" is then already the
  // field INSIDE the dialog, not the button that opened it.
  it("returns focus to the opener of a Dialog whose content has an autoFocus field", async () => {
    function Panel() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button onClick={() => setOpen(true)}>new npc</button>
          <Dialog open={open} onOpenChange={setOpen} title="New NPC">
            <input autoFocus aria-label="Name" />
          </Dialog>
        </>
      );
    }
    render(<Panel />);
    const opener = screen.getByRole("button", { name: "new npc" });
    act(() => opener.focus());
    fireEvent.click(opener);
    await waitFor(() => expect(screen.getByRole("textbox", { name: "Name" })).toHaveFocus());

    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(opener).toHaveFocus());
  });

  it("returns focus to the opener of a type-to-confirm ConfirmDialog (its field is autoFocus)", async () => {
    function Panel() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button onClick={() => setOpen(true)}>delete campaign</button>
          <ConfirmDialog
            open={open}
            onOpenChange={setOpen}
            title="Delete campaign?"
            confirmText="Saltmarsh"
            onConfirm={() => {}}
          />
        </>
      );
    }
    render(<Panel />);
    const opener = screen.getByRole("button", { name: "delete campaign" });
    act(() => opener.focus());
    fireEvent.click(opener);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(opener).toHaveFocus());
  });

  // The app runs under StrictMode, which in development calls every ref and
  // mount effect twice. The opener must still be read once, before focus is
  // inside the dialog.
  it("returns focus to the opener under StrictMode", async () => {
    function Panel() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button onClick={() => setOpen(true)}>new npc</button>
          <Dialog open={open} onOpenChange={setOpen} title="New NPC">
            <input aria-label="Name" />
          </Dialog>
        </>
      );
    }
    render(
      <StrictMode>
        <Panel />
      </StrictMode>,
    );
    const opener = screen.getByRole("button", { name: "new npc" });
    act(() => opener.focus());
    fireEvent.click(opener);
    await settle();
    expect(screen.getByRole("textbox", { name: "Name" })).toHaveFocus();

    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(opener).toHaveFocus());
  });
});
