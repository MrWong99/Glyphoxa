import type { ReactElement, ReactNode } from "react";
import * as RPopover from "@radix-ui/react-popover";

// Popover — a small non-modal panel anchored to its trigger, on a Radix
// Popover (ADR-0017): a filter panel, a quick form, a detail card. Radix
// supplies the behaviour — the trigger carries aria-expanded/aria-controls,
// opening moves focus into the panel, Escape or a press outside closes it and
// returns focus to the trigger, and the panel is portalled to <body> and kept
// inside the viewport.
//
// For a list of actions use Menu; for something the GM must answer before
// going on, Dialog.
//
//   <Popover label="Filter entries" trigger={<Button variant="secondary">Filter</Button>}>
//     …controls…
//   </Popover>
//
// The panel is a role=dialog element (Radix), and so it is marked
// data-gx-popover: usePopoverDismiss must not mistake this NON-modal panel for
// a modal layer and switch every hand-rolled popover's dismissal off while it
// is open.

type PopoverProps = {
  /** The element that toggles the popover. Must forward its ref (Button and IconButton do). */
  trigger: ReactElement;
  children: ReactNode;
  /** Accessible name of the panel. Required: a dialog needs one. */
  label: string;
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  side?: "top" | "right" | "bottom" | "left";
  align?: "start" | "center" | "end";
  className?: string;
};

export function Popover({
  trigger,
  children,
  label,
  open,
  defaultOpen,
  onOpenChange,
  side = "bottom",
  align = "start",
  className = "",
}: PopoverProps) {
  return (
    <RPopover.Root open={open} defaultOpen={defaultOpen} onOpenChange={onOpenChange}>
      <RPopover.Trigger asChild>{trigger}</RPopover.Trigger>
      <RPopover.Portal>
        <RPopover.Content
          className={className ? `gx-popover ${className}` : "gx-popover"}
          data-gx-popover=""
          aria-label={label}
          side={side}
          align={align}
          sideOffset={6}
          collisionPadding={8}
        >
          {children}
        </RPopover.Content>
      </RPopover.Portal>
    </RPopover.Root>
  );
}

// PopoverClose turns its single child into something that closes the popover
// it is rendered in (an Apply or Done button).
export function PopoverClose({ children }: { children: ReactElement }) {
  return <RPopover.Close asChild>{children}</RPopover.Close>;
}
