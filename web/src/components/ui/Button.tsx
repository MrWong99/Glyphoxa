import type { ButtonHTMLAttributes, ReactNode } from "react";

// Button — ported from the handoff components/core/Button.jsx onto the .gx-btn
// class vocabulary (CSS lifted into styles/components.css). Protocol mechanics:
// 2px border, bold, invert/glow on hover, in arcane dress.

type Variant = "primary" | "secondary" | "ghost" | "gold" | "danger";
type Size = "sm" | "md" | "lg";

export function Button({
  variant = "primary",
  size = "md",
  block = false,
  iconStart,
  iconEnd,
  className = "",
  children,
  ...props
}: {
  variant?: Variant;
  size?: Size;
  block?: boolean;
  iconStart?: ReactNode;
  iconEnd?: ReactNode;
} & ButtonHTMLAttributes<HTMLButtonElement>) {
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
    <button className={cls} {...props}>
      {iconStart && <span className="gx-btn__icon">{iconStart}</span>}
      {children}
      {iconEnd && <span className="gx-btn__icon">{iconEnd}</span>}
    </button>
  );
}
