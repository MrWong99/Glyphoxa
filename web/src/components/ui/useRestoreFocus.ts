import { useCallback, useRef } from "react";

// useRestoreFocus gives a Radix modal (Dialog, AlertDialog) back its manners
// when it is driven CONTROLLED, without a Radix Trigger.
//
// On close Radix returns focus to its own Trigger element and cancels the
// focus scope's default. A dialog opened by flipping `open` from some other
// button has no Trigger, so focus fell to <body>: a keyboard user was sent
// back to the top of the page after every dialog. This remembers what had
// focus when the dialog opened and puts focus back there on close — unless
// that element is gone (the row the dialog deleted, a screen it navigated away
// from), in which case Radix's own handling stands.
//
// Spread the two handlers onto the Radix Content element:
//
//   const restoreFocus = useRestoreFocus();
//   <RDialog.Content {...restoreFocus} />

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
  onOpenAutoFocus: (event: Event) => void;
  onCloseAutoFocus: (event: Event) => void;
} {
  const opener = useRef<HTMLElement | null>(null);

  // Fires before the focus scope moves focus into the dialog, so
  // document.activeElement is still the control that opened it.
  const onOpenAutoFocus = useCallback(() => {
    const active = document.activeElement;
    const fromMenu = takeMenuTrigger();
    if (active instanceof HTMLElement && active !== document.body && !active.closest('[role="menu"]')) {
      opener.current = active;
    } else {
      opener.current = fromMenu;
    }
  }, []);

  const onCloseAutoFocus = useCallback((event: Event) => {
    const target = opener.current;
    opener.current = null;
    if (target && target.isConnected) {
      // Ours to handle: stop Radix focusing its (absent) Trigger instead.
      event.preventDefault();
      target.focus();
    }
  }, []);

  return { onOpenAutoFocus, onCloseAutoFocus };
}
