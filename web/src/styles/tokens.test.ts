// @vitest-environment node
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// Contrast is checked from the token VALUES, because nothing else can check
// it: jsdom runs no cascade, and a colour that drifts below AA still renders.
// The numbers are WCAG 2.x contrast ratios computed from the hex values in
// tokens.css, resolving var() references the way the browser would.
const styles = fileURLToPath(new URL(".", import.meta.url));
const tokensCss = readFileSync(`${styles}tokens.css`, "utf8");
const componentsCss = readFileSync(`${styles}components.css`, "utf8");
const baseCss = readFileSync(`${styles}base.css`, "utf8");

// The first :root block holds every colour; the breakpoint blocks after it
// only restate --page-gutter.
const declarations = new Map<string, string>();
for (const [, name, value] of tokensCss.matchAll(/^\s*(--[a-z0-9-]+):\s*([^;]+);/gm)) {
  if (!declarations.has(name!)) declarations.set(name!, value!.trim());
}

function resolve(name: string, seen: string[] = []): string {
  const value = declarations.get(name);
  if (value === undefined) throw new Error(`token ${name} is not defined in tokens.css`);
  const reference = /^var\((--[a-z0-9-]+)\)$/.exec(value);
  if (!reference) return value;
  if (seen.includes(name)) throw new Error(`token cycle: ${[...seen, name].join(" -> ")}`);
  return resolve(reference[1]!, [...seen, name]);
}

function hex(name: string): string {
  const value = resolve(name);
  if (!/^#[0-9a-f]{6}$/i.test(value)) {
    throw new Error(`token ${name} resolves to "${value}", not a solid #rrggbb colour`);
  }
  return value;
}

