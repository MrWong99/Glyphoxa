import type { ReactNode } from "react";
import * as RAlert from "@radix-ui/react-alert-dialog";

import { Button } from "./Button";

// ConfirmDialog — a modal confirmation gate on a Radix AlertDialog (ADR-0017:
// Radix backs the accessibility-heavy primitives). Built for destructive actions
// that must not fire on a single click (#209 hard-delete): the caller drives it
// controlled via `open`, issues the side effect in `onConfirm`, and clears its
// state in `onOpenChange`. AlertDialog's native behaviour supplies the rest —
// Escape and Cancel dismiss without confirming, focus is trapped, and the
// overlay does NOT close on click (a mis-click can't destroy data).

export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel = "Delete",
  cancelLabel = "Cancel",
  onConfirm,
  destructive = true,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  onConfirm: () => void;
  destructive?: boolean;
}) {
  return (
    <RAlert.Root open={open} onOpenChange={onOpenChange}>
      <RAlert.Portal>
        <RAlert.Overlay className="gx-confirm__overlay" />
        {/* Radix auto-wires aria-describedby to the Description below when present. */}
        <RAlert.Content className="gx-confirm">
          <RAlert.Title className="gx-confirm__title">{title}</RAlert.Title>
          {description && (
            <RAlert.Description className="gx-confirm__desc">{description}</RAlert.Description>
          )}
          <div className="gx-confirm__actions">
            <RAlert.Cancel asChild>
              <Button variant="ghost">{cancelLabel}</Button>
            </RAlert.Cancel>
            <RAlert.Action asChild>
              <Button variant={destructive ? "danger" : "primary"} onClick={onConfirm}>
                {confirmLabel}
              </Button>
            </RAlert.Action>
          </div>
        </RAlert.Content>
      </RAlert.Portal>
    </RAlert.Root>
  );
}
