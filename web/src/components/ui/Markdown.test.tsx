import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

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
});
