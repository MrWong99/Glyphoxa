import type { Lang, TFunc } from "@/i18n";
import { formatDay } from "./sessionFormat";
import { speakerName, tagLabel } from "./speakers";
import { formatClockShort, type TranscriptLine } from "./useSessionEvents";

// An ended session's transcript as a Markdown document: what the GM pastes into
// their campaign notes or keeps as a file. Grouped like the screen — a speaker's
// consecutive lines sit under one heading line with the time the run started —
// so it reads as a script, not a log.
//
//   # The Sunless Citadel — session of Sat, Nov 21
//
//   **Bart** (NPC) · 20:15
//   Well met, traveller.
//   The road east is closed.
//
//   **Player / GM** · 20:16
//   Where is the dragon?
//
// Line text is written as it was said: an Agent's own markdown is already
// markdown, and a human line is speech-to-text — escaping it would only put
// backslashes in front of somebody's words.

export function transcriptMarkdown({
  campaignName,
  startedAt,
  lines,
  t,
  lang,
}: {
  campaignName: string | undefined;
  startedAt: Date | null;
  lines: readonly TranscriptLine[];
  t: TFunc;
  lang: Lang;
}): string {
  const day = startedAt ? formatDay(startedAt, lang) : "";
  const title =
    campaignName && day
      ? t("session.exportTitle", { campaign: campaignName, date: day })
      : day
        ? t("session.exportTitleNoCampaign", { date: day })
        : (campaignName ?? t("session.sessionTranscript"));

  const out: string[] = [`# ${title}`];
  let prev: TranscriptLine | undefined;
  for (const line of lines) {
    if (!prev || prev.who !== line.who || prev.kind !== line.kind) {
      const name = speakerName(line.who, line.kind, t);
      const tag = tagLabel(line.tag, line.kind, t);
      const clock = formatClockShort(line.ts);
      // A Butler still called "Butler" needs no "(Butler)" after its name.
      out.push("", `**${name}**${tag && tag !== name ? ` (${tag})` : ""}${clock ? ` · ${clock}` : ""}`);
    }
    out.push(line.text);
    prev = line;
  }
  return out.join("\n") + "\n";
}

/** A filesystem-safe file name for the export: "sunless-citadel-2026-11-21.md". */
export function transcriptFilename(campaignName: string | undefined, startedAt: Date | null): string {
  const slug = (campaignName ?? "")
    .normalize("NFKD")
    .replace(/[̀-ͯ]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  const pad = (n: number) => String(n).padStart(2, "0");
  const date = startedAt
    ? `${startedAt.getFullYear()}-${pad(startedAt.getMonth() + 1)}-${pad(startedAt.getDate())}`
    : "";
  return [slug || "session", date].filter(Boolean).join("-") + ".md";
}
