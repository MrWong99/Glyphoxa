import { createContext, useContext, type CSSProperties, type ReactNode } from "react";

import { useI18n } from "@/i18n";

// Skeleton — the loading placeholder, shaped like what is loading, on the
// .gx-skeleton shimmer (styles/components.css). Three shapes:
//
//   <Skeleton lines={3} />               a paragraph or a few fields
//   <Skeleton.Row count={5} />           list rows: avatar + two lines of text
//   <Skeleton.Block ratio="16 / 9" />    an image, a map, a card
//
// Each one is a status region (role=status, aria-busy, named "Loading…"), so a
// screen reader hears that something is on its way instead of silence; the
// bars themselves are decoration. To build a composite placeholder, nest
// shapes inside one <Skeleton>…</Skeleton>: only the outermost announces.
//
// Prefer QueryBoundary, which shows a skeleton while a query is pending and
// handles the failure too.

const Nested = createContext(false);

type CommonProps = {
  /** Accessible name; defaults to the shared "Loading…". */
  label?: string;
  className?: string;
};

function Status({
  label,
  className = "",
  children,
}: CommonProps & { children: ReactNode }) {
  const { t } = useI18n();
  const nested = useContext(Nested);
  const cls = className ? `gx-skeleton-group ${className}` : "gx-skeleton-group";
  if (nested) {
    return (
      <div className={cls} aria-hidden="true">
        {children}
      </div>
    );
  }
  return (
    <div className={cls} role="status" aria-busy="true" aria-label={label ?? t("common.loading")}>
      <Nested.Provider value={true}>{children}</Nested.Provider>
    </div>
  );
}

function Lines({ lines }: { lines: number }) {
  return (
    <>
      {Array.from({ length: Math.max(1, lines) }, (_, i) => (
        <span
          key={i}
          className="gx-skeleton gx-skeleton--line"
          // The last of several lines is shorter, the way a paragraph ends.
          data-short={lines > 1 && i === lines - 1 ? "true" : undefined}
          aria-hidden="true"
        />
      ))}
    </>
  );
}

export function Skeleton({
  lines = 3,
  children,
  ...common
}: CommonProps & {
  /** Number of text lines. Ignored when children are given. */
  lines?: number;
  /** Custom composition of Skeleton shapes; replaces the lines. */
  children?: ReactNode;
}) {
  return <Status {...common}>{children ?? <Lines lines={lines} />}</Status>;
}

function SkeletonRow({
  count = 3,
  avatar = true,
  ...common
}: CommonProps & {
  /** Number of rows. */
  count?: number;
  /** Lead each row with a round avatar placeholder. */
  avatar?: boolean;
}) {
  return (
    <Status {...common}>
      {Array.from({ length: Math.max(1, count) }, (_, i) => (
        <div key={i} className="gx-skeleton-row" aria-hidden="true">
          {avatar && <span className="gx-skeleton gx-skeleton--avatar" />}
          <div className="gx-skeleton-row__text">
            <Lines lines={2} />
          </div>
        </div>
      ))}
    </Status>
  );
}

function SkeletonBlock({
  ratio = "16 / 9",
  height,
  ...common
}: CommonProps & {
  /** CSS aspect-ratio of the block, e.g. "16 / 9" or "1". */
  ratio?: string;
  /** Fixed height in CSS pixels; when set it replaces the ratio. */
  height?: number;
}) {
  const style: CSSProperties = height ? { height } : { aspectRatio: ratio };
  return (
    <Status {...common}>
      <span className="gx-skeleton gx-skeleton--block" style={style} aria-hidden="true" />
    </Status>
  );
}

Skeleton.Row = SkeletonRow;
Skeleton.Block = SkeletonBlock;
