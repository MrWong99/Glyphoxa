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

## Amendment: the primitive layer, the feedback rule, and what was never built (2026-11-22)

The decision above named the Radix primitives, but only three were ever installed (alert-dialog, select, switch). Menus, popovers and the palette modal were hand-rolled per screen, every query's loading state was the same 10-character shimmer bar, empty states were a grey sentence in a dozen bespoke classes, and a failed load was red text with no way to retry. This amendment records the layer that closes that gap, the rule for which kind of feedback goes where, and three things the original text promised that do not exist.

**The primitives, all in `web/src/components/ui/`.** Each is a thin wrapper that emits `gx-` classes styled in `styles/components.css`; none carries styles of its own.

| Kind | Primitive | Backed by |
| --- | --- | --- |
| Overlay | `Dialog` (variants `modal` and `sheet`), `DialogClose` | `@radix-ui/react-dialog` |
| Overlay | `ConfirmDialog` | `@radix-ui/react-alert-dialog` |
| Overlay | `Popover`, `PopoverClose` | `@radix-ui/react-popover` |
| Overlay | `Menu`, `MenuItem`, `MenuLabel`, `MenuSeparator` | `@radix-ui/react-dropdown-menu` |
| Overlay | `Tooltip`, `TooltipProvider` | `@radix-ui/react-tooltip` |
| State | `Skeleton` (lines, `Skeleton.Row`, `Skeleton.Block`), `Spinner` | hand-rolled |
| State | `QueryBoundary`, `EmptyState`, `ErrorState`, `Alert` | hand-rolled |
| Layout | `Page`, `PageHeader`, `Card`, `AdvancedCard` | hand-rolled |
| Form | `Button` (with `loading`, `iconOnly`), `IconButton`, `Input`, `Textarea`, `Field`, `ChipInput`, `SegmentedControl` | hand-rolled |
| Form | `Select`, `Switch` | `@radix-ui/react-select`, `@radix-ui/react-switch` |
| Form | `Combobox` | `cmdk` |
| Feedback | `toastUndo` (`ui/toast.ts`) | `sonner` |
| Display | `Badge`, `Avatar`, `Markdown` | hand-rolled |

Three notes on that table:

- **`SegmentedControl` is hand-rolled, not a Radix ToggleGroup.** The screens' existing view switches are a `role="group"` of buttons with `aria-pressed`, and their tests query exactly that. A ToggleGroup renders radio semantics with roving focus; adopting it would have changed what a screen reader hears and broken those tests for no gain. Radix Tabs, ToggleGroup and Checkbox, named in the original decision, are still not installed: nothing uses them, and the sidebar sub-navigation (ADR-0063) replaced the in-page tabs.
- **The sheet variant of `Dialog` is a right-edge drawer, and a bottom sheet at 880px and below.** It shares one surface with the modal variant and `ConfirmDialog`.
- **A Radix modal driven controlled has no Trigger to return focus to**, so closing one left focus on `<body>`. `Dialog` and `ConfirmDialog` return focus to whatever had it when they opened (`ui/useRestoreFocus.ts`), including the trigger of a `Menu` whose item opened the dialog.

**The feedback rule.** Four situations, four answers, so the same event looks the same on every screen:

| Situation | Answer |
| --- | --- |
| A read failed and there is nothing to show | `ErrorState` in place of the content, with **Try again** (usually through `QueryBoundary`) |
| A mutation failed | `toast.error` with the reason |
| A save succeeded inside an editor that stays open | an inline status beside the Save button ("Saved", "Unsaved changes"), no toast |
| A success that removed something or navigated away | `toast.success`, or `toastUndo` when it can be taken back |

A failed read that still has data on screen (a background refetch) is not an error state: the data stays and the next refetch can succeed. A state that persists, such as a missing setup step, is an `Alert`, not a toast. A toast reports an event that has passed.

**Token changes.** `tokens.css` is no longer a verbatim copy of the 2026-06-24 handoff, and the next handoff must take these back rather than overwrite them:

- `--text-subtle` is light-gray-80 (was marketing-gray-60, 3.6 to 4.4:1 on the app's surfaces; now 4.8:1 or better).
- `--arcane-fill` and `--arcane-fill-hover` (violet-60 and violet-70) fill anything that carries white text. White on `--arcane` is 4.15:1.
- `--border-control` and `--border-control-hover` edge inputs, selects and the switch track at 3:1 or better against the card and overlay surfaces. They are solid colours, computed as violet-20 over `--surface-card`, because an alpha border has a different contrast on every surface.
- A z-index ladder (`--z-sticky` 20, `--z-scrim` 55, `--z-drawer` 60, `--z-overlay` 90, `--z-modal` 91, `--z-popover` 100, `--z-tooltip` 110). Floating surfaces sit above the modal pair so a dropdown inside a dialog opens over it.
- `--page-gutter` (32px, 24px at 880 and below, 16px at 640 and below), the page widths, the control sizes and the focus-outline tokens.

`styles/tokens.test.ts` recomputes the WCAG ratios from the hex values, so a later palette change that drops below AA fails a test instead of shipping.

**Three breakpoints.** 1100px (two-column editors stack), 880px (the sidebar becomes a drawer, sheets dock to the bottom), 640px (phone gutters). They are documented in `tokens.css` and exported from `web/src/lib/breakpoints.ts`; a custom property cannot be used in a media query, so stylesheets repeat the numbers. Two leftovers remain: the topbar's phone squeeze in `components.css` keeps 480, and the screens' co-located CSS still uses 520, 720 and 900 in places. They move to the three as each is next touched.

**Focus, touch and forced colours.** Keyboard focus is a 2px outline on `:focus-visible`, not a box-shadow: a selected row's own box-shadow used to replace the ring, so focus disappeared on the item it was on. One `(pointer: coarse)` block gives small controls a 40px target. One `(forced-colors: active)` block restates, with outlines and system colours, what the theme otherwise says with fills and shadows.

**Not implemented: `data-accent`, `data-density`, `data-reduce-motion`.** The original text says density, accent and reduced motion are root attributes whose selectors flip token values, and that `tokens.css` holds `[data-accent]` overrides. None of the three exists. There is one accent and one density. Reduced motion is honoured, but through the `prefers-reduced-motion` media query in `base.css`, which follows the operating system setting and needs no attribute. The attributes stay unbuilt until a user-facing setting needs them (tracked with the rest of the web backlog in #695); treat the paragraph above as a design option that was not taken, not as a description of the code.
