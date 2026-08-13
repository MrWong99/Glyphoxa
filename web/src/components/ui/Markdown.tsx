import { Fragment, type ReactNode } from "react";

// Markdown rendering for LLM-authored prose (the planning chat's Butler
// replies, recaps, and Agent transcript lines). Hand-rolled rather than a
// react-markdown/remark dependency: the corpus is chat-sized model output, the
// needed grammar is a small fixed subset, and every node below is built as a
// React element tree — never dangerouslySetInnerHTML — so raw HTML in model
// output stays inert text by construction (React escapes it), with no
// sanitizer to configure or get wrong.
//
// Three exports, one per surface shape:
//   <Markdown>       block-level prose (chat bubbles, recap cards);
//   <InlineMarkdown> emphasis/code/links only, for single transcript lines
//                    where block structure (headings, lists) has no place;
//   stripMarkdown()  flatten to plain text for one-line ellipsized previews
//                    (entry-card snippets, palette rows) — the "disable
//                    markdown" treatment where rendering can't apply.
//
// Deliberately unsupported: raw HTML (stays literal text), setext headings,
// tables, images (an ![alt](url) renders as literal text — remote images from
// model output would be a fetch to an attacker-chosen host), reference links,
// and backslash escapes (model output in practice never uses them; a literal
// `\*` therefore stays visible).

// ── Inline grammar ──────────────────────────────────────────────────────────

// One alternation, tried leftmost-first: a code span outranks every emphasis
// marker inside it, bold (** / __) is listed before italic (* / _) so a double
// marker never half-matches as italic, and a [label](url) only counts as a
// link when the url is explicit http(s) — which is also the whole URL-scheme
// safety story: a javascript: or data: href can never get through the regex.
// The _italic_ variant requires word boundaries so snake_case_identifiers in
// prose never italicize.
const INLINE = new RegExp(
  [
    "(?<codeTick>`+)(?<code>[^`][\\s\\S]*?)\\k<codeTick>",
    "\\*\\*(?<bold>[^\\s*][\\s\\S]*?)\\*\\*",
    "__(?<boldU>[^\\s_][\\s\\S]*?)__",
    "~~(?<strike>[^\\s~][\\s\\S]*?)~~",
    "\\*(?<em>[^\\s*][\\s\\S]*?)\\*",
    "\\b_(?<emU>[^\\s_][\\s\\S]*?)_\\b",
    "\\[(?<label>[^\\]\\n]+)\\]\\((?<href>https?://[^\\s)]+)\\)",
  ].join("|"),
  "g",
);

// maxDepth caps nesting recursion (bold inside a link inside a quote…) so
// pathological marker soup degrades to literal text instead of deep recursion.
const maxDepth = 4;

