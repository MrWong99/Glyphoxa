import { forwardRef } from "react";
import type { ButtonHTMLAttributes, ReactNode } from "react";

// Button — ported from the handoff components/core/Button.jsx onto the .gx-btn
// class vocabulary (CSS lifted into styles/components.css). Protocol mechanics:
// 2px border, bold, invert/glow on hover, in arcane dress.
//
// forwardRef so Radix `asChild` slots (e.g. AlertDialog Cancel/Action in
// ConfirmDialog, #209) can attach their ref — without it the ref is dropped and
// AlertDialog's focus management silently no-ops.

type Variant = "primary" | "secondary" | "ghost" | "gold" | "danger";
type Size = "sm" | "md" | "lg";

type ButtonProps = {
  variant?: Variant;
  size?: Size;
  block?: boolean;
  iconStart?: ReactNode;
  iconEnd?: ReactNode;
} & ButtonHTMLAttributes<HTMLButtonElement>;

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "primary", size = "md", block = false, iconStart, iconEnd, className = "", children, ...props },
  ref,
) {
  const cls = [
    "gx-btn",
    `gx-btn--${variant}`,
    `gx-btn--${size}`,
    block ? "gx-btn--block" : "",
    className,
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <button ref={ref} className={cls} {...props}>
      {iconStart && <span className="gx-btn__icon">{iconStart}</span>}
      {children}
      {iconEnd && <span className="gx-btn__icon">{iconEnd}</span>}
    </button>
  );
});
