// Audio-tag parsing for Agent transcript lines.
//
// Expressive TTS models (ElevenLabs v3) take inline stage directions such as
// "[cheerfully]" or "[whispers]" in the text they speak, and the LLM writes
// them straight into the reply — so the transcript line that records the reply
// carries them too, often glued to the next word: "[cheerfully]Hier ist dein
// Wurf". splitAudioTags separates those directions from the spoken words so the
// transcript can render them as a subdued stage direction instead of raw
// bracket text.
//
// Only SHORT, LOWERCASE bracketed directions count: one to four words of
// lowercase letters (any script), with hyphens/apostrophes, at least three
// characters. Everything else stays text, untouched: citations "[1]",
// editorial "[sic]", markdown links "[label](https://…)", task boxes "[x]",
// capitalised or punctuated brackets "[Note: …]", "[cheerful ?]". No input
// character is ever dropped except the two brackets of a recognised tag and
// the whitespace that separated it from its neighbours (the renderer puts a
// single space back).

type AudioTagSegment = { kind: "text"; text: string } | { kind: "tag"; text: string };

// A candidate bracket: no nested brackets or newlines inside.
const BRACKET = /\[([^[\]\n]{1,40})\]/g;

// The tag body: lowercase letters (incl. accents/umlauts via \p{Ll} + marks),
// words separated by single spaces or hyphens/apostrophes.
const TAG_BODY = /^\p{Ll}[\p{Ll}\p{M}'’-]*(?: \p{Ll}[\p{Ll}\p{M}'’-]*){0,3}$/u;

// Lowercase editorial markers that are real bracket uses, not directions.
const EDITORIAL = new Set(["sic", "sic!", "citation needed", "emphasis added", "emphasis mine", "sic erat scriptum"]);

function isTag(body: string): boolean {
  return body.length >= 3 && TAG_BODY.test(body) && !EDITORIAL.has(body);
}

// splitAudioTags returns the line as alternating text/tag segments. A line with
// no tags comes back as a single text segment (the original string).
export function splitAudioTags(text: string): AudioTagSegment[] {
  const out: AudioTagSegment[] = [];
  let last = 0;
  for (const m of text.matchAll(BRACKET)) {
    const start = m.index;
    const end = start + m[0].length;
    const body = m[1];
    // A markdown link label "[label](…)" or image "![alt]" is never a tag.
    if (text[end] === "(" || text[start - 1] === "!") continue;
    if (!isTag(body)) continue;
    const before = text.slice(last, start).trimEnd();
    if (before !== "") out.push({ kind: "text", text: last === 0 ? before : before.trimStart() });
    out.push({ kind: "tag", text: body });
    last = end;
  }
  if (last === 0) return [{ kind: "text", text }];
  const rest = text.slice(last).trimStart();
  if (rest !== "") out.push({ kind: "text", text: rest });
  return out;
}
