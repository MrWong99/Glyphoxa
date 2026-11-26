import { describe, it, expect } from "vitest";

import { tDe, tEn } from "./testT";
import { literalBlock, transcriptFilename, transcriptMarkdown } from "./transcriptMarkdown";
import type { TranscriptLine } from "./useSessionEvents";

// Local-time instants, so the HH:MM in the output is the same in any timezone
// the suite runs in.
const at = (h: number, m: number) => new Date(2026, 10, 21, h, m, 5).toISOString();
const STARTED = new Date(2026, 10, 21, 20, 14);

const LINES: TranscriptLine[] = [
  { id: "a:t1", who: "Bart", tag: "NPC", kind: "npc", ts: at(20, 15), text: "Well met, traveller." },
  { id: "a:t2", who: "Bart", tag: "NPC", kind: "npc", ts: at(20, 15), text: "The road east is **closed**." },
  { id: "u:1", who: "Player / DM", kind: "player", ts: at(20, 16), text: "Where is the dragon?" },
  { id: "u:2", who: "Anna", kind: "player", ts: at(20, 17), text: "I draw my sword." },
  { id: "a:t3", who: "Butler", tag: "Butler", kind: "butler", ts: at(20, 18), text: "Roll for initiative." },
];

describe("transcriptMarkdown", () => {
  it("writes a script: a heading, then each speaker's run under one name line", () => {
    const md = transcriptMarkdown({
      campaignName: "The Sunless Citadel",
      startedAt: STARTED,
      lines: LINES,
      t: tEn,
      lang: "en",
    });
    const day = STARTED.toLocaleDateString("en", {
      weekday: "short",
      year: STARTED.getFullYear() === new Date().getFullYear() ? undefined : "numeric",
      month: "short",
      day: "numeric",
    });
    expect(md).toBe(
      [
        `# The Sunless Citadel — session of ${day}`,
        "",
        // Inside a run every line but the last ends in a hard break, so a
        // renderer keeps the name and each line on their own lines.
        "**Bart** (NPC) · 20:15  ",
        "Well met, traveller.  ",
        // Bart's second line joins his run: no second name line. His own
        // markdown is left as markdown.
        "The road east is **closed**.",
        "",
        "**Player / GM** · 20:16  ",
        "Where is the dragon?",
        "",
        "**Anna** · 20:17  ",
        "I draw my sword.",
        "",
        // A Butler still called "Butler" gets no "(Butler)" after its name.
        "**Butler** · 20:18  ",
        "Roll for initiative.",
        "",
      ].join("\n"),
    );
  });

  it("follows the display language for the title and the relay's labels", () => {
    const md = transcriptMarkdown({
      campaignName: "Die Sonnenlose Zitadelle",
      startedAt: STARTED,
      lines: LINES,
      t: tDe,
      lang: "de",
    });
    expect(md).toMatch(/^# Die Sonnenlose Zitadelle — Session vom /);
    expect(md).toContain("**Spieler / GM** · 20:16");
  });

  it("still has a title without a campaign name or a start", () => {
    expect(transcriptMarkdown({ campaignName: undefined, startedAt: STARTED, lines: [], t: tEn, lang: "en" })).toMatch(
      /^# Session of /,
    );
    expect(transcriptMarkdown({ campaignName: undefined, startedAt: null, lines: [], t: tEn, lang: "en" })).toBe(
      "# Session transcript\n",
    );
  });
});

describe("literalBlock", () => {
  it("escapes an opening that would read as Markdown block structure", () => {
    expect(literalBlock("# not a heading")).toBe("\\# not a heading");
    expect(literalBlock("> not a quote")).toBe("\\> not a quote");
    expect(literalBlock("- not a bullet")).toBe("\\- not a bullet");
    expect(literalBlock("1. we go left")).toBe("1\\. we go left");
    expect(literalBlock("2) then right")).toBe("2\\) then right");
    expect(literalBlock("===")).toBe("\\===");
    expect(literalBlock("---")).toBe("\\---");
  });

  it("leaves ordinary words and inline markdown alone", () => {
    for (const text of ["#hashtag", "**bold** move", "1.5 gold pieces", "a - b", "Where is the dragon?"]) {
      expect(literalBlock(text)).toBe(text);
    }
  });
});

describe("transcriptFilename", () => {
  it("is a plain, sortable file name", () => {
    expect(transcriptFilename("The Sunless Citadel", STARTED)).toBe("the-sunless-citadel-2026-11-21.md");
  });

  it("survives accents, punctuation and a missing name", () => {
    expect(transcriptFilename("Die Höhle: Teil 2!", STARTED)).toBe("die-hohle-teil-2-2026-11-21.md");
    expect(transcriptFilename(undefined, STARTED)).toBe("session-2026-11-21.md");
    expect(transcriptFilename("???", null)).toBe("session.md");
  });
});
