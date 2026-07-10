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

// pointerInPortalledMenu reports whether a mousedown landed inside an open
// role="menu" surface. Such a menu (the campaign row-actions menu, #338) portals
// to document.body — OUTSIDE every popover's anchor ref, including the switcher
// PANEL that hosts the menu's own trigger. Without this, the panel's dismiss reads
// the mousedown on a menu item as "outside", closes the panel, and unmounts the
// portalled menu before mouseup — so the item's click never fires. Same trap
// radixModalOpen guards for dialogs (#269), generalized to menus: any popover
// defers to an open menu, whichever anchor it belongs to.
function pointerInPortalledMenu(target: Node): boolean {
  return target instanceof Element && target.closest('[role="menu"]') !== null;
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
  // extraRef, when set, is a SECOND element treated as "inside" for outside-click
  // dismissal — for a popover whose surface is portalled out of the anchor's
  // subtree (e.g. the campaign row-actions menu, #338, which escapes the switcher
  // list's overflow via a portal). A mousedown inside the portalled surface is no
  // longer "inside the anchor", so without this it would read as an outside click
  // and close the menu out from under its own item click.
  extraRef?: RefObject<HTMLElement | null>,
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
      const target = e.target as Node;
      if (extraRef?.current?.contains(target)) return; // inside the portalled surface
      if (pointerInPortalledMenu(target)) return; // an open menu owns its own dismissal
      if (ref.current && !ref.current.contains(target)) onCloseRef.current();
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
  }, [open, ref, extraRef]);
}
