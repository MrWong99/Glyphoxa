import { useEffect, useRef, type RefObject } from "react";

// radixModalOpen reports whether a Radix modal layer (AlertDialog / Dialog) is
// currently mounted. Those layers render through a Portal onto document.body —
// OUTSIDE any popover's anchor ref — and own their own dismissal (Escape closes
// the dialog; a mis-click on the overlay does not destroy data). A popover that
// spawns such a dialog (e.g. the campaign switcher's delete ConfirmDialog) must
// SUSPEND its own outside-mousedown / Escape dismissal while the dialog is up:
// otherwise the first mousedown inside the portalled dialog reads as "outside the
// anchor", the popover closes, and the dialog unmounts mid-interaction before its
// action can fire (#269).
function radixModalOpen(): boolean {
  return document.querySelector('[role="alertdialog"], [role="dialog"]') !== null;
}

// usePopoverDismiss closes a lightweight (non-Radix) popover the way a native
// select would: a mousedown outside the anchor element or an Escape keypress
// calls onClose. Extracted from the Combobox popover (#88 slice 2) when the
// CampaignSwitcher grew an identical copy (#267), so dismissal fixes (touch /
// pointer events, stacked popovers, focus handling) land in one place. While a
// Radix modal layer is open it defers to that layer (see radixModalOpen).
export function usePopoverDismiss(
  ref: RefObject<HTMLElement | null>,
  open: boolean,
  onClose: () => void,
) {
  // The latest onClose rides a ref so the document listeners — attached once per
  // open — never call a stale closure, and re-renders don't churn listeners.
  const onCloseRef = useRef(onClose);
  useEffect(() => {
    onCloseRef.current = onClose;
  });

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      // A Radix dialog spawned from inside this popover portals outside the anchor;
      // let it own dismissal rather than closing the popover out from under it.
      if (radixModalOpen()) return;
      if (ref.current && !ref.current.contains(e.target as Node)) onCloseRef.current();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        if (radixModalOpen()) return; // the dialog's own Escape handler closes it
        onCloseRef.current();
      }
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open, ref]);
}
