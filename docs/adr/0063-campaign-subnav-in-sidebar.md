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

## Amendment: the campaign comes first (2026-11-22)

The decision above put the campaign's views in the sidebar, but only while the Campaign screen was open — from Session or Setup the GM saw a single "Campaign" item, clicked it, landed on Cast, and only then could reach the wiki. The app also still opened on Setup: a returning GM's first screen was API keys. This amendment finishes the move. Where it contradicts the text above, it supersedes it.

### What changes

- **The views are always visible.** The sidebar has a section labelled with the **Active Campaign**'s name that lists the campaign's views on *every* screen, then Session, then — pinned under the scrolling list — the two Tenant-wide destinations, Campaigns and Setup. The accordion is gone, and with it the parent "Campaign" item that linked to "the current view": every view is a direct link, so the wiki is one click from anywhere. The lists live in one module (`app/destinations.ts`) that the sidebar, the topbar breadcrumb, the browser-tab title, the Overview and the in-shell not-found page all read, so a destination's label cannot drift between them.
- **Two views are added.** `overview` is where a campaign opens; `settings` is the campaign record itself (name, System, **Campaign Language**, highlight recording) as a page with an address, replacing the topbar switcher's popover as the only place to edit it. The vocabulary is now `overview | cast | knowledge | maps | players | proposals | planning | settings`. "Settings" (the campaign's) and "Setup" (the Tenant's) are deliberately different words.
- **The app lands on the campaign.** The tenant index, a bare `/campaign` and an unknown `:view` all redirect (replace) to `/campaign/overview`; `/` sends a signed-in visit to the tenant index rather than naming a screen itself. A Tenant with no campaign lands there too — the Campaign screen answers `CodeNotFound` with the create-first-campaign card on every view. Only a *fresh signup* still lands on Setup, because the onboarding step navigates there explicitly.
- **A Campaigns screen** exists at `/t/:tenantSlug/campaigns`, a sibling of Session and Setup. The topbar switcher stays, as the quick way to change campaign.
- **The topbar says where you are**: `<Campaign> / <View>` on the campaign's views and on Session (a **Voice Session** is one campaign's), a plain title on the Tenant-wide screens; the browser tab reads `<View> · <Campaign> · Glyphoxa`.

### The screen is a host; views are panels

"The Campaign screen no longer owns view state" (above) is taken one step further: it no longer owns the views either. `Campaign.tsx` resolves which campaign is active, remounts everything under it when that changes, renders the header and switches on the view. Each view is one panel file. What panels share comes from `useCampaignScreen()` — the campaign, its roster, and the cross-view jumps `openNode(id)`, `openAgent(id)`, `openView(view)` — instead of a prop threaded through the host for each new thing a panel wants to open. The screen root carries `data-view`, so a view's stylesheet can change the frame (a full-height view, say) without the host knowing.

A panel unmounts on a view switch, which the in-page tabs never did. Two things keep that from costing the GM their place: UI state a panel wants back (the selected NPC, the mode within Cast) is held in the host through `useCampaignPanelState`, and is dropped with the host on an Active Campaign switch; and selection that deserves an address goes in the URL (below).

### The navigation seam: `useScreenNav()`

The route used to hand the Campaign screen its navigation as callbacks (`onViewChange`, `onDeepLinkHandled`, `onOpenTranscriptLine`), one per thing a screen might do. That does not scale to a shell and a dozen panels that all navigate, and it made "which view" a prop of a prop. Instead the tenant layout provides one seam, above the shell:

- `search` — the validated, typed search params: `node`, `agent`, `map`, `thread`, `session`, `line`, `highlight`, every one an id;
- `setSearch(patch, { replace })` — change params on the current screen, keeping the keys the patch does not name;
- `goScreen(screen, search?)` and `goCampaignView(view, search?)`.

Screens and shell components import this, never the router. Outside a provider the hook is inert, so a screen under test needs no router and no router mock; a test that wants to observe navigation mounts an in-memory provider that applies the same rules. The URL shape becomes one file's business: the Active Campaign is ambient today (ADR-0039), and if the campaign id ever moves onto the path (#692), no screen changes.

A cross-view jump is therefore a *navigation*, not screen state: opening an entry from a map pin is `goCampaignView("knowledge", { node })`, and Back returns to the map. The wiki's focus is the route's `?node` itself — the host keeps no copy of it.

### Selection in the URL: one-shot and sticky keys

ADR-0063 inherited "consume then strip" from the palette's deep links (#591): a screen applies the param once and removes it, so a copied URL acts once. That is right for a jump and wrong for a selection — a selection that is stripped cannot be bookmarked, restored on reload, or stepped through with Back. Both lifetimes now exist, and the vocabulary names them:

- **One-shot** keys are a deep link: applied, then stripped with `replace` so no history entry is left. `line` and `highlight` are one-shot by nature. `agent` is one-shot (it opens Cast on that NPC, once). `node` is one-shot *today*.
- **Sticky** keys are the screen's selection for as long as it is selected: set with a push, so Back steps through selections and a reload restores them. `map`, `thread` and `session` are reserved for this, and `node` becomes sticky when the wiki adopts it.

The route does not know which is which and does not strip anything: **the screen that owns a key decides when it has been honoured.** (The wiki strips `?node` only once the entry's row has loaded, so a deep link survives a slow or retried list read.) The route's one job is placement: `?node` belongs on the wiki and `?agent` on Cast, and a URL pairing either with another view is redirected to the view it belongs on.

Two consequences follow from sticky selection, and both are handled where the state lives:

- **A query change can now replace what an editor is editing.** The unsaved-edits guard only looked at the path, because a query change never unmounted anything. A screen that keeps its selection in the URL declares those keys (`useGuardedSearchKeys`) while it is mounted; the route guard then also holds a navigation that changes a declared key while edits are unsaved. Undeclared keys — the one-shot strips — still pass.
- **A selection names a row of one campaign.** When the Active Campaign changes, the host drops `node`, `agent`, `map` and `thread` from the URL along with the rest of the per-campaign state.

### Live and attention signals belong to the shell

Whether a **Voice Session** is live, and how many **Knowledge Proposals** are waiting, were visible only on the screens that own them. The shell now reads both on every screen — a pulsing marker with the running time on the Session item, a "Live · Open session" link in the topbar when the GM is elsewhere, and a count on Suggestions — from the *same cache entries* those screens use, so the shell cannot disagree with the screen. This also repairs a promise ADR-0039's live-first rule left dangling: a campaign switch made during a live session "takes effect when it ends", but the cache sweep that makes it so is triggered by the session read flipping to inactive, and that read was only polled while the Session screen was open. With the shell polling it, the switch comes due on any screen.

### Switching campaign is guarded

Because the Active Campaign is ambient, a switch silently retargets every write on screen, and the Campaign screen remounts on the new id — discarding whatever was typed and not saved, without a word. All hand-made switches now go through one hook (`useCampaignSwitch`): it asks first while any editor reports unsaved edits, writes the selection, runs the sweep, and says what happened — "Now working in {name}", or that a live session defers it. It also records, per tab, which campaign *this* tab asked for, so a later observer can tell a change made elsewhere (another tab, Discord) from its own.

### Not decided here

- The campaign id in the URL and on requests (#692). This amendment makes it cheaper — the seam means screens will not change — but the Active Campaign stays ambient server state.
- Which keys the Session screen keeps sticky, and when the wiki's `node` flips from one-shot to sticky: each is the owning screen's decision, made when it adopts the seam.
