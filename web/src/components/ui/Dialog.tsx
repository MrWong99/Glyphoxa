import { useCallback, useRef, type ReactElement, type ReactNode } from "react";
import * as RDialog from "@radix-ui/react-dialog";
import { X } from "lucide-react";

import { useI18n } from "@/i18n";
import { IconButton } from "./IconButton";
import { popoverOpenWithin } from "./usePopoverDismiss";
import { useRestoreFocus } from "./useRestoreFocus";

// Dialog — a modal layer on a Radix Dialog (ADR-0017), for anything that is
// more than a yes/no question (that stays ConfirmDialog): a create form, a
// picker, a settings sheet. Radix supplies the behaviour — focus moves in and
// is trapped, Escape and a click on the dimmed backdrop close it, focus goes
// back to whatever opened it, and the page behind is inert to a screen reader.
//
// Two shapes on one surface (the .gx-confirm surface, shared with
// ConfirmDialog):
//
//   modal  a centred card. For a short, focused task.
//   sheet  a drawer on the right edge; at 880px and below a sheet that rises
//          from the bottom edge. For a longer form, or a task the GM does
//          while glancing at the page behind it.
//
// The header and footer stay in place and the body scrolls. `title` is
// required: it is the dialog's accessible name.
//
//   <Dialog open={open} onOpenChange={setOpen} title="New NPC"
//           footer={<><DialogClose><Button variant="ghost">Cancel</Button></DialogClose>
//                     <Button onClick={create}>Create</Button></>}>
//     …fields…
//   </Dialog>
//
// Drive it controlled (open + onOpenChange), or pass `trigger` and let it keep
// its own state. To hold a close while a form has unsaved edits, drive it
// controlled and decide in onOpenChange. Either way focus returns, on close,
// to whatever had it when the dialog opened (useRestoreFocus).

type DialogProps = {
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** The element that opens the dialog (uncontrolled use). Must forward its ref. */
  trigger?: ReactElement;
  title: ReactNode;
  description?: ReactNode;
  children?: ReactNode;
  /** The action row, pinned under the scrolling body. */
  footer?: ReactNode;
  variant?: "modal" | "sheet";
  size?: "sm" | "md" | "lg";
  /** Show the close (X) button. Default true. */
  showClose?: boolean;
  /**
   * Let a click on the dimmed backdrop close the dialog. Default true; turn it
   * off for a form where a stray click would throw away typing.
   */
  closeOnOutsideClick?: boolean;
  className?: string;
};

// A toast is rendered outside the dialog, so Radix reads a click or a focus
// move onto one ("Undo") as leaving the dialog and closes it. Toasts belong to
// the whole app, not to the layer underneath.
function inToast(target: EventTarget | null): boolean {
  return target instanceof Element && target.closest("[data-sonner-toaster]") !== null;
}

export function Dialog({
  open,
  defaultOpen,
  onOpenChange,
  trigger,
  title,
  description,
  children,
  footer,
  variant = "modal",
  size = "md",
  showClose = true,
  closeOnOutsideClick = true,
  className = "",
}: DialogProps) {
  const { t } = useI18n();
  const { ref: noteOpener, onCloseAutoFocus } = useRestoreFocus();
  const content = useRef<HTMLDivElement | null>(null);
  const setContent = useCallback(
    (node: HTMLDivElement | null) => {
      content.current = node;
      noteOpener(node);
    },
    [noteOpener],
  );
  const cls = ["gx-dialog", `gx-dialog--${variant}`, className].filter(Boolean).join(" ");

  return (
    <RDialog.Root open={open} defaultOpen={defaultOpen} onOpenChange={onOpenChange}>
      {trigger && <RDialog.Trigger asChild>{trigger}</RDialog.Trigger>}
      <RDialog.Portal>
        <RDialog.Overlay className="gx-confirm__overlay" />
        <RDialog.Content
          className={cls}
          data-size={size}
          data-variant={variant}
          // Radix warns when a dialog has no description unless this is set
          // explicitly; with a description it wires the id itself.
          {...(description ? {} : { "aria-describedby": undefined })}
          ref={setContent}
          onCloseAutoFocus={onCloseAutoFocus}
          // A picker open inside the dialog (a Combobox) takes this Escape for
          // itself; the dialog closes on the next one.
          onEscapeKeyDown={(event) => {
            if (popoverOpenWithin(content.current)) event.preventDefault();
          }}
          onInteractOutside={(event) => {
            if (inToast(event.target)) event.preventDefault();
          }}
          onPointerDownOutside={(event) => {
            if (!closeOnOutsideClick) event.preventDefault();
          }}
        >
          <div className="gx-dialog__header">
            <RDialog.Title className="gx-confirm__title">{title}</RDialog.Title>
            {description && (
              <RDialog.Description className="gx-confirm__desc">{description}</RDialog.Description>
            )}
          </div>
          <div className="gx-dialog__body">{children}</div>
          {footer && <div className="gx-confirm__actions gx-dialog__footer">{footer}</div>}
          {/* Last in the DOM, first on screen (top right): opening the dialog
              puts focus on its first field or action, not on Close. Its name
              is on screen as the aria-label; a tooltip would only pop up over
              the title. */}
          {showClose && (
            <RDialog.Close asChild>
              <IconButton label={t("common.close")} tooltip={false} className="gx-dialog__close">
                <X size={18} />
              </IconButton>
            </RDialog.Close>
          )}
        </RDialog.Content>
      </RDialog.Portal>
    </RDialog.Root>
  );
}

// DialogClose turns its single child (a Button) into something that closes the
// dialog it is rendered in: the Cancel of a footer, without the caller having
// to thread the open state through.
export function DialogClose({ children }: { children: ReactElement }) {
  return <RDialog.Close asChild>{children}</RDialog.Close>;
}
