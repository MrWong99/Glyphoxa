import { useCallback, useRef } from "react";

// useRestoreFocus gives a Radix modal (Dialog, AlertDialog) back its manners
// when it is driven CONTROLLED, without a Radix Trigger.
//
// On close Radix returns focus to its own Trigger element and cancels the
// focus scope's default. A dialog opened by flipping `open` from some other
// button has no Trigger, so focus fell to <body>: a keyboard user was sent
// back to the top of the page after every dialog. This remembers what had
// focus when the dialog opened and puts focus back there on close, with two
// exceptions:
//
//   - that element is gone (the row the dialog deleted, a screen it navigated
//     away from): Radix's own handling stands;
//   - something else has taken focus by the time the dialog is gone (the
//     confirmed action opened a form whose first field is autoFocus): focus
//     stays there. Returning it to the opener would pull the caret out of the
//     field the GM is about to type in.
//
// Spread what it returns onto the Radix Content element:
//
//   const restoreFocus = useRestoreFocus();
//   <RDialog.Content {...restoreFocus} />
//
// The opener is read when the Content element MOUNTS (the ref), not in Radix's
// onOpenAutoFocus: Radix skips that callback altogether when focus is already
// inside the dialog, which is the case for every dialog with an autoFocus
// field.

// The last focus move in the document: where focus went and where it came
// from. A dialog whose content has an autoFocus field needs it. React focuses
// that field while it commits the dialog, so by the time this hook is handed
// the Content element "what has focus" is already inside the dialog, and the
// opener is only known as the element focus came from.
let lastFocusMove: { to: EventTarget | null; from: HTMLElement | null } | null = null;
if (typeof document !== "undefined") {
  document.addEventListener(
    "focusin",
    (event) => {
      lastFocusMove = {
        to: event.target,
        from: event.relatedTarget instanceof HTMLElement ? event.relatedTarget : null,
      };
    },
    true,
  );
}

// A dialog opened from a Menu item is the one case where "what had focus" is
// useless: the item is unmounted with its menu in the same commit that mounts
// the dialog. Menu notes its trigger here when an item is chosen, and a dialog
// that opens right after returns focus to that trigger instead.
const MENU_HANDOFF_MS = 1000;
let menuTrigger: { element: HTMLElement; at: number } | null = null;

export function noteMenuTrigger(element: HTMLElement | null): void {
  menuTrigger = element ? { element, at: Date.now() } : null;
}

function takeMenuTrigger(): HTMLElement | null {
  const noted = menuTrigger;
  menuTrigger = null;
  if (!noted || Date.now() - noted.at > MENU_HANDOFF_MS) return null;
  return noted.element;
}

export function useRestoreFocus(): {
  ref: (content: HTMLElement | null) => void;
  onCloseAutoFocus: (event: Event) => void;
} {
  const opener = useRef<HTMLElement | null>(null);
  // The Content element the opener was read for: a ref is called again when a
  // caller composes it into a new function (and twice under StrictMode), and
  // by then focus is inside the dialog.
  const seen = useRef<HTMLElement | null>(null);

  // Runs while React commits the dialog, before Radix moves focus into it, so
  // document.activeElement is still the control that opened it — unless an
  // autoFocus field inside the dialog got there first (see lastFocusMove).
  const ref = useCallback((content: HTMLElement | null) => {
    if (!content || content === seen.current) return;
    seen.current = content;
    let active: Element | null = document.activeElement;
    if (active && content.contains(active)) {
      const move = lastFocusMove;
      const cameFrom = move && move.to instanceof Node && content.contains(move.to) ? move.from : null;
      active = cameFrom && !content.contains(cameFrom) ? cameFrom : null;
    }
    const fromMenu = takeMenuTrigger();
    if (active instanceof HTMLElement && active !== document.body && !active.closest('[role="menu"]')) {
      opener.current = active;
    } else {
      opener.current = fromMenu;
    }
  }, []);

  // Fires in a timeout after the dialog has unmounted, so whatever the action
  // behind Confirm or Save rendered is already on the page.
  const onCloseAutoFocus = useCallback((event: Event) => {
    const target = opener.current;
    opener.current = null;
    seen.current = null;
    const active = document.activeElement;
    if (active instanceof HTMLElement && active !== document.body) {
      // Focus is already somewhere on purpose. Leave it, and stop Radix
      // moving it to its Trigger.
      event.preventDefault();
      return;
    }
    if (target && target.isConnected) {
      // Ours to handle: stop Radix focusing its (absent) Trigger instead.
      event.preventDefault();
      target.focus();
    }
  }, []);

  return { ref, onCloseAutoFocus };
}
