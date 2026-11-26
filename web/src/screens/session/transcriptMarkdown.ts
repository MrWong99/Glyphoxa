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
//   **Bart** (NPC) · 20:15␣␣
//   Well met, traveller.␣␣
//   The road east is closed.
//
//   **Player / GM** · 20:16␣␣
//   Where is the dragon?
//
// Inside a run every line but the last ends in a hard break (two trailing
// spaces, ␣␣ above): a bare newline is a soft break, and a Markdown renderer
// would fold the name and the whole run into one paragraph.
//
// Line text is written as it was said: an Agent's own inline markdown stays
// markdown, and a human line is speech-to-text, so no backslash goes in front
// of somebody's words. The one exception is a line that OPENS like Markdown
// block structure ("# ", "- ", "1. ", "> "): the screen shows such a line
// literally, so the export escapes that opening, or a spoken "1. we go left"
// would become a list and a "#" a heading.

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
  let run: string[] = [];
  const flush = () => {
    if (run.length > 0) out.push("", run.join(HARD_BREAK));
    run = [];
  };
  let prev: TranscriptLine | undefined;
  for (const line of lines) {
    if (!prev || prev.who !== line.who || prev.kind !== line.kind) {
      flush();
      const name = speakerName(line.who, line.kind, t);
      const tag = tagLabel(line.tag, line.kind, t);
      const clock = formatClockShort(line.ts);
      // A Butler still called "Butler" needs no "(Butler)" after its name.
      run.push(`**${name}**${tag && tag !== name ? ` (${tag})` : ""}${clock ? ` · ${clock}` : ""}`);
    }
    run.push(literalBlock(line.text));
    prev = line;
  }
  flush();
  return out.join("\n") + "\n";
}

// HARD_BREAK ends a line inside a run: two trailing spaces, then the newline.
const HARD_BREAK = "  \n";

// literalBlock escapes a line's opening when it would read as Markdown block
// structure (a heading, quote, bullet or ordered item, or a setext/thematic
// underline). Inline markdown is left alone.
export function literalBlock(text: string): string {
  const ordered = /^(\s*\d{1,9})([.)])(?=\s|$)/;
  if (ordered.test(text)) return text.replace(ordered, "$1\\$2");
  return text.replace(/^(\s*)(?=#{1,6}(?:\s|$)|>|[-+*](?:\s|$)|[-_*=]{3,}\s*$|=+\s*$)/, "$1\\");
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
