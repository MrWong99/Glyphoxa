import type { CSSProperties, HTMLAttributes } from "react";

// Avatar — ported from the handoff components/core/Avatar.jsx onto .gx-avatar.
// Deterministic arcane-gradient initials (or an image); `speaking` lights a
// pulsing rune ring for the NPC currently voicing; `status` shows a presence dot.

type Size = "xs" | "sm" | "md" | "lg" | "xl";
type Status = "live" | "idle" | "offline" | null;

const HUES = [
  "linear-gradient(135deg,#3a8ee6,#9059ff)",
  "linear-gradient(135deg,#9059ff,#c139e6)",
  "linear-gradient(135deg,#00ddff,#0090ed)",
  "linear-gradient(135deg,#3fe1b0,#0090ed)",
  "linear-gradient(135deg,#ff7139,#e31587)",
  "linear-gradient(135deg,#ffbd4f,#ff7139)",
];

function hueFor(name = ""): string {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return HUES[h % HUES.length];
}

function initials(name = ""): string {
  return name
    .trim()
    .split(/\s+/)
    .slice(0, 2)
    .map((w) => w[0] || "")
    .join("")
    .toUpperCase();
}

export function Avatar({
  name = "",
  src = null,
  size = "md",
  shape = "circle",
  speaking = false,
  status = null,
  className = "",
  ...props
}: {
  name?: string;
  src?: string | null;
  size?: Size;
  shape?: "circle" | "rounded";
  speaking?: boolean;
  status?: Status;
} & HTMLAttributes<HTMLSpanElement>) {
  const cls = [
    "gx-avatar",
    `gx-avatar--${size}`,
    shape === "rounded" ? "gx-avatar--rounded" : "",
    speaking ? "gx-avatar--speaking" : "",
    className,
  ]
    .filter(Boolean)
    .join(" ");

  const statusColor =
    status === "live"
      ? "var(--status-live)"
      : status === "idle"
        ? "var(--status-warning)"
        : status === "offline"
          ? "var(--text-subtle)"
          : null;

  const style: CSSProperties | undefined = src ? undefined : { background: hueFor(name) };

  return (
    <span className={cls} style={style} {...props}>
      {src ? <img src={src} alt={name} /> : initials(name)}
      {statusColor && <span className="gx-avatar__status" style={{ background: statusColor }} />}
    </span>
  );
}
