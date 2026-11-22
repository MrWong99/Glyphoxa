import { createContext, useContext, useRef, type ReactElement, type ReactNode, type RefObject } from "react";
import * as RMenu from "@radix-ui/react-dropdown-menu";

import { noteMenuTrigger } from "./useRestoreFocus";

// Menu — a list of actions behind one trigger (a kebab on a card, a "New …"
// button with types), on a Radix DropdownMenu (ADR-0017). Radix supplies the
// behaviour: Enter, Space or ArrowDown on the trigger opens it with the first
// item focused; arrows, Home/End and typing a first letter move; Enter or
// Space runs the item; Escape closes and returns focus to the trigger. The
// surface is portalled to <body>, kept inside the viewport, and named by its
// trigger (so an icon-only trigger must be an IconButton, which has a label).
//
//   <Menu trigger={<IconButton label="Campaign actions"><MoreVertical /></IconButton>}>
//     <MenuItem icon={<Settings />} onSelect={openSettings}>Settings</MenuItem>
//     <MenuItem icon={<Download />} onSelect={exportCampaign}>Export</MenuItem>
//     <MenuSeparator />
//     <MenuItem tone="danger" icon={<Trash2 />} onSelect={askDelete}>Delete…</MenuItem>
//   </Menu>
//
// An item that opens a ConfirmDialog or Dialog just sets that dialog's open
// state in onSelect: the menu closes, the dialog takes focus, and when the
// dialog closes focus returns to this menu's trigger (useRestoreFocus).

const TriggerContext = createContext<RefObject<HTMLButtonElement | null> | null>(null);

type MenuProps = {
  /** The element that opens the menu. Must forward its ref (Button and IconButton do). */
  trigger: ReactElement;
  children: ReactNode;
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  side?: "top" | "right" | "bottom" | "left";
  align?: "start" | "center" | "end";
  className?: string;
};

export function Menu({
  trigger,
  children,
  open,
  defaultOpen,
  onOpenChange,
  side = "bottom",
  align = "end",
  className = "",
}: MenuProps) {
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  return (
    // modal={false}: a menu must not make the rest of the page inert or lock
    // scrolling; a press outside simply closes it.
    <RMenu.Root open={open} defaultOpen={defaultOpen} onOpenChange={onOpenChange} modal={false}>
      <RMenu.Trigger asChild ref={triggerRef}>
        {trigger}
      </RMenu.Trigger>
      <RMenu.Portal>
        <RMenu.Content
          className={className ? `gx-menu ${className}` : "gx-menu"}
          side={side}
          align={align}
          sideOffset={6}
          collisionPadding={8}
        >
          <TriggerContext.Provider value={triggerRef}>{children}</TriggerContext.Provider>
        </RMenu.Content>
      </RMenu.Portal>
    </RMenu.Root>
  );
}

export function MenuItem({
  children,
  onSelect,
  icon,
  hint,
  tone = "neutral",
  disabled = false,
}: {
  children: ReactNode;
  /** Runs when the item is chosen by click, Enter or Space; the menu then closes. */
  onSelect?: () => void;
  icon?: ReactNode;
  /** Trailing note, e.g. a keyboard shortcut. */
  hint?: ReactNode;
  tone?: "neutral" | "danger";
  disabled?: boolean;
}) {
  const triggerRef = useContext(TriggerContext);
  return (
    <RMenu.Item
      className={tone === "danger" ? "gx-menu__item gx-menu__item--danger" : "gx-menu__item"}
      disabled={disabled}
      onSelect={() => {
        // Before the action: if it opens a dialog, that dialog returns focus
        // to this menu's trigger when it closes.
        noteMenuTrigger(triggerRef?.current ?? null);
        onSelect?.();
      }}
    >
      {icon && (
        <span className="gx-menu__icon" aria-hidden="true">
          {icon}
        </span>
      )}
      <span className="gx-menu__text">{children}</span>
      {hint && <span className="gx-menu__hint">{hint}</span>}
    </RMenu.Item>
  );
}

// MenuLabel heads a group of items (not focusable, not an action).
export function MenuLabel({ children }: { children: ReactNode }) {
  return <RMenu.Label className="gx-menu__label">{children}</RMenu.Label>;
}

export function MenuSeparator() {
  return <RMenu.Separator className="gx-menu__separator" />;
}
