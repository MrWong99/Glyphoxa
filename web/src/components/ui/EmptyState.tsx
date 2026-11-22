import type { ReactNode } from "react";

// EmptyState — what a list, a panel or a search shows when it has nothing: an
// icon, one line that says what is missing, one or two that say why it matters
// or how it fills up, and the action that fills it. An empty first-run panel
// is the best place in the product to say what a feature is for; a grey
// sentence wastes it.
//
//   <EmptyState
//     icon={<Users size={22} />}
//     title="No NPCs yet"
//     description="NPCs are the characters your players talk to at the table."
//     action={<Button onClick={create}>New NPC</Button>}
//   />
//
// size="panel" (default) fills the space the content would have taken, inside
// a dashed frame. size="inline" is one quiet row for a place that already has
// a frame: a card section, a filtered list with no matches.
//
// It has no ARIA role: it is content, read in place. `titleAs` lets the title
// be a real heading where the page outline wants one.

export function EmptyState({
  icon,
  title,
  description,
  action,
  size = "panel",
  titleAs: Title = "p",
  className = "",
}: {
  /** A lucide icon, about 22px (panel) or 16px (inline). Decorative. */
  icon?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  /** One or two Buttons. */
  action?: ReactNode;
  size?: "panel" | "inline";
  titleAs?: "p" | "h2" | "h3" | "h4";
  className?: string;
}) {
  const cls = ["gx-empty", `gx-empty--${size}`, className].filter(Boolean).join(" ");
  return (
    <div className={cls}>
      {icon && (
        <span className="gx-empty__icon" aria-hidden="true">
          {icon}
        </span>
      )}
      <div className="gx-empty__text">
        <Title className="gx-empty__title">{title}</Title>
        {description && <p className="gx-empty__desc">{description}</p>}
      </div>
      {action && <div className="gx-empty__action">{action}</div>}
    </div>
  );
}
