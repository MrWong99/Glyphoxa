import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";

import { ConfirmDialog } from "./ConfirmDialog";

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
});
