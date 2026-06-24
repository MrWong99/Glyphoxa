import type { HTMLAttributes, ReactNode } from "react";

// Card + header/title/body — ported from the handoff components/core/Card.jsx
// onto the .gx-card class vocabulary. The arcane panel on the ink-card surface.

export function Card({
  interactive = false,
  live = false,
  flat = false,
  accent = false,
  className = "",
  children,
  ...props
}: {
  interactive?: boolean;
  live?: boolean;
  flat?: boolean;
  accent?: boolean;
} & HTMLAttributes<HTMLDivElement>) {
  const cls = [
    "gx-card",
    interactive ? "gx-card--interactive" : "",
    live ? "gx-card--live" : "",
    flat ? "gx-card--flat" : "",
    accent ? "gx-card--accent" : "",
    className,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <div className={cls} {...props}>
      {children}
    </div>
  );
}

export function CardHeader({ className = "", children, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={"gx-card__header " + className} {...props}>
      {children}
    </div>
  );
}

export function CardTitle({
  className = "",
  children,
  ...props
}: HTMLAttributes<HTMLHeadingElement> & { children?: ReactNode }) {
  return (
    <h3 className={"gx-card__title " + className} {...props}>
      {children}
    </h3>
  );
}

export function CardBody({ className = "", children, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={"gx-card__body " + className} {...props}>
      {children}
    </div>
  );
}
