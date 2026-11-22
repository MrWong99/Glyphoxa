// @vitest-environment node
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it, vi } from "vitest";

import { atMost, BREAKPOINTS, isAtMost, watchBreakpoint } from "./breakpoints";

const styles = fileURLToPath(new URL("../styles/", import.meta.url));
const read = (name: string) => readFileSync(`${styles}${name}`, "utf8");

afterEach(() => vi.unstubAllGlobals());

describe("breakpoints", () => {
  it("names the three layout breakpoints", () => {
    expect(BREAKPOINTS).toEqual({ stack: 1100, drawer: 880, phone: 640 });
    expect(atMost("drawer")).toBe("(max-width: 880px)");
  });

  // A custom property cannot be used in a media query, so the stylesheets
  // repeat the numbers. This keeps the shared ones on the three above.
  it("the shared stylesheets use no other width breakpoint", () => {
    const allowed = new Set<number>(Object.values(BREAKPOINTS));
    // The one exception: the topbar's phone squeeze, which belongs to the
    // shell's layout and predates the scale.
    const legacy = new Set([480]);
    for (const file of ["tokens.css", "base.css", "components.css"]) {
      const widths = [...read(file).matchAll(/@media[^{]*\((?:max|min)-width:\s*(\d+)px\)/g)].map((m) =>
        Number(m[1]),
      );
      for (const width of widths) {
        expect(allowed.has(width) || legacy.has(width), `${file} uses ${width}px`).toBe(true);
      }
    }
  });

  it("steps the page gutter 32 / 24 / 16 at the drawer and phone breakpoints", () => {
    const tokens = read("tokens.css");
    const base = /^\s*--page-gutter:\s*(\d+)px;/m.exec(tokens)?.[1];
    const at = (width: number) =>
      new RegExp(`@media \\(max-width: ${width}px\\) \\{\\s*:root \\{\\s*--page-gutter:\\s*(\\d+)px;`).exec(
        tokens,
      )?.[1];
    expect([base, at(BREAKPOINTS.drawer), at(BREAKPOINTS.phone)]).toEqual(["32", "24", "16"]);
  });

  it("isAtMost reads matchMedia, and answers desktop where there is none", () => {
    // No window (server render) or no matchMedia (jsdom): the desktop layout.
    expect(isAtMost("phone")).toBe(false);
    vi.stubGlobal("window", {});
    expect(isAtMost("phone")).toBe(false);

    const matchMedia = vi.fn((query: string) => ({ matches: query === "(max-width: 880px)" }));
    vi.stubGlobal("window", { matchMedia });
    expect(isAtMost("drawer")).toBe(true);
    expect(isAtMost("phone")).toBe(false);
    expect(matchMedia).toHaveBeenCalledWith("(max-width: 640px)");
  });

  it("watchBreakpoint reports crossings until it is stopped", () => {
    // Without matchMedia it is a harmless no-op.
    expect(() => watchBreakpoint("drawer", () => {})()).not.toThrow();

    let listener: ((event: { matches: boolean }) => void) | null = null;
    const query = {
      matches: false,
      addEventListener: vi.fn((_type: string, l: (event: { matches: boolean }) => void) => {
        listener = l;
      }),
      removeEventListener: vi.fn(),
    };
    vi.stubGlobal("window", { matchMedia: vi.fn(() => query) });

    const onChange = vi.fn();
    const stop = watchBreakpoint("drawer", onChange);
    expect(query.addEventListener).toHaveBeenCalledWith("change", expect.any(Function));
    listener!({ matches: true });
    listener!({ matches: false });
    expect(onChange.mock.calls).toEqual([[true], [false]]);

    stop();
    expect(query.removeEventListener).toHaveBeenCalledWith("change", listener);
  });
});
