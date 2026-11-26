import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

import { InlineMarkdown, Markdown, stripMarkdown } from "./Markdown";

// The renderer's whole safety story is "React element tree, no raw HTML" —
// several tests below pin that property (HTML stays literal text, only http(s)
// links become anchors) alongside the grammar itself.

describe("Markdown (block)", () => {
  it("renders emphasis, code spans, and strikethrough", () => {
    const { container } = render(<Markdown text="a **bold** and *em* and `code` and ~~gone~~" />);
    expect(container.querySelector("strong")?.textContent).toBe("bold");
    expect(container.querySelector("em")?.textContent).toBe("em");
    expect(container.querySelector("code")?.textContent).toBe("code");
    expect(container.querySelector("del")?.textContent).toBe("gone");
  });

  it("renders __bold__ and _em_ underscore variants without breaking snake_case", () => {
    const { container } = render(<Markdown text="__b__ and _i_ keep snake_case_name intact" />);
    expect(container.querySelector("strong")?.textContent).toBe("b");
    expect(container.querySelector("em")?.textContent).toBe("i");
    expect(container.textContent).toContain("snake_case_name");
  });

  it("leaves intraword double underscores literal", () => {
    const { container } = render(<Markdown text="my__var__here and FILE__NAME__SUFFIX" />);
    expect(container.querySelector("strong")).toBeNull();
    expect(container.textContent).toContain("my__var__here");
    expect(container.textContent).toContain("FILE__NAME__SUFFIX");
  });

  it("keeps balanced parentheses inside a link's URL", () => {
    const { container } = render(
      <Markdown text="[Rust](https://en.wikipedia.org/wiki/Rust_(programming_language))" />,
    );
    const a = container.querySelector("a");
    expect(a?.getAttribute("href")).toBe("https://en.wikipedia.org/wiki/Rust_(programming_language)");
    // No stray ')' leaks after the anchor.
    expect(container.textContent).toBe("Rust");
  });

  it("renders http(s) links in a new tab and refuses javascript: urls", () => {
    const { container } = render(
      <Markdown text="[docs](https://example.com/a) [evil](javascript:alert(1))" />,
    );
    const a = container.querySelectorAll("a");
    expect(a).toHaveLength(1);
    expect(a[0].getAttribute("href")).toBe("https://example.com/a");
    expect(a[0].getAttribute("target")).toBe("_blank");
    expect(a[0].getAttribute("rel")).toBe("noopener noreferrer");
    // The refused link stays visible as literal text, not silently dropped.
    expect(container.textContent).toContain("[evil](javascript:alert(1))");
  });

  it("keeps raw HTML inert as literal text", () => {
    const { container } = render(<Markdown text={'<img src=x onerror=alert(1)> <b>hi</b>'} />);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("b")).toBeNull();
    expect(container.textContent).toContain("<b>hi</b>");
  });

  it("renders headings as styled paragraphs, never real hN elements", () => {
    const { container } = render(<Markdown text={"## Plot hooks\nprose"} />);
    expect(container.querySelector("h1,h2,h3,h4,h5,h6")).toBeNull();
    const heading = container.querySelector(".gx-md__heading");
    expect(heading?.textContent).toBe("Plot hooks");
    expect(heading?.getAttribute("data-level")).toBe("2");
  });

  it("renders unordered and ordered lists, including one nested level", () => {
    const { container } = render(
      <Markdown text={"- one\n- two\n  - two-a\n\n1. first\n2. second"} />,
    );
    const ul = container.querySelector("ul");
    expect(ul).not.toBeNull();
    // Top level has two items; the indented entry nests under "two".
    expect(ul?.children).toHaveLength(2);
    expect(container.querySelector("ul ul li")?.textContent).toBe("two-a");
    const ol = container.querySelector("ol");
    expect(ol?.children).toHaveLength(2);
    expect(ol?.textContent).toContain("first");
  });

  it("parses a list glued directly under a prose line", () => {
    const { container } = render(<Markdown text={"Ideas:\n- ambush\n- parley"} />);
    expect(container.querySelector("p")?.textContent).toBe("Ideas:");
    expect(container.querySelectorAll("li")).toHaveLength(2);
  });

  it("preserves an ordered list's start number", () => {
    const { container } = render(<Markdown text={"3. third\n4. fourth"} />);
    const ol = container.querySelector("ol");
    expect(ol?.getAttribute("start")).toBe("3");
    expect(ol?.children).toHaveLength(2);
  });

  it("does not let a mid-paragraph line starting with a big number become a list", () => {
    // CommonMark's interruption rule: only "1." may break a paragraph — else
    // prose with a hard break onto a year would swallow it into a list.
    const { container } = render(<Markdown text={"The siege ended.\n1225. The keep fell."} />);
    expect(container.querySelector("ol")).toBeNull();
    expect(container.textContent).toContain("1225. The keep fell.");
  });

  it("keeps a heading's trailing # when not space-separated", () => {
    const { container } = render(<Markdown text={"## Songs in C# and F#"} />);
    expect(container.querySelector(".gx-md__heading")?.textContent).toBe("Songs in C# and F#");
  });

  it("treats a rule line inside a list run as a rule, not a bullet", () => {
    const { container } = render(<Markdown text={"- item one\n- - -\n- item two"} />);
    expect(container.querySelector("hr")).not.toBeNull();
    const items = [...container.querySelectorAll("li")].map((li) => li.textContent);
    expect(items).toEqual(["item one", "item two"]);
  });

  it("renders fenced code with markers left literal inside", () => {
    const { container } = render(<Markdown text={"```js\nconst a = '**not bold**';\n```"} />);
    const pre = container.querySelector("pre");
    expect(pre?.getAttribute("data-lang")).toBe("js");
    expect(pre?.textContent).toBe("const a = '**not bold**';");
    expect(pre?.querySelector("strong")).toBeNull();
  });

  it("keeps an unclosed fence's text instead of dropping it", () => {
    const { container } = render(<Markdown text={"```\ntrailing code"} />);
    expect(container.querySelector("pre")?.textContent).toBe("trailing code");
  });

  it("renders blockquotes and horizontal rules", () => {
    const { container } = render(<Markdown text={"> quoted **loud**\n\n---"} />);
    expect(container.querySelector("blockquote strong")?.textContent).toBe("loud");
    expect(container.querySelector("hr")).not.toBeNull();
  });

  it("keeps single newlines inside a paragraph as hard breaks", () => {
    const { container } = render(<Markdown text={"line one\nline two"} />);
    expect(container.querySelectorAll("p")).toHaveLength(1);
    expect(container.querySelector("p br")).not.toBeNull();
  });

  it("merges className onto the gx-md root", () => {
    const { container } = render(<Markdown text="x" className="extra" />);
    expect(container.querySelector(".gx-md.extra")).not.toBeNull();
  });

  it("renders a pipe table with a header row and body rows", () => {
    const { container } = render(
      <Markdown text={"| Name | HP |\n| --- | --- |\n| Bart | 12 |\n| Ogre | 40 |"} />,
    );
    const table = container.querySelector("table.gx-md__table");
    expect(table).not.toBeNull();
    const ths = [...container.querySelectorAll("thead th")].map((th) => th.textContent);
    expect(ths).toEqual(["Name", "HP"]);
    const rows = [...container.querySelectorAll("tbody tr")];
    expect(rows).toHaveLength(2);
    expect([...rows[0].querySelectorAll("td")].map((td) => td.textContent)).toEqual(["Bart", "12"]);
  });

  it("accepts edge-pipe-less rows and renders inline markdown inside cells", () => {
    const { container } = render(
      <Markdown text={"Name | Notes\n--- | ---\n**Bart** | owes `20 gold`"} />,
    );
    expect(container.querySelector("tbody strong")?.textContent).toBe("Bart");
    expect(container.querySelector("tbody code")?.textContent).toBe("20 gold");
  });

  it("carries column alignment from the delimiter row as data-align", () => {
    const { container } = render(
      <Markdown text={"| a | b | c |\n| :--- | :---: | ---: |\n| 1 | 2 | 3 |"} />,
    );
    const tds = [...container.querySelectorAll("tbody td")];
    expect(tds.map((td) => td.getAttribute("data-align"))).toEqual(["left", "center", "right"]);
    const ths = [...container.querySelectorAll("thead th")];
    expect(ths[1].getAttribute("data-align")).toBe("center");
  });

  it("squares ragged body rows to the header's width", () => {
    const { container } = render(
      <Markdown text={"| a | b |\n| --- | --- |\n| short |\n| one | two | extra |"} />,
    );
    const rows = [...container.querySelectorAll("tbody tr")];
    expect([...rows[0].querySelectorAll("td")].map((td) => td.textContent)).toEqual(["short", ""]);
    expect([...rows[1].querySelectorAll("td")].map((td) => td.textContent)).toEqual(["one", "two"]);
  });

  it("treats an escaped pipe as a literal pipe inside a cell", () => {
    const { container } = render(
      <Markdown text={"| expr | value |\n| --- | --- |\n| a \\| b | union |"} />,
    );
    const tds = [...container.querySelectorAll("tbody td")].map((td) => td.textContent);
    expect(tds).toEqual(["a | b", "union"]);
  });

  it("leaves a header/delimiter cell-count mismatch as prose", () => {
    const { container } = render(<Markdown text={"| a | b |\n| --- |\n| 1 | 2 |"} />);
    expect(container.querySelector("table")).toBeNull();
    expect(container.textContent).toContain("| a | b |");
  });

  it("does not turn a lone pipe-bearing paragraph into a table", () => {
    const { container } = render(<Markdown text={"Either ambush | parley works."} />);
    expect(container.querySelector("table")).toBeNull();
  });

  it("lets a table interrupt a paragraph glued above it", () => {
    const { container } = render(
      <Markdown text={"Initiative order:\n| Who | Roll |\n| --- | --- |\n| Bart | 17 |"} />,
    );
    expect(container.querySelector("p")?.textContent).toBe("Initiative order:");
    expect(container.querySelector("table")).not.toBeNull();
  });

  it("ends the table at the first pipe-less line instead of swallowing prose", () => {
    const { container } = render(
      <Markdown text={"| a |\n| --- |\n| 1 |\nThe ogre attacks."} />,
    );
    expect(container.querySelectorAll("tbody tr")).toHaveLength(1);
    expect(container.querySelector("p")?.textContent).toBe("The ogre attacks.");
  });

  it("keeps a --- rule under prose whose only pipes are escaped or edge", () => {
    // HR outranks the delimiter row: an escaped/edge pipe collapses to one
    // splitRow cell, which must not pair with a bare "---" section divider
    // into a one-column table that eats the rule.
    for (const text of [
      "Use `cat\\|dog` to match either.\n---\nMore prose.",
      "| means OR in a regex.\n----",
      "Progress: 80% done |\n---",
    ]) {
      const { container } = render(<Markdown text={text} />);
      expect(container.querySelector("table")).toBeNull();
      expect(container.querySelector("hr")).not.toBeNull();
    }
  });

  it("still opens a single-column table from a piped delimiter row", () => {
    const { container } = render(<Markdown text={"| a |\n| --- |\n| 1 |"} />);
    expect(container.querySelector("thead th")?.textContent).toBe("a");
  });

  it("rejects a degenerate delimiter-shaped line in linear time", () => {
    // TABLE_DELIM's backtracking regex is gone; a delimiter-shaped line with
    // a huge whitespace run and one stray char must fail fast (the old regex
    // went quadratic — >10s at this size, tripping the test timeout).
    const { container } = render(
      <Markdown text={"col a | col b\n|-" + " ".repeat(160000) + "x"} />,
    );
    expect(container.querySelector("table")).toBeNull();
  });

  it("breaks the table at a glued heading, list, or quote line that contains a pipe", () => {
    const table = "| a | b |\n| --- | --- |\n| 1 | 2 |\n";
    const heading = render(<Markdown text={table + "## Results | Summary"} />).container;
    expect(heading.querySelectorAll("tbody tr")).toHaveLength(1);
    expect(heading.querySelector(".gx-md__heading")?.textContent).toBe("Results | Summary");
    const list = render(<Markdown text={table + "- note: `a | b` is a union"} />).container;
    expect(list.querySelectorAll("tbody tr")).toHaveLength(1);
    expect(list.querySelector("li code")?.textContent).toBe("a | b");
    const quote = render(<Markdown text={table + "> caveat: a | b differs"} />).container;
    expect(quote.querySelectorAll("tbody tr")).toHaveLength(1);
    expect(quote.querySelector("blockquote")?.textContent).toBe("caveat: a | b differs");
  });
});

