import type { HTMLAttributes, ReactNode } from "react";

// Badge — ported from the handoff components/core/Badge.jsx onto .gx-badge. Pill
// status/label; `live` + dot pulse for active sessions, semantic variants for
// state, neutral for metadata.

type Variant =
  | "arcane"
  | "live"
  | "success"
  | "warning"
  | "danger"
  | "info"
  | "gold"
  | "neutral"
  | "solid";

export function Badge({
  variant = "neutral",
  size = "md",
  dot = false,
  pulse = false,
  className = "",
  children,
  ...props
}: {
  variant?: Variant;
  size?: "sm" | "md";
  dot?: boolean;
  pulse?: boolean;
  children?: ReactNode;
} & HTMLAttributes<HTMLSpanElement>) {
  const cls = ["gx-badge", `gx-badge--${variant}`, size === "sm" ? "gx-badge--sm" : "", className]
    .filter(Boolean)
    .join(" ");
  return (
    <span className={cls} {...props}>
      {dot && <span className={"gx-badge__dot" + (pulse ? " gx-badge__dot--pulse" : "")} />}
      {children}
    </span>
  );
}
