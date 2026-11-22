import type { ReactNode } from "react";

// SegmentedControl — pick one of a few views or modes (Roster / Prep, List /
// Map / Checkup). The chosen segment is filled; the others are plain, at full
// strength, so a choice you have not made does not look disabled.
//
// Hand-rolled on purpose, not a Radix ToggleGroup: it keeps the markup the
// screens' existing view switches already have — a role=group of buttons, each
// with aria-pressed — so a screen can adopt it without its tests changing, and
// a screen reader hears "Roster, pressed" exactly as before. Every segment is
// a tab stop (the toggle-button pattern, not a radio group's roving focus).
//
//   <SegmentedControl
//     label="Cast view"
//     value={mode}
//     onChange={setMode}
//     options={[{ value: "edit", label: "Roster" }, { value: "prep", label: "Prep" }]}
//   />
//
// onChange is only called for a segment that is not already the chosen one, so
// a guard wrapped around it (unsaved edits) is never asked about a no-op.

export type SegmentOption<T extends string> = {
  value: T;
  label: ReactNode;
  icon?: ReactNode;
  /** A small trailing number, e.g. the items in that view. */
  count?: number;
  disabled?: boolean;
};

export function SegmentedControl<T extends string>({
  label,
  value,
  onChange,
  options,
  size = "md",
  block = false,
  className = "",
}: {
  /** Accessible name of the group. */
  label: string;
  value: T;
  onChange: (value: T) => void;
  options: readonly SegmentOption<T>[];
  size?: "sm" | "md";
  /** Stretch to the container's width with equal segments. */
  block?: boolean;
  className?: string;
}) {
  const cls = ["gx-seg", size === "sm" ? "gx-seg--sm" : "", block ? "gx-seg--block" : "", className]
    .filter(Boolean)
    .join(" ");
  return (
    <div className={cls} role="group" aria-label={label}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          className="gx-seg__item"
          aria-pressed={option.value === value}
          disabled={option.disabled}
          onClick={() => {
            if (option.value !== value) onChange(option.value);
          }}
        >
          {option.icon && (
            <span className="gx-btn__icon" aria-hidden="true">
              {option.icon}
            </span>
          )}
          {option.label}
          {/* The space is for the accessible name ("Map 12", not "Map12");
              the flex gap does the visual spacing. */}
          {option.count !== undefined && (
            <>
              {" "}
              <span className="gx-seg__count">{option.count}</span>
            </>
          )}
        </button>
      ))}
    </div>
  );
}