describe("Markdown lists", () => {
  it("keeps a loose numbered list with blank lines and indented sub-bullets as one list", () => {
    const text = [
      "1. **The smuggler's offer**",
      "",
      "   Maren wants the party to move a crate past the watch.",
      "",
      "   - she pays in silver",
      "   - the crate ticks",
      "",
      "2. **The missing keeper**",
      "",
      "3. **A storm rolls in**",
    ].join("\n");
    const { container } = render(<Markdown text={text} />);
    const lists = container.querySelectorAll(".gx-md > ol");
    expect(lists).toHaveLength(1);
    const items = lists[0].querySelectorAll(":scope > li");
    expect(items).toHaveLength(3);
    // The continuation paragraph and the sub-bullets stay inside item one.
    expect(items[0].textContent).toContain("Maren wants the party");
    expect(items[0].querySelectorAll(":scope > ul > li")).toHaveLength(2);
    expect(items[0].querySelector("strong")?.textContent).toBe("The smuggler's offer");
    expect(lists[0].getAttribute("start")).toBeNull();
  });

  it("nests three levels deep", () => {
    const { container } = render(<Markdown text={"- act one\n  - the docks\n    - the crate\n- act two"} />);
    expect(container.querySelector("ul ul ul li")?.textContent).toBe("the crate");
    expect(container.querySelectorAll(".gx-md > ul > li")).toHaveLength(2);
  });

  it("keeps numbered sub-items under their item, whatever their number", () => {
    const { container } = render(<Markdown text={"1. Prep\n   2. maps\n   3. voices\n2. Play"} />);
    const top = container.querySelectorAll(".gx-md > ol > li");
    expect(top).toHaveLength(2);
    expect(top[0].querySelectorAll("ol li")).toHaveLength(2);
    expect(top[0].querySelector("ol")?.getAttribute("start")).toBe("2");
  });

  it("does not merge a bullet list and a numbered list separated by a blank line", () => {
    const { container } = render(<Markdown text={"- a\n- b\n\n1. one"} />);
    expect(container.querySelectorAll(".gx-md > ul > li")).toHaveLength(2);
    expect(container.querySelectorAll(".gx-md > ol > li")).toHaveLength(1);
  });

  it("ends the list at an unindented paragraph after a blank line", () => {
    const { container } = render(<Markdown text={"- a\n- b\n\nThat is all."} />);
    expect(container.querySelectorAll("li")).toHaveLength(2);
    expect(container.querySelector(".gx-md > p")?.textContent).toBe("That is all.");
  });

  it("renders task items as non-interactive checkboxes", () => {
    const { container } = render(<Markdown text={"- [ ] buy rope\n- [x] hire a guide\n- plain item"} />);
    const boxes = [...container.querySelectorAll<HTMLInputElement>("input[type=checkbox]")];
    expect(boxes).toHaveLength(2);
    expect(boxes.map((b) => b.checked)).toEqual([false, true]);
    for (const b of boxes) expect(b).toBeDisabled();
    const items = [...container.querySelectorAll("li")].map((li) => li.textContent);
    expect(items).toEqual(["buy rope", "hire a guide", "plain item"]);
    expect(container.querySelectorAll("li.gx-md__task")).toHaveLength(2);
  });

  it("keeps a fenced block inside its item", () => {
    const { container } = render(<Markdown text={"1. Run this:\n\n   ```\n   roll 1d20\n   ```\n2. Then this"} />);
    const items = container.querySelectorAll(".gx-md > ol > li");
    expect(items).toHaveLength(2);
    expect(items[0].querySelector("pre")?.textContent).toBe("roll 1d20");
  });

  it("renders half-arrived streaming input without losing or inventing text", () => {
    // Each prefix is a moment in a streaming reply: it must render as what has
    // arrived so far, never throw and never drop characters.
    const full = "1. **Hook one**\n\n   The keeper lies.\n\n   - [ ] ask Maren\n2. Hook two";
    for (let end = 1; end <= full.length; end++) {
      const prefix = full.slice(0, end);
      const { container, unmount } = render(<Markdown text={prefix} />);
      const visible = (container.textContent ?? "").replace(/\s+/g, "");
      // Everything typed so far shows up, minus the markup characters.
      const expected = prefix.replace(/\*\*(.+?)\*\*/g, "$1").replace(/^\s*(\d+\.|-)\s/gm, "").replace(/\[ \]\s/g, "");
      for (const word of expected.split(/\s+/).filter((w) => /^[A-Za-z]{3,}$/.test(w))) {
        expect(visible, `prefix ${JSON.stringify(prefix)}`).toContain(word);
      }
      unmount();
    }
    const done = render(<Markdown text={full} />).container;
    expect(done.querySelectorAll(".gx-md > ol > li")).toHaveLength(2);
    expect(done.querySelector("input[type=checkbox]")).not.toBeNull();
  });

  it("keeps a blank tail mid-stream from ending the list early", () => {
    const { container, rerender } = render(<Markdown text={"1. one\n\n"} />);
    expect(container.querySelectorAll("ol > li")).toHaveLength(1);
    rerender(<Markdown text={"1. one\n\n2. two"} />);
    expect(container.querySelectorAll("ol")).toHaveLength(1);
    expect(container.querySelectorAll("ol > li")).toHaveLength(2);
  });

  it("flattens nested list content for previews", () => {
    expect(stripMarkdown("1. one\n\n   more\n   - deep\n2. two")).toBe("one more deep two");
    expect(stripMarkdown("- [x] done")).toBe("done");
  });
});

