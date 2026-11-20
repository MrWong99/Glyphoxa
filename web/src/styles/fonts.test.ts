// @vitest-environment node
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// Fonts are self-hosted (W4): no page may ask a third-party origin for a font,
// because that request hands the visitor's IP address to the font host. These
// read the source files a build is made of; the build output is additionally
// grepped by hand when the font set changes.
const web = fileURLToPath(new URL("../..", import.meta.url));
const indexHtml = readFileSync(`${web}/index.html`, "utf8");
const fontsCss = readFileSync(`${web}/src/styles/fonts.css`, "utf8");
const localUrls = [...fontsCss.matchAll(/url\(\s*['"]?([^'")]+)['"]?\s*\)/g)].map((m) => m[1]!);

describe("self-hosted fonts", () => {
  it("references no third-party font host from index.html or the font CSS", () => {
    for (const source of [indexHtml, fontsCss]) {
      expect(source).not.toMatch(/fonts\.googleapis\.com|fonts\.gstatic\.com|https?:\/\//);
    }
  });

  it("loads every font file from this origin, and every file exists", () => {
    expect(localUrls.length).toBeGreaterThan(0);
    for (const url of localUrls) {
      expect(url).toMatch(/^\/fonts\/[a-z0-9-]+\.woff2$/);
      expect(existsSync(`${web}/public${url}`)).toBe(true);
    }
  });

  it("declares font-display: swap on every face", () => {
    const faces = fontsCss.match(/@font-face\s*\{[^}]*\}/g) ?? [];
    expect(faces.length).toBe(localUrls.length);
    for (const face of faces) expect(face).toMatch(/font-display:\s*swap/);
  });

  it("preloads only font files the CSS declares", () => {
    const preloads = [...indexHtml.matchAll(/<link[^>]*rel="preload"[^>]*>/g)].map((m) => m[0]);
    expect(preloads.length).toBeGreaterThan(0);
    for (const tag of preloads) {
      const href = /href="([^"]+)"/.exec(tag)?.[1];
      expect(localUrls).toContain(href);
      // Font preloads are fetched in CORS mode; without `crossorigin` the
      // browser downloads the file twice.
      expect(tag).toMatch(/crossorigin/);
    }
  });

  it("ships the OFL licence text for each family", () => {
    for (const name of ["Inter-OFL.txt", "JetBrainsMono-OFL.txt", "ZillaSlab-OFL.txt"]) {
      expect(readFileSync(`${web}/public/fonts/${name}`, "utf8")).toMatch(/SIL Open Font License/);
    }
  });
});
