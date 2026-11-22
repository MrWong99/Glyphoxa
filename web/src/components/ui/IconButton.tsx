import { forwardRef } from "react";
import type { ButtonHTMLAttributes, ReactNode } from "react";

import { Spinner } from "./Spinner";
import { Tooltip } from "./Tooltip";

// IconButton — a square button around one icon (row actions, close buttons,
// toolbars) on the .gx-icon-btn classes: 28px (sm) or 36px (md), growing to
// 40px on a touch screen.
//
// `label` is REQUIRED: it is the accessible name (aria-label) and, by default,
// the tooltip a sighted user gets on hover or keyboard focus — an icon alone
// is a guess. Pass tooltip={false} where the tooltip would only repeat what is
// already on screen (a Close button beside a dialog title) or would cover what
// the button opens.
//
// forwardRef + prop spreading, so it can be the trigger of a Menu, Popover or
// Dialog (Radix asChild).

export type IconButtonProps = {
  label: string;
  size?: "sm" | "md";
  tone?: "neutral" | "danger";
  /** Show `label` as a tooltip on hover and keyboard focus. Default true. */
  tooltip?: boolean;
  tooltipSide?: "top" | "right" | "bottom" | "left";
  /** Pending: disabled, aria-busy, spinner in place of the icon. */
  loading?: boolean;
  children: ReactNode;
} & Omit<ButtonHTMLAttributes<HTMLButtonElement>, "aria-label" | "children">;

export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  {
    label,
    size = "md",
    tone = "neutral",
    tooltip = true,
    tooltipSide = "top",
    loading = false,
    disabled,
    className = "",
    type = "button",
    children,
    ...props
  },
  ref,
) {
  const cls = ["gx-icon-btn", `gx-icon-btn--${size}`, tone === "danger" ? "gx-icon-btn--danger" : "", className]
    .filter(Boolean)
    .join(" ");

  const button = (
    <button
      ref={ref}
      type={type}
      className={cls}
      {...props}
      aria-label={label}
      disabled={disabled || loading}
      {...(loading ? { "aria-busy": true, "data-loading": true } : {})}
    >
      {children}
      {loading && (
        <span className="gx-btn__spinner">
          <Spinner />
        </span>
      )}
    </button>
  );

  // A disabled button fires no pointer or focus events, so a tooltip on it
  // could never open; skip the wrapper rather than mount a dead one.
  if (!tooltip || disabled || loading) return button;
  return (
    <Tooltip content={label} side={tooltipSide}>
      {button}
    </Tooltip>
  );
});
