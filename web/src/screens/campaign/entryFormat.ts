import { timestampDate } from "@bufbuild/protobuf/wkt";

import type { Node } from "@gen/glyphoxa/management/v1/management_pb";
import type { Lang } from "@/i18n";

// Small formatting helpers shared by the wiki's cards and its entry page.

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/**
 * relativeTime says how long ago `at` was, in the display language: "3 days
 * ago", "vor 3 Tagen". Past a month it gives the date instead, because "14
 * months ago" is harder to place than "3 Sep 2025". It holds no translated
 * string of its own: Intl does the wording.
 */
export function relativeTime(at: Date, lang: Lang, now: Date = new Date()): string {
  const ago = now.getTime() - at.getTime();
  const rtf = new Intl.RelativeTimeFormat(lang, { numeric: "auto" });
  if (ago < MINUTE) return rtf.format(0, "minute");
  if (ago < HOUR) return rtf.format(-Math.floor(ago / MINUTE), "minute");
  if (ago < DAY) return rtf.format(-Math.floor(ago / HOUR), "hour");
  if (ago < 30 * DAY) return rtf.format(-Math.floor(ago / DAY), "day");
  return at.toLocaleDateString(lang, { dateStyle: "medium" });
}

/** When the entry last changed, or null for a row that carries no stamp. */
export function updatedAt(node: Pick<Node, "updatedAt" | "createdAt">): Date | null {
  const stamp = node.updatedAt ?? node.createdAt;
  return stamp ? timestampDate(stamp) : null;
}

/** Milliseconds since the epoch for sorting; 0 for a row with no stamp. */
export function stampMs(stamp: Node["updatedAt"]): number {
  return stamp ? timestampDate(stamp).getTime() : 0;
}
