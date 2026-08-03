# Radix + plain CSS tokens; class vocabulary anchored to Claude Design

Roll trivial primitives by hand (button, card, input, tag, telemetry strip, sidebar nav, NPC sigil, latency bars, waveform, live-dot — all already styled in the prototype). Use Radix Primitives for the hard accessibility-heavy components (Dialog, Popover, DropdownMenu, Select, Tabs, Tooltip, ToggleGroup, Switch, Checkbox); `cmdk` for the ⌘K palette; `sonner` for toasts; `lucide-react` for icons (port the prototype's bespoke brand/NPC sigil components by hand).

CSS authoring is plain CSS files:

- `web/src/styles/tokens.css` — `:root` tokens + `[data-accent]` overrides
- `web/src/styles/base.css` — resets, scrollbars, body type
- `web/src/styles/components.css` — `.btn`, `.card`, `.input`, `.tag`, `.field`, `.telemetry`, `.live-dot`, etc.
- Co-located CSS per screen.

No Tailwind, no CSS-in-JS, no theme-context plumbing. Density / accent / reduce-motion are root attributes (`data-accent`, `data-density`, `data-reduce-motion`); CSS selectors flip token values.

**Claude Design integration constraints:**

- Tokens MUST stay in plain CSS files so Claude Design can read them on the next iteration. Do not move into TS / CSS-in-JS / theme objects.
- The class-name vocabulary in React components mirrors the prototype's so future handoff bundles drop in with minimal porting friction.
- Each handoff bundle is **committed** at `web/design-handoff/<YYYY-MM-DD>/` (not gitignored). Diffing successive bundles drives iteration planning.
- Claude Design does not import component libraries — each bundle re-rolls dialog/popover/select inline. We port those onto our Radix-backed wrappers at handoff time. One-time porting cost per design iteration; minimised by keeping class-name vocabulary stable.
- Link `web/` (not the whole monorepo) into Claude Design to avoid lag.

**Considered options:**

- **shadcn/ui** — rejected. Forces re-doing the prototype's tokens against Tailwind.
- **Base UI** — rejected. Smaller 2026 community than Radix.
- **Roll everything** — rejected. Combobox / dialog accessibility eats sprints.

Forms use plain `useState` for v1.0; reach for `react-hook-form + zod` only when a form genuinely needs it (multi-step wizards, shared validation).

## Amendment: `d3-force` as a layout dependency (2026-08-03, #534)

The Knowledge Graph view (ADR-0008 fourth amendment) adds **`d3-force`** — the first frontend dependency that is neither Radix, `cmdk`/`sonner`, nor an icon set. Small (~30 KB, no transitive React), but it is a posture change and deserves the paragraph.

**Why a dependency at all.** Force-directed layout is the one part of the graph view that is genuinely hard. A hand-rolled simulation is ~150 lines and tempting, but collision resolution and link-distance settling are exactly what make a 300-node graph legible rather than a hairball — and getting them wrong is not a bug you notice in review, it is a picture nobody can read.

**What it does NOT change.** `d3-force` computes numbers; it renders nothing. The graph is SVG authored by our own components against the existing token/class vocabulary, so the constraints above hold in full: no Tailwind, no CSS-in-JS, tokens stay in plain CSS, and the 7-type palette is the same one the list view uses (now shared, in `knowledgeVocab.ts`). We deliberately did not take `d3-selection`, `d3-zoom`, or any renderer — pan and zoom are our own arithmetic on the SVG `viewBox`, ~15 lines.

**The bar for the next one.** A frontend dependency is justified when it solves a hard, well-specified, *non-visual* problem and stays out of the DOM. Anything that renders, themes, or owns layout markup remains ours.
