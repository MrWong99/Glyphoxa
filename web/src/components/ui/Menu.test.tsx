import { useState } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { Dialog } from "./Dialog";
import { IconButton } from "./IconButton";
import { Menu, MenuItem, MenuLabel, MenuSeparator } from "./Menu";

function Actions({
  onSettings = () => {},
  onExport = () => {},
  onDelete = () => {},
}: {
  onSettings?: () => void;
  onExport?: () => void;
  onDelete?: () => void;
}) {
  return (
    <Menu
      trigger={
        <IconButton label="Campaign actions" tooltip={false}>
          <svg />
        </IconButton>
      }
    >
      <MenuLabel>Campaign</MenuLabel>
      <MenuItem onSelect={onSettings}>Settings</MenuItem>
      <MenuItem onSelect={onExport} disabled>
        Export
      </MenuItem>
      <MenuSeparator />
      <MenuItem onSelect={onDelete} tone="danger">
        Delete…
      </MenuItem>
    </Menu>
  );
}

// Radix opens a dropdown menu from the keyboard on Enter, Space or ArrowDown.
function openWithKeyboard() {
  const trigger = screen.getByRole("button", { name: "Campaign actions" });
  act(() => trigger.focus());
  fireEvent.keyDown(trigger, { key: "Enter" });
  return trigger;
}

describe("Menu", () => {
  it("is closed until its trigger is used, and is named by the trigger", () => {
    render(<Actions />);
    const trigger = screen.getByRole("button", { name: "Campaign actions" });
    expect(trigger).toHaveAttribute("aria-haspopup", "menu");
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("menu")).toBeNull();

    openWithKeyboard();
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("menu", { name: "Campaign actions" })).toBeInTheDocument();
    expect(screen.getAllByRole("menuitem").map((item) => item.textContent)).toEqual([
      "Settings",
      "Export",
      "Delete…",
    ]);
  });

  it("is keyboard operable: arrows move past a disabled item, Enter runs the item and closes", async () => {
    const onSettings = vi.fn();
    const onDelete = vi.fn();
    render(<Actions onSettings={onSettings} onDelete={onDelete} />);
    const trigger = openWithKeyboard();

    const settings = screen.getByRole("menuitem", { name: "Settings" });
    await waitFor(() => expect(settings).toHaveFocus());

    // Export is disabled, so ArrowDown lands on Delete.
    fireEvent.keyDown(settings, { key: "ArrowDown" });
    const del = screen.getByRole("menuitem", { name: "Delete…" });
    await waitFor(() => expect(del).toHaveFocus());

    fireEvent.keyDown(del, { key: "Enter" });
    expect(onDelete).toHaveBeenCalledTimes(1);
    expect(onSettings).not.toHaveBeenCalled();
    expect(screen.queryByRole("menu")).toBeNull();
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it("closes on Escape without running anything and returns focus to the trigger", async () => {
    const onSettings = vi.fn();
    render(<Actions onSettings={onSettings} />);
    const trigger = openWithKeyboard();
    const settings = screen.getByRole("menuitem", { name: "Settings" });
    await waitFor(() => expect(settings).toHaveFocus());

    fireEvent.keyDown(settings, { key: "Escape" });
    expect(screen.queryByRole("menu")).toBeNull();
    expect(onSettings).not.toHaveBeenCalled();
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it("runs an item on click and never runs a disabled one", () => {
    const onSettings = vi.fn();
    const onExport = vi.fn();
    render(<Actions onSettings={onSettings} onExport={onExport} />);

    openWithKeyboard();
    const exportItem = screen.getByRole("menuitem", { name: "Export" });
    expect(exportItem).toHaveAttribute("aria-disabled", "true");
    fireEvent.click(exportItem);
    expect(onExport).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("menuitem", { name: "Settings" }));
    expect(onSettings).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("marks a destructive item", () => {
    render(<Actions />);
    openWithKeyboard();
    expect(screen.getByRole("menuitem", { name: "Delete…" })).toHaveClass("gx-menu__item--danger");
  });

  // The item is unmounted together with its menu, so "what had focus when the
  // dialog opened" is gone by the time the dialog closes. Focus must land on
  // the menu's trigger, not on <body>.
  it("hands focus back to its trigger after a dialog an item opened closes", async () => {
    function Card() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <Actions onSettings={() => setOpen(true)} />
          <Dialog open={open} onOpenChange={setOpen} title="Campaign settings">
            <input aria-label="Name" />
          </Dialog>
        </>
      );
    }
    render(<Card />);
    const trigger = openWithKeyboard();
    const settings = screen.getByRole("menuitem", { name: "Settings" });
    await waitFor(() => expect(settings).toHaveFocus());
    fireEvent.keyDown(settings, { key: "Enter" });

    const field = await screen.findByRole("textbox", { name: "Name" });
    await waitFor(() => expect(field).toHaveFocus());

    fireEvent.keyDown(field, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(trigger).toHaveFocus());
  });
});