describe("Markdown copyable code", () => {
  it("offers a copy button on each code block only when copyable", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const plain = render(<Markdown text={"```\nroll 1d20\n```"} />).container;
    expect(plain.querySelector("button")).toBeNull();

    render(<Markdown copyable text={"```\nroll 1d20\n```"} />);
    const button = screen.getByRole("button", { name: "Copy code" });
    fireEvent.click(button);
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("roll 1d20"));
    expect(await screen.findByRole("button", { name: "Copied" })).toBeInTheDocument();
  });
});

describe("InlineMarkdown", () => {
  it("formats emphasis but leaves block markers literal", () => {
    render(
      <span data-testid="line">
        <InlineMarkdown text="# not a heading with **bold** and `code`" />
      </span>,
    );
    const line = screen.getByTestId("line");
    expect(line.querySelector("strong")?.textContent).toBe("bold");
    expect(line.querySelector("code")?.textContent).toBe("code");
    expect(line.textContent).toContain("# not a heading");
  });

  it("leaves ElevenLabs-style audio tags untouched", () => {
    // Voiced Agent lines can carry leftover [whispers]-style tags; bare
    // brackets are not link syntax and must stay verbatim.
    render(
      <span data-testid="line">
        <InlineMarkdown text="[whispers] the key is under the floor" />
      </span>,
    );
    expect(screen.getByTestId("line").textContent).toBe("[whispers] the key is under the floor");
  });

  it("renders plain text unchanged", () => {
    render(
      <span data-testid="line">
        <InlineMarkdown text="Just a spoken sentence, 2 * 3 = 6." />
      </span>,
    );
    expect(screen.getByTestId("line").textContent).toBe("Just a spoken sentence, 2 * 3 = 6.");
  });
});

describe("stripMarkdown", () => {
  it("flattens block and inline syntax to plain prose", () => {
    const text = "## Title\n\nA **bold** [link](https://x.dev) here.\n\n- item one\n- item two";
    expect(stripMarkdown(text)).toBe("Title A bold link here. item one item two");
  });

  it("unwraps nested emphasis and keeps code content", () => {
    expect(stripMarkdown("**a _deep_ one** and `raw()`")).toBe("a deep one and raw()");
  });

  it("drops rules and collapses fenced code onto one line", () => {
    expect(stripMarkdown("---\n```\nlet a;\nlet b;\n```")).toBe("let a; let b;");
  });

  it("returns plain text unchanged", () => {
    expect(stripMarkdown("Bart owes the ogre 20 gold.")).toBe("Bart owes the ogre 20 gold.");
  });

  it("flattens a table to space-separated cell text", () => {
    expect(stripMarkdown("| Name | HP |\n| --- | --- |\n| **Bart** | 12 |")).toBe(
      "Name HP Bart 12",
    );
  });
});
