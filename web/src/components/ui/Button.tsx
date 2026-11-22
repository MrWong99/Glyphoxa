import { forwardRef } from "react";
import type { ButtonHTMLAttributes, ReactNode } from "react";

import { Spinner } from "./Spinner";

// Button — ported from the handoff components/core/Button.jsx onto the .gx-btn
// class vocabulary (CSS lifted into styles/components.css). Protocol mechanics:
// 2px border, bold, invert/glow on hover, in arcane dress.
//
// forwardRef so Radix `asChild` slots (e.g. AlertDialog Cancel/Action in
// ConfirmDialog, #209) can attach their ref — without it the ref is dropped and
// AlertDialog's focus management silently no-ops.
//
// `loading` is the one pending state: the button is disabled and aria-busy, a
// spinner replaces the label, and the label stays in the layout (invisible) so
// the button keeps its width and its accessible name. Callers no longer swap
// the label for "Saving…" or dim the button by hand.
//
// `iconOnly` makes the button a square around one icon. It has no visible
// text, so the type then REQUIRES an aria-label.

type Variant = "primary" | "secondary" | "ghost" | "gold" | "danger";
type Size = "sm" | "md" | "lg";

type BaseProps = {
  variant?: Variant;
  size?: Size;
  block?: boolean;
  iconStart?: ReactNode;
  iconEnd?: ReactNode;
  /** Pending: disabled, aria-busy, spinner over the (kept) label. */
  loading?: boolean;
} & ButtonHTMLAttributes<HTMLButtonElement>;

export type ButtonProps = BaseProps &
  ({ iconOnly?: false } | { iconOnly: true; "aria-label": string });

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  {
    variant = "primary",
    size = "md",
    block = false,
    iconStart,
    iconEnd,
    loading = false,
    iconOnly = false,
    disabled,
    className = "",
    children,
    ...props
  },
  ref,
) {
  const cls = [
    "gx-btn",
    `gx-btn--${variant}`,
    `gx-btn--${size}`,
    block ? "gx-btn--block" : "",
    iconOnly ? "gx-btn--icon" : "",
    className,
  ]
    .filter(Boolean)
    .join(" ");

  // In icon-only mode the children ARE the icon; give them the same sized box
  // an iconStart gets.
  const content = (
    <>
      {iconStart && <span className="gx-btn__icon">{iconStart}</span>}
      {iconOnly && children ? <span className="gx-btn__icon">{children}</span> : children}
      {iconEnd && <span className="gx-btn__icon">{iconEnd}</span>}
    </>
  );

  return (
    <button
      ref={ref}
      className={cls}
      {...props}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      data-loading={loading || undefined}
    >
      {loading ? (
        <>
          <span className="gx-btn__content">{content}</span>
          <span className="gx-btn__spinner">
            <Spinner />
          </span>
        </>
      ) : (
        content
      )}
    </button>
  );
});
