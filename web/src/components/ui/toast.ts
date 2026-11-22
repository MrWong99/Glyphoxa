import { createElement } from "react";
import { toast } from "sonner";

import { useI18n } from "@/i18n";

// Toast helpers on sonner (ADR-0017). The one <Toaster> lives in AppShell;
// its look comes from styles/components.css ("Toasts"), not from here.
//
// When to toast at all is the feedback rule in ADR-0017: a toast reports an
// EVENT that has passed (a mutation failed; something was removed; a save took
// the GM to another place). A state that persists is an Alert, a failed load
// an ErrorState, a save inside an editor that stays open an inline status.

// An undo toast stays long enough to be read and reached with a keyboard
// (sonner's own 4s default is not). sonner pauses the countdown while the
// pointer is over the toast or the window is in the background.
const UNDO_DURATION_MS = 8000;

// The label is a component, not a string, so this module needs no `t` from its
// caller: it renders inside the Toaster, under the app's I18nProvider, and so
// follows the display language even if it changes while the toast is up.
function UndoLabel() {
  return useI18n().t("common.undo");
}

// toastUndo reports something that has just been done and offers to take it
// back: "Removed Flo from the cast" [Undo]. `onUndo` runs at most once, when
// the GM presses Undo; the toast then closes. Returns the toast id (pass it to
// toast.dismiss to close the toast early, e.g. when the screen unmounts).
//
//   remove(npc);
//   toastUndo(t("cast.removed", { name: npc.name }), () => restore(npc));
//
// The action must really be undoable for as long as the toast is up. If the
// undo itself can fail, report that from onUndo with toast.error.
export function toastUndo(
  message: string,
  onUndo: () => void,
  options: { duration?: number; description?: string } = {},
): string | number {
  let undone = false;
  return toast(message, {
    description: options.description,
    duration: options.duration ?? UNDO_DURATION_MS,
    action: {
      label: createElement(UndoLabel),
      onClick: () => {
        if (undone) return;
        undone = true;
        onUndo();
      },
    },
  });
}
