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
