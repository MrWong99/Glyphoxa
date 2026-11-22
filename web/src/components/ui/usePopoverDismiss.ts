import { useEffect, useRef, type RefObject } from "react";

// MODAL_LAYER matches a real MODAL layer that is currently mounted: a
// Radix AlertDialog (role=alertdialog), a Radix Dialog (role=dialog carrying
// Radix's data-state="open") or a hand-rolled modal that declares itself with
// aria-modal="true" (the command palette). Those layers render through a Portal onto document.body —
// OUTSIDE any popover's anchor ref — and own their own dismissal (Escape closes
// the dialog; a mis-click on the overlay does not destroy data). A popover that
// spawns such a dialog (e.g. the campaign switcher's delete ConfirmDialog) must
// SUSPEND its own outside-mousedown / Escape dismissal while the dialog is up:
// otherwise the first mousedown inside the portalled dialog reads as "outside the
// anchor", the popover closes, and the dialog unmounts mid-interaction before its
// action can fire (#269).
//
// It must NOT match a bare role="dialog": inline, non-modal panels once carried
// that role (the map form, the graph's review card and relation picker), and
// while one of them was merely on screen every popover in the app — the
// campaign switcher, each combobox — ignored outside clicks and Escape.
//
// For the same reason it must not match the Popover primitive (ui/Popover.tsx):
// a Radix Popover's panel is ALSO role="dialog" with data-state="open", and it
// is not modal. That panel carries data-gx-popover, which excludes it here.
const MODAL_LAYER =
  '[role="alertdialog"], [role="dialog"][aria-modal="true"], [role="dialog"][data-state="open"]:not([data-gx-popover])';

// A layer only outranks a popover it does NOT contain. A popover that lives
// INSIDE a dialog (the voice Combobox of a create form in ui/Dialog) is the
// layer on top: it closes on its own Escape and on a press elsewhere in the
// dialog, like anywhere else. Without this, an open dialog switched the
// dismissal of its own pickers off, and Escape closed the whole dialog, form
// and all, instead of the list.
function modalLayerAbove(anchor: HTMLElement | null): boolean {
  for (const layer of document.querySelectorAll(MODAL_LAYER)) {
    if (!anchor || !layer.contains(anchor)) return true;
  }
  return false;
}

// The anchors of the popovers that are open right now, so a Radix layer can
// tell that Escape belongs to a popover inside it (popoverOpenWithin).
const openAnchors = new Set<RefObject<HTMLElement | null>>();

// popoverOpenWithin reports whether a usePopoverDismiss popover anchored
// inside `container` is open. Radix listens for Escape on the document in the
// capture phase, ahead of this hook, and would close the whole Dialog or
// Popover; those primitives ask this in onEscapeKeyDown and stand back, so the
// one keypress closes only the innermost thing.
export function popoverOpenWithin(container: Element | null): boolean {
  if (!container) return false;
  for (const anchor of openAnchors) {
    if (anchor.current && container.contains(anchor.current)) return true;
  }
  return false;
}

// pointerInPortalledMenu reports whether a mousedown landed inside an open
// role="menu" surface. Such a menu (the campaign row-actions menu, #338) portals
// to document.body — OUTSIDE every popover's anchor ref, including the switcher
// PANEL that hosts the menu's own trigger. Without this, the panel's dismiss reads
// the mousedown on a menu item as "outside", closes the panel, and unmounts the
// portalled menu before mouseup — so the item's click never fires. Same trap
// modalLayerAbove guards for dialogs (#269), generalized to menus: any popover
// defers to an open menu, whichever anchor it belongs to. The Menu primitive
// (a Radix DropdownMenu, also role="menu") and the Popover primitive's panel
// are portalled the same way and get the same deference — unless the popover
// is itself INSIDE that surface (a Combobox in a Popover panel): a press
// elsewhere in the panel is then an ordinary outside press.
function pointerInPortalledMenu(target: Node, anchor: HTMLElement | null): boolean {
  if (!(target instanceof Element)) return false;
  const surface = target.closest('[role="menu"], [data-gx-popover]');
  return surface !== null && !(anchor !== null && surface.contains(anchor));
}

// usePopoverDismiss closes a lightweight (non-Radix) popover the way a native
// select would: a mousedown outside the anchor element or an Escape keypress
// calls onClose. Extracted from the Combobox popover (#88 slice 2) when the
// CampaignSwitcher grew an identical copy (#267), so dismissal fixes (touch /
// pointer events, stacked popovers, focus handling) land in one place. While a
// modal layer is open ABOVE it, it defers to that layer (see modalLayerAbove).
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
    openAnchors.add(ref);
    const onDown = (e: MouseEvent) => {
      // A Radix dialog spawned from inside this popover portals outside the anchor;
      // let it own dismissal rather than closing the popover out from under it.
      if (modalLayerAbove(ref.current)) return;
      const target = e.target as Node;
      if (extraRef?.current?.contains(target)) return; // inside the portalled surface
      if (pointerInPortalledMenu(target, ref.current)) return; // an open menu owns its own dismissal
      if (ref.current && !ref.current.contains(target)) onCloseRef.current();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        if (modalLayerAbove(ref.current)) return; // the dialog's own Escape handler closes it
        onCloseRef.current();
      }
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      openAnchors.delete(ref);
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open, ref, extraRef]);
}
