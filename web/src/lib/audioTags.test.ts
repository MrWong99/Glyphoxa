import { describe, it, expect } from "vitest";

import { splitAudioTags } from "./audioTags";

describe("splitAudioTags", () => {
  it("splits a leading tag glued to the sentence (the live-test line)", () => {
    expect(splitAudioTags("[cheerfully]Hier ist dein Wurf")).toEqual([
      { kind: "tag", text: "cheerfully" },
      { kind: "text", text: "Hier ist dein Wurf" },
    ]);
  });

  it("splits a spaced leading tag and drops only the separating space", () => {
    expect(splitAudioTags("[whispers] Hinter dir.")).toEqual([
      { kind: "tag", text: "whispers" },
      { kind: "text", text: "Hinter dir." },
    ]);
  });

  it("splits a mid-text tag and multi-word directions", () => {
    expect(splitAudioTags("Na gut. [laughs softly] Dann eben so. [sighs]")).toEqual([
      { kind: "text", text: "Na gut." },
      { kind: "tag", text: "laughs softly" },
      { kind: "text", text: "Dann eben so." },
      { kind: "tag", text: "sighs" },
    ]);
  });

  it("handles adjacent tags and non-ASCII lowercase directions", () => {
    expect(splitAudioTags("[excited][fröhlich]Los geht's!")).toEqual([
      { kind: "tag", text: "excited" },
      { kind: "tag", text: "fröhlich" },
      { kind: "text", text: "Los geht's!" },
    ]);
  });

  it.each([
    "He wrote it thus [sic] in the letter.",
    "As the rulebook says [1], advantage stacks.",
    "See [the docs](https://example.com/docs) for more.",
    "- [x] done",
    "[Note: the bridge is out]",
    "[cheerful ?][?] [....]",
    "Roll [d20] now",
    "[A] or [B]?",
    "Plain line with no brackets.",
  ])("leaves a normal bracket use as text: %s", (line) => {
    expect(splitAudioTags(line)).toEqual([{ kind: "text", text: line }]);
  });

  it("never drops text: segments rejoin to the input minus tag brackets and separators", () => {
    const line = "Gut. [pauses]  Und [cheerfully]jetzt [1] weiter.";
    const segs = splitAudioTags(line);
    expect(segs.map((s) => s.text).join(" ")).toBe("Gut. pauses Und cheerfully jetzt [1] weiter.");
  });
});
