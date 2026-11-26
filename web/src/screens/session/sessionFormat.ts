import { timestampMs } from "@bufbuild/protobuf/wkt";

import type { VoiceSession } from "@gen/glyphoxa/management/v1/management_pb";
import type { Lang, TFunc } from "@/i18n";

// Dates, durations and money as the Session screen writes them. Module-level
// helpers hold no translation: `t` and `lang` are threaded in from the calling
// component, so every label follows the display language.

/** A protobuf Timestamp as epoch milliseconds, or null when unset. */
export function tsMs(ts: VoiceSession["startedAt"] | undefined): number | null {
  return ts ? Number(timestampMs(ts)) : null;
}

// formatUsd renders a USD estimate as a currency amount (#130). Always paired
// with an "estimated" label at the call site — it is an approximate figure, not
// a bill. The CURRENCY stays USD (that is what the providers bill in); only its
// presentation follows the display language, so a German operator reads
// "3,21 $" rather than "$3.21".
export function formatUsd(usd: number, lang: Lang): string {
  try {
    return new Intl.NumberFormat(lang, { style: "currency", currency: "USD" }).format(usd);
  } catch {
    // A runtime without the currency data still owes the operator a figure.
    return `$${usd.toFixed(2)}`;
  }
}

// formatStamp renders an instant as a short "Mon D, HH:MM" stamp in the display
// language's locale. An instant from a previous year carries the year — without
// it, two sessions a year apart render identical labels.
export function formatStamp(d: Date, lang: Lang): string {
  return d.toLocaleString(lang, {
    year: d.getFullYear() === new Date().getFullYear() ? undefined : "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

// formatDay renders an instant as "Sat, Nov 21" — the weekday is how a GM
// remembers a session ("the Saturday one"). The year appears once it differs.
export function formatDay(d: Date, lang: Lang): string {
  return d.toLocaleDateString(lang, {
    weekday: "short",
    year: d.getFullYear() === new Date().getFullYear() ? undefined : "numeric",
    month: "short",
    day: "numeric",
  });
}

/** An instant's time of day, "20:15" / "08:15 PM" by locale. */
export function formatTime(d: Date, lang: Lang): string {
  return d.toLocaleTimeString(lang, { hour: "2-digit", minute: "2-digit" });
}

/** How long a session ran in milliseconds, or null while it has no end. */
export function sessionDurationMs(vs: VoiceSession): number | null {
  const started = tsMs(vs.startedAt);
  const ended = tsMs(vs.endedAt);
  return started != null && ended != null ? Math.max(0, ended - started) : null;
}

/** A duration as "1h 30m" (rounded to the minute), in the display language. */
export function formatDuration(ms: number, t: TFunc): string {
  const minutes = Math.max(0, Math.round(ms / 60000));
  return t("session.durationHm", { h: Math.floor(minutes / 60), m: minutes % 60 });
}

/** "12 lines" / "1 line". */
export function linesLabel(n: number, t: TFunc): string {
  return n === 1 ? t("session.pickerLinesOne") : t("session.pickerLines", { n });
}

// SHORT_SESSION_MS is the length under which an ended session is "a false
// start" for the history list: it joined, nothing was said, it was stopped.
export const SHORT_SESSION_MS = 60_000;

/**
 * True for an ended session that is not worth a row of its own in the history:
 * nothing was transcribed, or it ran for under a minute. The running session
 * and a failed one are never minor — the first is where the GM is, the second
 * is something they need to see.
 */
export function isMinorSession(vs: VoiceSession): boolean {
  if (vs.status !== "ended") return false;
  if (vs.lineCount === 0) return true;
  const ms = sessionDurationMs(vs);
  return ms != null && ms < SHORT_SESSION_MS;
}
