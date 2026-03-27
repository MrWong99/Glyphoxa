"use client";

import { useState, useEffect } from "react";

function computeElapsedTime(startedAt: string): string {
  const start = new Date(startedAt).getTime();
  const diff = Math.max(0, Math.floor((Date.now() - start) / 1000));
  const h = Math.floor(diff / 3600);
  const m = Math.floor((diff % 3600) / 60);
  const s = diff % 60;
  return `${h}:${m.toString().padStart(2, "0")}:${s.toString().padStart(2, "0")}`;
}

export function LiveTimer({
  startedAt,
  className,
}: {
  startedAt: string;
  className?: string;
}) {
  const [elapsed, setElapsed] = useState(() => computeElapsedTime(startedAt));

  useEffect(() => {
    const interval = setInterval(
      () => setElapsed(computeElapsedTime(startedAt)),
      1000,
    );
    return () => clearInterval(interval);
  }, [startedAt]);

  return <span className={className ?? "font-mono tabular-nums"}>{elapsed}</span>;
}