// renderInline turns one run of inline text into React nodes. Text between
// matches passes through verbatim (React escapes it); emphasis and link labels
// recurse so `**[docs](https://…)**` nests naturally; code spans stay literal.
function renderInline(text: string, depth = 0): ReactNode[] {
  if (depth > maxDepth) return [text];
  const out: ReactNode[] = [];
  const re = new RegExp(INLINE.source, "g");
  let last = 0;
  let key = 0;
  for (let m = re.exec(text); m !== null; m = re.exec(text)) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const g = m.groups ?? {};
    if (g.code !== undefined) {
      out.push(
        <code key={key} className="gx-md__code">
          {g.code}
        </code>,
      );
    } else if (g.bold !== undefined || g.boldU !== undefined) {
      out.push(<strong key={key}>{renderInline((g.bold ?? g.boldU)!, depth + 1)}</strong>);
    } else if (g.strike !== undefined) {
      out.push(<del key={key}>{renderInline(g.strike, depth + 1)}</del>);
    } else if (g.em !== undefined || g.emU !== undefined) {
      out.push(<em key={key}>{renderInline((g.em ?? g.emU)!, depth + 1)}</em>);
    } else {
      // The link alternative — href is regex-guaranteed http(s). New tab +
      // noopener/noreferrer: model-authored destinations never get a window
      // handle back into the app.
      out.push(
        <a key={key} href={g.href} target="_blank" rel="noopener noreferrer">
          {renderInline(g.label!, depth + 1)}
        </a>,
      );
    }
    last = m.index + m[0].length;
    key++;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

// ── Block grammar ───────────────────────────────────────────────────────────

type ListEntry = { indent: number; ordered: boolean; text: string };
type ListItem = { text: string; child: List | null };
type List = { ordered: boolean; items: ListItem[] };

type Block =
  | { kind: "p"; lines: string[] }
  | { kind: "heading"; level: number; text: string }
  | { kind: "fence"; lang: string; code: string }
  | { kind: "quote"; blocks: Block[] }
  | { kind: "list"; list: List }
  | { kind: "hr" };

const FENCE_OPEN = /^ {0,3}```([^`\n]*)$/;
const FENCE_CLOSE = /^ {0,3}```+\s*$/;
const HEADING = /^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
// Checked BEFORE the list pattern: "- - -" / "***" are rules, not bullets.
const HR = /^ {0,3}([-*_])\s*(?:\1\s*){2,}$/;
const QUOTE = /^ {0,3}> ?(.*)$/;
const LIST = /^(\s*)(?:[-*+]|\d{1,9}[.)])\s+(.*)$/;

// buildList shapes a run of raw list entries into at most two levels: entries
// indented ≥2 spaces past the run's first entry nest under the preceding
// top-level item. Deeper indentation flattens into that child list — LLM chat
// output essentially never goes three deep, and a flattened third level reads
// fine where it does.
function buildList(entries: ListEntry[]): List {
  const base = entries[0].indent;
  const list: List = { ordered: entries[0].ordered, items: [] };
  for (const e of entries) {
    const parent = list.items[list.items.length - 1];
    if (e.indent >= base + 2 && parent) {
      if (!parent.child) parent.child = { ordered: e.ordered, items: [] };
      parent.child.items.push({ text: e.text, child: null });
    } else {
      list.items.push({ text: e.text, child: null });
    }
  }
  return list;
}

// parseBlocks is a line-based single pass: each iteration consumes one whole
// block. Paragraphs run until a blank line or the start of any other block, so
// a list glued directly under a prose line (no blank between — chat models do
// this constantly) still parses as a list.
function parseBlocks(text: string, depth = 0): Block[] {
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  const blocks: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (line.trim() === "") {
      i++;
      continue;
    }
    const fence = FENCE_OPEN.exec(line);
    if (fence) {
      const code: string[] = [];
      i++;
      while (i < lines.length && !FENCE_CLOSE.test(lines[i])) {
        code.push(lines[i]);
        i++;
      }
      i++; // past the closing fence (or EOF — an unclosed fence keeps its text)
      blocks.push({ kind: "fence", lang: fence[1].trim(), code: code.join("\n") });
      continue;
    }
    const heading = HEADING.exec(line);
    if (heading) {
      blocks.push({ kind: "heading", level: heading[1].length, text: heading[2] });
      i++;
      continue;
    }
    if (HR.test(line)) {
      blocks.push({ kind: "hr" });
      i++;
      continue;
    }
    if (QUOTE.test(line) && depth < maxDepth) {
      const inner: string[] = [];
      while (i < lines.length) {
        const q = QUOTE.exec(lines[i]);
        if (!q) break;
        inner.push(q[1]);
        i++;
      }
      blocks.push({ kind: "quote", blocks: parseBlocks(inner.join("\n"), depth + 1) });
      continue;
    }
    if (LIST.test(line)) {
      const entries: ListEntry[] = [];
      while (i < lines.length) {
        const m = LIST.exec(lines[i]);
        if (!m) break;
        entries.push({ indent: m[1].length, ordered: /\d/.test(lines[i].trim()[0]), text: m[2] });
        i++;
      }
      blocks.push({ kind: "list", list: buildList(entries) });
      continue;
    }
    // Paragraph: consecutive plain lines. Single newlines are kept as hard
    // breaks — chat prose treats them as intentional (the bubbles rendered
    // them via pre-wrap before this component existed).
    const para: string[] = [line];
    i++;
    while (
      i < lines.length &&
      lines[i].trim() !== "" &&
      !FENCE_OPEN.test(lines[i]) &&
      !HEADING.test(lines[i]) &&
      !HR.test(lines[i]) &&
      !QUOTE.test(lines[i]) &&
      !LIST.test(lines[i])
    ) {
      para.push(lines[i]);
      i++;
    }
    blocks.push({ kind: "p", lines: para });
  }
  return blocks;
}

// ── Rendering ───────────────────────────────────────────────────────────────

function renderList(list: List, keyBase: number): ReactNode {
  const Tag = list.ordered ? "ol" : "ul";
  return (
    <Tag key={keyBase} className="gx-md__list">
      {list.items.map((item, i) => (
        <li key={i}>
          {renderInline(item.text)}
          {item.child && renderList(item.child, i)}
        </li>
      ))}
    </Tag>
  );
}

function renderBlocks(blocks: Block[]): ReactNode[] {
  return blocks.map((b, i) => {
    switch (b.kind) {
      case "heading":
        // A styled <p> rather than a real <hN>: these fragments render inside
        // chat bubbles and cards, and injecting model-chosen heading levels
        // into the page's document outline would scramble it for assistive
        // tech. data-level drives the visual scale only.
        return (
          <p key={i} className="gx-md__heading" data-level={Math.min(b.level, 4)}>
            {renderInline(b.text)}
          </p>
        );
      case "fence":
        return (
          <pre key={i} className="gx-md__pre" data-lang={b.lang || undefined}>
            <code>{b.code}</code>
          </pre>
        );
      case "quote":
        return (
          <blockquote key={i} className="gx-md__quote">
            {renderBlocks(b.blocks)}
          </blockquote>
        );
      case "list":
        return renderList(b.list, i);
      case "hr":
        return <hr key={i} className="gx-md__hr" />;
      case "p":
        return (
          <p key={i} className="gx-md__p">
            {b.lines.map((line, j) => (
              <Fragment key={j}>
                {j > 0 && <br />}
                {renderInline(line)}
              </Fragment>
            ))}
          </p>
        );
    }
  });
}

/**
 * Markdown renders LLM-authored prose with block structure — the planning
 * chat's Butler bubbles and the recap card. The wrapper div resets
 * white-space to normal so it can sit inside containers that pre-wrap plain
 * text (the chat bubble), while fenced code keeps its own wrapping.
 */
export function Markdown({ text, className }: { text: string; className?: string }) {
  return (
    <div className={className ? `gx-md ${className}` : "gx-md"}>
      {renderBlocks(parseBlocks(text))}
    </div>
  );
}

/**
 * InlineMarkdown renders emphasis, code spans, and links only — for a single
 * transcript line, where an Agent's leaked `**bold**` should format but block
 * structure has no place in a one-row layout. Block markers (#, -, >) stay
 * literal text.
 */
export function InlineMarkdown({ text }: { text: string }) {
  return <>{renderInline(text)}</>;
}

// inlinePlain flattens inline markers to their text content, recursively so
// nested emphasis unwraps fully: code spans yield their code, links their
// label, emphasis its inner text.
function inlinePlain(text: string, depth = 0): string {
  if (depth > maxDepth) return text;
  return text.replace(new RegExp(INLINE.source, "g"), (...args) => {
    const g = args[args.length - 1] as Record<string, string | undefined>;
    if (g.code !== undefined) return g.code;
    const inner = g.bold ?? g.boldU ?? g.strike ?? g.em ?? g.emU ?? g.label;
    return inner !== undefined ? inlinePlain(inner, depth + 1) : (args[0] as string);
  });
}

function blockPlain(b: Block): string {
  switch (b.kind) {
    case "heading":
      return inlinePlain(b.text);
    case "fence":
      return b.code.replace(/\s+/g, " ").trim();
    case "quote":
      return b.blocks.map(blockPlain).join(" ");
    case "list":
      return b.list.items
        .map((it) =>
          [inlinePlain(it.text), ...(it.child?.items.map((c) => inlinePlain(c.text)) ?? [])].join(" "),
        )
        .join(" ");
    case "hr":
      return "";
    case "p":
      return b.lines.map((l) => inlinePlain(l)).join(" ");
  }
}

/**
 * stripMarkdown flattens markdown to plain text for one-line ellipsized
 * previews (entry-card snippets, palette rows, appearance quotes) — surfaces
 * where markdown can't render, so its syntax must not leak as literal `**`
 * noise. Blocks collapse to single-space-separated prose.
 */
export function stripMarkdown(text: string): string {
  return parseBlocks(text)
    .map(blockPlain)
    .filter((s) => s !== "")
    .join(" ")
    .replace(/\s+/g, " ")
    .trim();
}
