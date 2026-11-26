import { createContext, useContext, useState, type FocusEvent, type ReactElement, type ReactNode } from "react";
import * as RTooltip from "@radix-ui/react-tooltip";

// Tooltip — a Radix Tooltip (ADR-0017) on the .gx-tooltip surface. It opens on
// hover AND on keyboard focus, and Escape closes it, which the title attribute
// it replaces does for neither a keyboard nor a touch user.
//
// A tooltip NAMES or briefly explains its trigger; it is never the only home
// of something the GM has to read (a touch screen has no hover). Icon-only
// controls get theirs through IconButton, which also sets the aria-label.
//
// The trigger is the single child, and it must forward its ref and spread its
// props onto a focusable element (Button, IconButton and plain DOM elements
// all do).
//
// TooltipProvider shares one open delay across a subtree, so moving between
// neighbouring icons shows each tooltip at once after the first. Mount it once
// around the app chrome; a Tooltip rendered outside any provider brings its
// own, so the primitive works in a single-screen test or a lazily loaded
// corner without ceremony.

const DELAY_MS = 400;

// Radix opens a tooltip on ANY focus that did not follow a pointerdown on the
// trigger itself — script focus included. The shell hands focus back to the
// drawer toggle after a tap closes the drawer, and the palette hands it back
// to the search button after a tap picks a row: on a phone that left a
// "Toggle sidebar" (or a "Ctrl+K") bubble stuck over the screen the GM had
// just opened. Open on focus only when the browser would draw a focus ring
// there (:focus-visible), which it does after keyboard use and not after a
// tap or a click. Hover opens it as before. (preventDefault is how Radix's
// composed handlers are told to skip their own.)
function holdShutUnlessFocusVisible(event: FocusEvent<HTMLElement>) {
  let visible = true;
  try {
    visible = event.currentTarget.matches(":focus-visible");
  } catch {
    // An engine without :focus-visible: keep the keyboard path working.
  }
  if (!visible) event.preventDefault();
}

const InsideProvider = createContext(false);

export function TooltipProvider({
  children,
  delayDuration = DELAY_MS,
}: {
  children: ReactNode;
  delayDuration?: number;
}) {
  return (
    <RTooltip.Provider delayDuration={delayDuration} skipDelayDuration={300}>
      <InsideProvider.Provider value={true}>{children}</InsideProvider.Provider>
    </RTooltip.Provider>
  );
}

export function Tooltip({
  content,
  children,
  side = "top",
  align = "center",
  open,
  defaultOpen,
  onOpenChange,
  disabled = false,
}: {
  /** The tooltip text. A falsy value renders the child with no tooltip. */
  content: ReactNode;
  children: ReactElement;
  side?: "top" | "right" | "bottom" | "left";
  align?: "start" | "center" | "end";
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  /**
   * Keep the tooltip shut (e.g. while the trigger's own menu is open, or while
   * it is disabled). The trigger stays the same mounted element, so toggling
   * this never costs it its focus.
   */
  disabled?: boolean;
}) {
  const insideProvider = useContext(InsideProvider);
  // Always driven controlled, so `disabled` can hold it shut without swapping
  // the tree around the trigger: returning the bare child while disabled would
  // put a different element type at this position, and React would replace
  // the trigger's DOM node (dropping focus to <body>) on every toggle.
  const [ownOpen, setOwnOpen] = useState(defaultOpen ?? false);
  if (disabled && ownOpen) setOwnOpen(false);
  if (!content) return children;

  const tooltip = (
    <RTooltip.Root
      open={!disabled && (open ?? ownOpen)}
      onOpenChange={(next) => {
        if (disabled) return;
        setOwnOpen(next);
        onOpenChange?.(next);
      }}
    >
      <RTooltip.Trigger asChild onFocus={holdShutUnlessFocusVisible}>
        {children}
      </RTooltip.Trigger>
      <RTooltip.Portal>
        <RTooltip.Content className="gx-tooltip" side={side} align={align} sideOffset={6} collisionPadding={8}>
          {content}
        </RTooltip.Content>
      </RTooltip.Portal>
    </RTooltip.Root>
  );

  return insideProvider ? tooltip : <TooltipProvider>{tooltip}</TooltipProvider>;
}