function luminance(colour: string): number {
  const channel = (offset: number) => {
    const c = parseInt(colour.slice(offset, offset + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(1) + 0.7152 * channel(3) + 0.0722 * channel(5);
}

function contrast(a: string, b: string): number {
  const [light, dark] = [luminance(a), luminance(b)].sort((x, y) => y - x) as [number, number];
  return (light + 0.05) / (dark + 0.05);
}

const on = (foreground: string, background: string) => contrast(hex(foreground), hex(background));

describe("contrast math", () => {
  it("matches the WCAG reference points", () => {
    expect(contrast("#ffffff", "#000000")).toBeCloseTo(21, 5);
    expect(contrast("#777777", "#ffffff")).toBeCloseTo(4.48, 2);
    expect(contrast("#123456", "#123456")).toBe(1);
  });
});

describe("token contrast (WCAG AA)", () => {
  // Small hint and placeholder text: 4.5:1.
  it.each(["--surface-card", "--surface-overlay", "--surface-inset", "--surface-base", "--surface-raised"])(
    "--text-subtle is at least 4.5:1 on %s",
    (surface) => {
      expect(on("--text-subtle", surface)).toBeGreaterThanOrEqual(4.5);
    },
  );

  it("the muted and default text colours clear 4.5:1 on the card and overlay surfaces", () => {
    for (const text of ["--text-muted", "--text-default", "--text-strong"]) {
      for (const surface of ["--surface-card", "--surface-overlay"]) {
        expect(on(text, surface)).toBeGreaterThanOrEqual(4.5);
      }
    }
  });

  // The primary button label.
  it("white is at least 4.5:1 on --arcane-fill and on its hover", () => {
    expect(on("--text-on-arcane", "--arcane-fill")).toBeGreaterThanOrEqual(4.5);
    expect(on("--text-on-arcane", "--arcane-fill-hover")).toBeGreaterThanOrEqual(4.5);
  });

  // The reason --arcane-fill exists: the brand violet itself does not carry
  // white text. If this ever passes, the fill tokens can fold back into it.
  it("white on --arcane itself is below 4.5:1", () => {
    expect(on("--text-on-arcane", "--arcane")).toBeLessThan(4.5);
  });

  // Non-text contrast (WCAG 1.4.11): the edge of an input, select or switch.
  it("--border-control is at least 3:1 on the surfaces a control sits on", () => {
    expect(on("--border-control", "--surface-card")).toBeGreaterThanOrEqual(3);
    expect(on("--border-control", "--surface-overlay")).toBeGreaterThanOrEqual(3);
    // … and against the control's own fill, so the edge is an edge from both sides.
    expect(on("--border-control", "--surface-inset")).toBeGreaterThanOrEqual(3);
  });

  it("--border-control-hover is stronger than --border-control", () => {
    expect(on("--border-control-hover", "--surface-card")).toBeGreaterThan(
      on("--border-control", "--surface-card"),
    );
  });

  // The keyboard focus outline.
  it("the focus outline colour is at least 3:1 on every surface", () => {
    for (const surface of ["--surface-base", "--surface-raised", "--surface-card", "--surface-card-hover", "--surface-overlay"]) {
      expect(on("--focus-outline-color", surface)).toBeGreaterThanOrEqual(3);
    }
  });

  // Status colours used as text (alerts, the danger menu item, toasts).
  it("status text colours clear 4.5:1 on the overlay surface", () => {
    for (const status of ["--status-success", "--status-warning", "--status-danger", "--rune", "--color-red-20"]) {
      expect(on(status, "--surface-overlay")).toBeGreaterThanOrEqual(4.5);
    }
  });
});

describe("tokens in use", () => {
  it("the primary button and the solid badge are filled with --arcane-fill", () => {
    const rule = (selector: string) => {
      const match = new RegExp(`\\n${selector.replace(/[.-]/g, "\\$&")} \\{([^}]*)\\}`).exec(componentsCss);
      if (!match) throw new Error(`no rule for ${selector}`);
      return match[1]!;
    };
    expect(rule(".gx-btn--primary")).toMatch(/background:\s*var\(--arcane-fill\)/);
    expect(rule(".gx-btn--primary:hover")).toMatch(/background:\s*var\(--arcane-fill-hover\)/);
    expect(rule(".gx-badge--solid")).toMatch(/background:\s*var\(--arcane-fill\)/);
  });

  it("inputs, selects and the switch track are edged with --border-control", () => {
    for (const selector of [".gx-input", ".gx-select", ".gx-switch__track", ".gx-chipinput"]) {
      const match = new RegExp(`\\n${selector.replace(/[.-]/g, "\\$&")} \\{([^}]*)\\}`).exec(componentsCss);
      expect(match?.[1], selector).toMatch(/border:\s*1px solid var\(--border-control\)/);
    }
  });

  it("keyboard focus is an outline, never a box-shadow or a forced radius", () => {
    const focus = /\n:focus-visible \{([^}]*)\}/.exec(baseCss)?.[1] ?? "";
    expect(focus).toMatch(/outline:\s*var\(--focus-outline-width\) solid var\(--focus-outline-color\)/);
    expect(focus).not.toMatch(/box-shadow|border-radius|outline:\s*none/);
    // No rule in the shared stylesheets swaps the outline for the old ring.
    expect(componentsCss).not.toMatch(/var\(--focus-ring\)/);
    expect(baseCss).not.toMatch(/var\(--focus-ring\)/);
  });

  // base.css says every focusable element gets the outline. A later rule that
  // says `outline: none` at the same specificity silently takes it away again
  // (it did, for the campaign switcher's rows and the row-actions menu's
  // items, through .gx-select__item). So every such reset has to be listed
  // here with the thing that shows focus INSTEAD, and that thing has to exist.
  describe("every `outline: none` names what shows focus instead", () => {
    // selector of the reset -> selector of the rule that draws the indicator,
    // or a reason there is nothing to indicate.
    const SURFACE = "a surface that takes focus only as a container; focus inside it is drawn as usual";
    const OWN_SHAPE = "draws its own mark on its shape (the screen's stylesheet)";
    const resets: Record<string, string> = {
      // The rule itself swaps the outline for a border colour and a glow.
      ".gx-input:focus": ".gx-input:focus",
      '.gx-select:focus, .gx-select[data-state="open"]': '.gx-select:focus, .gx-select[data-state="open"]',
      // The highlighted row is the keyboard position in a listbox or a menu.
      '.gx-select__item[role="option"]': ".gx-select__item[data-highlighted]",
      ".gx-menu__item": ".gx-menu__item[data-highlighted]",
      // A borderless field inside a frame: the frame shows it.
      ".gx-combobox__input": ".gx-combobox__search:focus-within",
      ".gx-chipinput__input": ".gx-chipinput:focus-within",
      ".gx-dialog:focus-visible": SURFACE,
      ".gx-popover:focus-visible, .gx-menu:focus-visible": SURFACE,
      "svg g:focus-visible": OWN_SHAPE,
    };

    // Every `selector { declarations }` block, comments stripped. Blocks nest
    // one level deep at most (@media), and the inner blocks are what match.
    const rules = (css: string) =>
      [...css.replace(/\/\*[\s\S]*?\*\//g, "").matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({
        selector: m[1]!.trim().replace(/\s+/g, " "),
        body: m[2]!,
      }));
    const all = [...rules(baseCss), ...rules(componentsCss)];
    const INDICATOR = /(^|[\s;])(outline|box-shadow|border-color|background)\s*:\s*(?!none)/;

    it("no reset is unlisted", () => {
      const found = all.filter((rule) => /outline:\s*(none|0)\s*;/.test(rule.body)).map((rule) => rule.selector);
      expect(found.length).toBeGreaterThan(0);
      for (const selector of found) {
        expect(Object.keys(resets), `"${selector}" resets the focus outline`).toContain(selector);
      }
    });

    it.each(Object.entries(resets).filter(([, instead]) => instead !== SURFACE && instead !== OWN_SHAPE))(
      "%s -> %s",
      (_reset, instead) => {
        const indicator = all.filter((rule) => rule.selector === instead);
        expect(indicator.length, `no rule for ${instead}`).toBeGreaterThan(0);
        expect(indicator.some((rule) => INDICATOR.test(rule.body.replace(/outline:\s*none\s*;/, "")))).toBe(true);
      },
    );

    // The regression itself: the row class shared by listbox options and real
    // buttons must not reset the outline for the buttons.
    it(".gx-select__item itself keeps the outline (only its role=option form drops it)", () => {
      const row = all.filter((rule) => rule.selector === ".gx-select__item");
      expect(row.length).toBeGreaterThan(0);
      for (const rule of row) expect(rule.body).not.toMatch(/outline\s*:/);
    });
  });

  it("layers come from the z-index tokens, not from literals", () => {
    for (const css of [componentsCss, baseCss]) {
      const literals = [...css.matchAll(/z-index:\s*([^;]+);/g)].map((m) => m[1]!.trim());
      for (const value of literals) expect(value).toMatch(/^var\(--z-[a-z]+\)$/);
    }
    // Floating surfaces sit above the modal pair, so a dropdown inside a
    // dialog opens over it; the drawer and its scrim sit below.
    const z = (name: string) => Number(resolve(name));
    expect(z("--z-scrim")).toBeLessThan(z("--z-drawer"));
    expect(z("--z-drawer")).toBeLessThan(z("--z-dropdown"));
    expect(z("--z-dropdown")).toBeLessThan(z("--z-overlay"));
    expect(z("--z-overlay")).toBeLessThan(z("--z-modal"));
    expect(z("--z-modal")).toBeLessThan(z("--z-popover"));
    expect(z("--z-popover")).toBeLessThan(z("--z-tooltip"));
  });

  // .gx-select__content is two things: the portalled Radix Select list, which
  // has to open above a dialog it is used in, and the surface of the panels
  // that open in place on the page (Combobox list, campaign switcher), which
  // have to stay under a dialog opened while they are up.
  it("only the portalled select list sits above the modal pair", () => {
    const zOf = (selector: string) => {
      const match = new RegExp(`\\n${selector.replace(/[.\-[\]"=]/g, "\\$&")} \\{([^}]*)\\}`).exec(componentsCss);
      if (!match) throw new Error(`no rule for ${selector}`);
      return /z-index:\s*var\((--z-[a-z]+)\)/.exec(match[1]!.replace(/\/\*[\s\S]*?\*\//g, ""))?.[1];
    };
    expect(zOf(".gx-select__content")).toBe("--z-dropdown");
    expect(zOf('.gx-select__content[role="listbox"]')).toBe("--z-popover");
  });

  it("the shared stylesheets carry no var() fallbacks (every token is defined)", () => {
    for (const css of [tokensCss, baseCss, componentsCss]) {
      // A token must not need a fallback. (A component-scoped --gx-* property
      // may carry its default inline; that is its definition.)
      expect(css).not.toMatch(/var\(--(?!gx-)[a-z0-9-]+\s*,/);
      for (const [, name] of css.matchAll(/var\((--[a-z0-9-]+)\)/g)) {
        // Component-scoped properties (--gx-*) and the ones Radix and sonner
        // set at runtime are defined where they are used.
        if (/^--(gx|radix|normal|success|info|warning|error|border-radius)/.test(name!)) continue;
        expect(declarations.has(name!), `${name} is used but not defined in tokens.css`).toBe(true);
      }
    }
  });
});
