// @vitest-environment node
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import { SHARE_IMAGE_PATH, publicOrigin, shareTags, withLargeCard } from "./shareMeta";

// What a crawler gets: the static document (web/index.html), robots.txt, and
// the tags the build adds when it knows the deployment's address. None of it
// runs in the app, so it is pinned here, against the files as they ship.

const web = fileURLToPath(new URL("../../", import.meta.url));
const read = (path: string) => readFileSync(`${web}${path}`, "utf8");
const indexHtml = read("index.html");

const metaContent = (html: string, attr: "name" | "property", key: string): string | undefined =>
  new RegExp(`<meta\\s+${attr}="${key}"\\s+content="([^"]*)"`).exec(html.replace(/\s+/g, " "))?.[1];

describe("publicOrigin", () => {
  it("answers null when the build was not told an origin", () => {
    expect(publicOrigin(undefined)).toBeNull();
    expect(publicOrigin("")).toBeNull();
    expect(publicOrigin("   ")).toBeNull();
  });

  it("normalizes to a bare origin", () => {
    expect(publicOrigin("https://glyphoxa.example")).toBe("https://glyphoxa.example");
    expect(publicOrigin(" https://glyphoxa.example/ ")).toBe("https://glyphoxa.example");
    expect(publicOrigin("https://glyphoxa.example/some/path?x=1")).toBe("https://glyphoxa.example");
    expect(publicOrigin("http://localhost:8080")).toBe("http://localhost:8080");
  });

  it("fails the build on a value that is not an http(s) URL, instead of shipping a broken preview", () => {
    expect(() => publicOrigin("glyphoxa.example")).toThrow(/VITE_PUBLIC_ORIGIN/);
    expect(() => publicOrigin("ftp://glyphoxa.example")).toThrow(/http\(s\)/);
  });
});

describe("shareTags", () => {
  it("emits nothing without an origin — no relative image a crawler cannot resolve", () => {
    expect(shareTags(undefined)).toEqual([]);
    expect(shareTags("")).toEqual([]);
  });

  it("emits og:url, og:image (with its size) and the twitter image for a known origin", () => {
    const tags = shareTags("https://glyphoxa.example/");
    const byKey = Object.fromEntries(tags.map((t) => [t.attrs.property ?? t.attrs.name, t.attrs.content]));
    expect(byKey).toEqual({
      "og:url": "https://glyphoxa.example/",
      "og:image": "https://glyphoxa.example/og-image.jpg",
      "og:image:width": "1200",
      "og:image:height": "630",
      "twitter:image": "https://glyphoxa.example/og-image.jpg",
    });
    for (const tag of tags) expect(tag).toMatchObject({ tag: "meta", injectTo: "head" });
  });

  it("points at an image that is really in the build's public files", () => {
    const file = `${web}public${SHARE_IMAGE_PATH}`;
    expect(existsSync(file)).toBe(true);
    // A JPEG (every link-preview crawler reads one), by its magic bytes.
    const head = readFileSync(file).subarray(0, 3);
    expect([...head]).toEqual([0xff, 0xd8, 0xff]);
  });
});

describe("withLargeCard", () => {
  it("upgrades the document's own twitter:card, and nothing else", () => {
    const upgraded = withLargeCard(indexHtml);
    expect(metaContent(upgraded, "name", "twitter:card")).toBe("summary_large_image");
    expect(upgraded.length - indexHtml.length).toBe("_large_image".length);
  });
});

describe("web/index.html", () => {
  it("describes the product to a crawler: title, description, Open Graph text, card type", () => {
    expect(/<title>([^<]*)<\/title>/.exec(indexHtml)?.[1]).toMatch(/^Glyphoxa — /);
    const description = metaContent(indexHtml, "name", "description") ?? "";
    expect(description).toMatch(/Discord/);
    // Search engines cut a description near 160 characters; link previews a
    // little later. Keep it inside what is actually shown.
    expect(description.length).toBeGreaterThan(70);
    expect(description.length).toBeLessThanOrEqual(200);

    expect(metaContent(indexHtml, "property", "og:type")).toBe("website");
    expect(metaContent(indexHtml, "property", "og:title")).toMatch(/^Glyphoxa — /);
    expect(metaContent(indexHtml, "property", "og:description")).toMatch(/Discord/);
    expect(metaContent(indexHtml, "name", "twitter:card")).toBe("summary");
  });

  it("carries no absolute-url tag of its own — those come from the build, with a real origin", () => {
    expect(indexHtml).not.toMatch(/property="og:image"/);
    expect(indexHtml).not.toMatch(/property="og:url"/);
    expect(indexHtml).not.toMatch(/name="twitter:image"/);
  });

  it("paints the browser chrome in the app's base surface colour", () => {
    // theme-color cannot read a CSS custom property, so the hex is repeated;
    // this keeps it on --surface-base (--color-ink-90).
    const tokens = read("src/styles/tokens.css");
    expect(/--surface-base:\s*var\(--color-ink-90\)/.test(tokens)).toBe(true);
    const ink90 = /--color-ink-90:\s*(#[0-9a-f]{6})/i.exec(tokens)?.[1];
    expect(metaContent(indexHtml, "name", "theme-color")).toBe(ink90);
  });

  it("has words in #root before the app mounts: a heading, a sentence and a way in", () => {
    const root = /<div id="root">([\s\S]*?)<\/div>\s*<script/.exec(indexHtml)?.[1] ?? "";
    expect(root).toMatch(/<h1>Glyphoxa<\/h1>/);
    expect(root).toMatch(/<p>[\s\S]*Discord voice channel[\s\S]*<\/p>/);
    expect(root).toMatch(/<a [^>]*href="\/login"[^>]*>[^<]+<\/a>/);
    // No script of its own: the page's CSP allows none inline (internal/spa).
    expect(root).not.toMatch(/<script/);
  });

  it("makes no promise the product does not keep (#258): no price, no roadmap", () => {
    const text = indexHtml.replace(/<!--[\s\S]*?-->/g, "");
    expect(text).not.toMatch(/\$\s?\d|€\s?\d|\d\s?€/);
    expect(text).not.toMatch(/coming soon|soon/i);
  });
});

describe("web/public/robots.txt", () => {
  const robots = read("public/robots.txt");
  const rules = robots
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "" && !line.startsWith("#"));

  it("applies to every crawler and keeps them out of the app, auth, onboarding and the API", () => {
    expect(rules[0]).toBe("User-agent: *");
    expect(rules.slice(1).sort()).toEqual(
      ["Disallow: /api/", "Disallow: /auth/", "Disallow: /onboarding/", "Disallow: /t/"].sort(),
    );
  });

  it("leaves the public pages crawlable", () => {
    const disallowed = rules.filter((r) => r.startsWith("Disallow:")).map((r) => r.slice("Disallow:".length).trim());
    for (const path of ["/", "/login", "/imprint", "/privacy", "/terms", "/og-image.jpg", "/shots/session-live.webp"]) {
      expect(disallowed.some((prefix) => path.startsWith(prefix)), `${path} must stay crawlable`).toBe(false);
    }
  });

  it("names the prefix the browser really dials for RPCs", () => {
    // If the transport's base path ever moves, this file has to move with it.
    expect(read("src/lib/transport.ts")).toMatch(/baseUrl:\s*"\/api"/);
  });
});
