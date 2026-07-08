import { useState, type ReactNode } from "react";
import * as RAlert from "@radix-ui/react-alert-dialog";

import { Button } from "./Button";
import { Input } from "./Input";

// ConfirmDialog — a modal confirmation gate on a Radix AlertDialog (ADR-0017:
// Radix backs the accessibility-heavy primitives). Built for destructive actions
// that must not fire on a single click (#209 hard-delete): the caller drives it
// controlled via `open`, issues the side effect in `onConfirm`, and clears its
// state in `onOpenChange`. AlertDialog's native behaviour supplies the rest —
// Escape and Cancel dismiss without confirming, focus is trapped, and the
// overlay does NOT close on click (a mis-click can't destroy data).
//
// For irreversible deletes (#269 campaign hard-delete) pass `confirmText`: the
// dialog then renders a text field and keeps confirm disabled until the operator
// re-types that exact string (e.g. the campaign name). The typed value resets
// whenever the dialog closes, so a reopened dialog never inherits a prior entry.

export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel = "Delete",
  cancelLabel = "Cancel",
  onConfirm,
  confirmDisabled = false,
  destructive = true,
  confirmText,
  confirmTextLabel,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  onConfirm: () => void;
  confirmDisabled?: boolean;
  destructive?: boolean;
  // confirmText, when set, gates confirm behind an exact re-typed match (#269).
  confirmText?: string;
  // confirmTextLabel labels the confirmation field; defaults to a generic prompt.
  confirmTextLabel?: ReactNode;
}) {
  const [typed, setTyped] = useState("");

  // Reset the typed value on every close so a reopened dialog starts empty — a
  // stale match must never let the next open confirm without a fresh re-type.
  const handleOpenChange = (next: boolean) => {
    if (!next) setTyped("");
    onOpenChange(next);
  };

  const typedGateOpen = confirmText === undefined || typed === confirmText;
  const disabled = confirmDisabled || !typedGateOpen;

  return (
    <RAlert.Root open={open} onOpenChange={handleOpenChange}>
      <RAlert.Portal>
        <RAlert.Overlay className="gx-confirm__overlay" />
        {/* Radix auto-wires aria-describedby to the Description below when present. */}
        <RAlert.Content className="gx-confirm">
          <RAlert.Title className="gx-confirm__title">{title}</RAlert.Title>
          {description && (
            <RAlert.Description className="gx-confirm__desc">{description}</RAlert.Description>
          )}
          {confirmText !== undefined && (
            <Input
              label={confirmTextLabel ?? `Type “${confirmText}” to confirm`}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              autoComplete="off"
              autoFocus
              data-testid="confirm-text-input"
            />
          )}
          <div className="gx-confirm__actions">
            <RAlert.Cancel asChild>
              <Button variant="ghost">{cancelLabel}</Button>
            </RAlert.Cancel>
            <RAlert.Action asChild>
              <Button
                variant={destructive ? "danger" : "primary"}
                onClick={onConfirm}
                disabled={disabled}
              >
                {confirmLabel}
              </Button>
            </RAlert.Action>
          </div>
        </RAlert.Content>
      </RAlert.Portal>
    </RAlert.Root>
  );
}
