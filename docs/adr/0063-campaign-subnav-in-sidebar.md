# Campaign sub-views move into the sidebar, addressed by path

The Campaign screen had grown six in-page tabs (Cast, World wiki, Maps, Players, Suggestions, Planning) in a seg-control beside the title — cramped on desktop, overflowing on phones, and invisible from anywhere else in the app. This ADR moves that sub-navigation into the app shell's left sidebar and promotes the sub-view from screen state to a path segment.

## What this decides

- **The sidebar owns sub-navigation.** The shell's Campaign nav item expands,
  accordion-style, into one sub-item per sub-view while Campaign is the active
  screen — the same items the in-page tab bar carried, same labels
  (`campaign.tab*`), now reachable from the persistent chrome. The in-page tab
  bar is removed, not duplicated: one navigation surface, one active state. On
  the drawer breakpoint the sub-items live in the off-canvas drawer, and any
  nav tap now also shuts the drawer so it doesn't keep covering the screen it
  navigated to.
- **The sub-view is a path segment:** `/t/:tenantSlug/campaign/:view`
  (`cast | knowledge | maps | players | proposals | planning`, the shared
  `CAMPAIGN_VIEWS` vocabulary in `screens/campaign/views.ts`). ADR-0018's
  code-based tree gains a generic `$screen/$view` sibling of `$screen`; only
  Campaign defines sub-views today, so any other screen with a second segment
  renders the notFound placeholder. The sidebar links to and highlights the
  `:view` param, and a bookmarked URL restores the view — which screen-local
  state never could.
- **Sub-view changes are param-only navigations** within the one
  `$screen/$view` route, so the screen component stays mounted and its local
  state (selected agent, cast mode, wiki focus) survives a sidebar switch —
  the same behaviour the in-page tabs had.
- **Legacy links keep working.** A bare `/campaign` redirects to
  `/campaign/cast`; the pre-ADR-0063 palette vocabulary (#591) `?view=X` is
  rewritten onto the path, and `?node=Y` lands on `/campaign/knowledge?node=Y`
  (the redirect owns view selection; the screen keeps consume-then-strip for
  the node focus). The palette itself now deep-links entry hits to the path
  directly. `ScreenSearch.view` stays in the search vocabulary solely to parse
  those legacy links.

## Consequences

- The Campaign screen no longer owns view state: it receives `view` and an
  `onViewChange` callback from the route, and its cross-view jumps (prep marks
  opening the wiki, wiki entries opening the cast editor) go through the same
  callback. Tests drive views through a harness standing in for the route +
  sidebar pair.
- Session and Setup have no sub-views; if either grows tabs, the same
  `$screen/$view` route and sidebar pattern is the expected shape, not a new
  in-page tab bar.
