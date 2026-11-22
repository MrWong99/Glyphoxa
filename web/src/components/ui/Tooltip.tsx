import { createContext, useContext, type ReactElement, type ReactNode } from "react";
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
  /** Render the child bare (e.g. while its own menu is open). */
  disabled?: boolean;
}) {
  const insideProvider = useContext(InsideProvider);
  if (disabled || !content) return children;

  const tooltip = (
    <RTooltip.Root open={open} defaultOpen={defaultOpen} onOpenChange={onOpenChange}>
      <RTooltip.Trigger asChild>{children}</RTooltip.Trigger>
      <RTooltip.Portal>
        <RTooltip.Content className="gx-tooltip" side={side} align={align} sideOffset={6} collisionPadding={8}>
          {content}
        </RTooltip.Content>
      </RTooltip.Portal>
    </RTooltip.Root>
  );

  return insideProvider ? tooltip : <TooltipProvider>{tooltip}</TooltipProvider>;
}
