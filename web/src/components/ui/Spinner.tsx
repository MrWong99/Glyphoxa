import type { CSSProperties } from "react";

// Spinner — the one busy indicator, on the .gx-spinner class (styles/base.css).
// It is 1em square and drawn in currentColor, so it follows the text beside it;
// pass `size` (CSS pixels) for a fixed box.
//
// Decorative by default: the thing that is busy says so itself (a Button with
// aria-busy, a Skeleton with role=status). Pass `label` only when the spinner
// stands alone, and it becomes a labelled status.

export function Spinner({
  size,
  label,
  className = "",
}: {
  size?: number;
  label?: string;
  className?: string;
}) {
  const cls = className ? `gx-spinner ${className}` : "gx-spinner";
  const style = size ? ({ "--gx-spinner-size": `${size}px` } as CSSProperties) : undefined;
  if (label) {
    return <span className={cls} style={style} role="status" aria-label={label} />;
  }
  return <span className={cls} style={style} aria-hidden="true" />;
}
