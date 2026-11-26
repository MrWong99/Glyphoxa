import { useEffect, useId, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { Link, Outlet, useParams } from "@tanstack/react-router";
import { useQuery } from "@connectrpc/connect-query";
import { Toaster } from "sonner";
import { Dices, PanelLeft, Search } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";

import { Button } from "./ui/Button";
import { Tooltip, TooltipProvider } from "./ui/Tooltip";
import { SidebarUser } from "./SidebarUser";
import { LegalFooter } from "./LegalFooter";
import { CampaignSwitcher } from "./CampaignSwitcher";
import { CommandPalette, PaletteShortcut, isApplePlatform, paletteShortcut } from "./CommandPalette";
import { ConnectionBanner } from "./ConnectionBanner";
import type { CurrentUser } from "@/app/currentUser";
import {
  CAMPAIGN_DESTINATIONS,
  campaignDestination,
  screenDestination,
  screenDestinationsFor,
} from "@/app/destinations";
import { useI18n } from "@/i18n";
import { isAtMost, watchBreakpoint, type Breakpoint } from "@/lib/breakpoints";
import { isNotFound } from "@/lib/connectError";
import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { formatElapsed, useElapsedSeconds, useLiveSession } from "@/lib/useLiveSession";
import { isCampaignView } from "@/screens/campaign/views";

import "./appShell.css";

// The persistent app shell — sidebar + topbar — ported from the handoff
// ui_kits/glyphoxa-web/shell.jsx (its inline styles lifted onto .gx-shell* /
// .gx-topbar* in styles/components.css; what the shell has grown since lives in
// ./appShell.css) and driven by TanStack Router's active route rather than page
// state (ADR-0018). The Tenant lives in the path (/t/:tenantSlug/...); for the
// single-operator MVP (ADR-0039) it is a thin pass-through slug. The sidebar
// user footer shows the real signed-in operator (ADR-0016), passed in by the
// AuthGate that wraps the shell.
//
// The sidebar is campaign-first (ADR-0063 amendment): a section labelled with
// the Active Campaign's name lists that campaign's views on EVERY screen — not
// only while the Campaign screen is open — then Session, and, pinned at the
// bottom, the Tenant-wide destinations: Campaigns and Setup — and, for a
// Platform Admin only, Admin (ADR-0065). The lists themselves are
// app/destinations.ts.

// The desktop sidebar's hidden/shown choice, kept across reloads. It is a
// per-browser preference like the display language, and only the DESKTOP
// choice: on a drawer-sized viewport the sidebar always starts shut.
const SIDEBAR_PREF_KEY = "gx-sidebar-collapsed";

function readDesktopCollapsed(): boolean {
  try {
    return localStorage.getItem(SIDEBAR_PREF_KEY) === "1";
  } catch {
    return false; // storage unavailable (privacy mode): the default, shown
  }
}

function writeDesktopCollapsed(collapsed: boolean): void {
  try {
    if (collapsed) localStorage.setItem(SIDEBAR_PREF_KEY, "1");
    else localStorage.removeItem(SIDEBAR_PREF_KEY);
  } catch {
    // not persisted; the toggle still works for this visit
  }
}

// useAtMost follows one of the app's breakpoints (lib/breakpoints.ts — the
// same numbers the stylesheets use). False where matchMedia is absent (jsdom),
// which is the desktop layout.
function useAtMost(breakpoint: Breakpoint): boolean {
  const [atMost, setAtMost] = useState(() => isAtMost(breakpoint));
  useEffect(() => watchBreakpoint(breakpoint, setAtMost), [breakpoint]);
  return atMost;
}

// What Tab may land on inside the open drawer.
const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

// How often the pending-suggestions count is re-read while a Voice Session is
// live — that is when the Butler and the NPCs file them. Idle, the count only
// moves when the GM reviews one (which invalidates the read) or refocuses the
// tab.
const PROPOSALS_REFETCH_MS = 60_000;

// The pending-suggestions read is ONE cache entry shared with the Suggestions
// view and the graph overlay, and React Query runs a refetch with the options
// of the observer that starts it — on a window refocus the first one
// subscribed, which is the shell. So the shell must not be stricter than they
// are: with a bare retry:false here, one failed refetch put the shared query in
// error and the Suggestions view swapped its whole list for an error line.
// Failures are retried like any other read; only the settled "this Tenant has
// no campaign yet" answer (CodeNotFound) is not — backing off on that would
// just delay the empty state.
const proposalsRetry = (count: number, err: unknown) => !isNotFound(err) && count < 3;

export function AppShell(props: { tenantSlug: string; user: CurrentUser }) {
  return (
    // One tooltip provider for the chrome: moving between neighbouring topbar
    // icons shows each tooltip at once after the first.
    <TooltipProvider>
      <Shell {...props} />
    </TooltipProvider>
  );
}

function Shell({ tenantSlug, user }: { tenantSlug: string; user: CurrentUser }) {
  const { t } = useI18n();
  // Derived by hand rather than via Link activeProps so the mocked-router unit
  // test can observe it, and because the topbar needs the values anyway.
  const { screen, view } = useParams({ strict: false }) as { screen?: string; view?: string };
  const activeView = screen === "campaign" && isCampaignView(view) ? view : undefined;
  // Filtered by who is signed in: a non-admin on /admin gets the not-found
  // page, and the topbar must not title that page "Admin".
  const screens = screenDestinationsFor(user);
  const activeScreen = screenDestination(screen, user);

  // The Active Campaign names the sidebar section, the breadcrumb and the
  // browser tab. retry:false like every other observer of this shared cache
  // entry (the switcher, Setup, Session), so a fresh install's CodeNotFound
  // settles at once whichever of them mounted first. Rendered from DATA, which
  // React Query retains through a failed background refetch — a transient blip
  // never blanks a name already shown.
  const activeQ = useQuery(CampaignService.method.getActiveCampaign, {}, { retry: false });
  const campaignName = activeQ.data?.campaign?.name;
  const noCampaign = !activeQ.data && activeQ.isError && isNotFound(activeQ.error);

  // Live and attention signals, on every screen.
  const { live, startedMs } = useLiveSession();
  // The same cache entry the Suggestions view reads, so approving or dismissing
  // one there moves this count without a second source of truth. A count that
  // cannot be read is simply not shown; a count already shown survives a failed
  // refetch (it is rendered from retained data).
  const proposalsQ = useQuery(
    CampaignService.method.listKnowledgeProposals,
    {},
    {
      staleTime: PROPOSALS_REFETCH_MS,
      retry: proposalsRetry,
      refetchInterval: live ? PROPOSALS_REFETCH_MS : false,
    },
  );
  const pendingSuggestions = proposalsQ.data?.proposals.length ?? 0;

  // What the topbar and the browser tab call this page. Campaign views and the
  // Session screen are about ONE campaign, so they carry its name; Campaigns
  // and Setup are Tenant-wide and do not.
  const pageTitle = activeView
    ? t(campaignDestination(activeView).label)
    : activeScreen
      ? t(activeScreen.title)
      : null;
  const crumbCampaign = activeView || screen === "session" ? campaignName : undefined;

  // "<View> · <Campaign> · Glyphoxa" — with several Glyphoxa tabs open, an
  // all-"Glyphoxa" tab strip is a guessing game, and two campaigns' Cast tabs
  // are no better. The hook hands the tab back to the bare product name when
  // the shell unmounts: a lapsed session leaves by an in-app navigation to
  // /login, and that page must not inherit a campaign's name.
  useDocumentTitle(pageTitle, crumbCampaign);

  // A new screen or view starts at its top. .gx-content is the scroll
  // container (the shell itself never scrolls), so without this a GM who
  // scrolled down the wiki would land halfway down Cast.
  const contentRef = useRef<HTMLElement>(null);
  useEffect(() => {
    if (contentRef.current) contentRef.current.scrollTop = 0;
  }, [screen, view]);

  // Sidebar collapse (#88 slice 4). On narrow viewports the sidebar is an
  // off-canvas drawer the topbar toggle opens; on wide viewports the toggle
  // hides it for focus. State lives on data-collapsed so the CSS breakpoints
  // (and the unit test) read it without media-query support.
  //
  // The two layouts have different defaults, and crossing the breakpoint
  // applies the one that belongs to the side just entered: the drawer starts
  // shut, the desktop sidebar as the GM last left it (remembered across
  // reloads). Without that reset a window narrowed to phone width and widened
  // again kept the drawer's "shut" as the desktop state — a sidebar gone until
  // someone found the toggle. (matchMedia is absent in jsdom → the shell is on
  // the desktop layout under test unless a test stubs it.)
  const [drawer, setDrawer] = useState(() => isAtMost("drawer"));
  const [collapsed, setCollapsed] = useState(() => (isAtMost("drawer") ? true : readDesktopCollapsed()));
  useEffect(
    () =>
      watchBreakpoint("drawer", (nowDrawer) => {
        setDrawer(nowDrawer);
        setCollapsed(nowDrawer ? true : readDesktopCollapsed());
      }),
    [],
  );
  const drawerOpen = drawer && !collapsed;

  const toggleSidebar = () => {
    const next = !collapsed;
    setCollapsed(next);
    if (!drawer) writeDesktopCollapsed(next);
  };

  // The open drawer behaves like the modal layer it looks like: focus moves
  // into it, Tab stays in it, Escape and the scrim close it, the page behind
  // is inert (see .gx-main below), and closing hands focus back to the toggle
  // that opened it. Before this a keyboard user who opened it was left on the
  // toggle, behind the scrim, tabbing through a page they could not see.
  const sidebarRef = useRef<HTMLElement>(null);
  const toggleRef = useRef<HTMLButtonElement>(null);
  const wasDrawerOpen = useRef(false);
  useEffect(() => {
    if (drawerOpen && !wasDrawerOpen.current) {
      sidebarRef.current?.querySelector<HTMLElement>(FOCUSABLE)?.focus();
    } else if (!drawerOpen && wasDrawerOpen.current) {
      // Only if focus is still in the drawer (or nowhere): a dialog the GM
      // opened from the account menu has taken it on purpose.
      const active = document.activeElement;
      if (!active || active === document.body || sidebarRef.current?.contains(active)) {
        toggleRef.current?.focus();
      }
    }
    wasDrawerOpen.current = drawerOpen;
  }, [drawerOpen]);

  useEffect(() => {
    if (!drawerOpen) return;
    const onKey = (e: KeyboardEvent) => {
      // defaultPrevented: something on top of the drawer (the account menu, a
      // dialog, the palette) took this Escape for itself.
      if (e.key === "Escape" && !e.defaultPrevented) setCollapsed(true);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [drawerOpen]);

  const trapDrawerTab = (e: ReactKeyboardEvent<HTMLElement>) => {
    if (!drawerOpen || e.key !== "Tab") return;
    const root = sidebarRef.current;
    // A menu or dialog portalled out of the drawer reports its keys here too
    // (React events follow the component tree); those manage their own focus.
    if (!root || !root.contains(e.target as Node)) return;
    const focusables = root.querySelectorAll<HTMLElement>(FOCUSABLE);
    if (focusables.length === 0) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  };

  // The Ctrl+K palette, lifted so the topbar's search button can open it too —
  // a keyboard chord alone is undiscoverable and unreachable on touch.
  const [paletteOpen, setPaletteOpen] = useState(false);
  // Below the "stack" breakpoint the search button is down to its icon, and
  // only then does it need a tooltip to say what it is.
  const searchIconOnly = useAtMost("stack");

  // On the drawer breakpoint a nav tap should navigate AND shut the drawer —
  // otherwise it keeps covering the screen it just navigated to. On wide
  // viewports this is a no-op so the sidebar stays put.
  const closeDrawerOnMobile = () => {
    if (drawer) setCollapsed(true);
  };

  const sectionLabelId = useId();
  const sidebarId = useId();

  return (
    <div
      className="gx-shell"
      data-collapsed={collapsed ? "true" : undefined}
      data-drawer-open={drawerOpen ? "true" : undefined}
    >
      <button
        type="button"
        className="gx-shell__scrim"
        aria-hidden="true"
        tabIndex={-1}
        onClick={() => setCollapsed(true)}
      />
      <aside className="gx-sidebar" id={sidebarId} ref={sidebarRef} onKeyDown={trapDrawerTab}>
        {/* The brand is the way home: the tenant index, which opens the
            campaign's Overview. */}
        <Link
          className="gx-sidebar__brand"
          to="/t/$tenantSlug"
          params={{ tenantSlug }}
          aria-label={t("shell.home")}
          onClick={closeDrawerOnMobile}
        >
          <span className="gx-sidebar__sigil">
            <Dices size={17} />
          </span>
          <span className="gx-sidebar__wordmark gx-gradient-text">Glyphoxa</span>
        </Link>

        <nav className="gx-nav" aria-label={t("shell.navLabel")}>
          <div className="gx-nav__main">
            {/* The campaign's views — always rendered, whatever screen is open,
                so World wiki is one click from Session or Setup (ADR-0063
                amendment). The group is named by its heading: "Campaign" plus
                the Active Campaign's name. */}
            <div className="gx-nav__section" role="group" aria-labelledby={sectionLabelId}>
              <div className="gx-nav__section-head" id={sectionLabelId}>
                <span className="gx-overline">{t("shell.navCampaign")}</span>
                {campaignName ? (
                  // title: the name ellipsizes in a fixed-width sidebar.
                  <span className="gx-nav__section-name" title={campaignName}>
                    {campaignName}
                  </span>
                ) : noCampaign ? (
                  <span className="gx-nav__section-name gx-nav__section-name--none">
                    {t("shell.noCampaign")}
                  </span>
                ) : activeQ.isPending ? (
                  <span className="gx-skeleton" data-testid="nav-campaign-loading" />
                ) : null}
              </div>
              <div className="gx-nav__sub">
                {CAMPAIGN_DESTINATIONS.map((d) => {
                  const Icon = d.icon;
                  const label = t(d.label);
                  const isActive = activeView === d.view;
                  const count = d.view === "proposals" ? pendingSuggestions : 0;
                  return (
                    <Link
                      key={d.view}
                      className="gx-nav__subitem"
                      to="/t/$tenantSlug/$screen/$view"
                      params={{ tenantSlug, screen: "campaign", view: d.view }}
                      data-active={isActive ? "true" : undefined}
                      aria-current={isActive ? "page" : undefined}
                      // The count rides in the accessible name ("Suggestions,
                      // 3 pending"); the pill below is its visual twin.
                      aria-label={count > 0 ? t("shell.suggestionsPending", { label, count }) : undefined}
                      onClick={closeDrawerOnMobile}
                    >
                      <Icon size={18} aria-hidden="true" />
                      <span className="gx-nav__label">{label}</span>
                      {count > 0 && (
                        <span className="gx-nav__count" aria-hidden="true">
                          {count > 99 ? "99+" : count}
                        </span>
                      )}
                    </Link>
                  );
                })}
              </div>
            </div>

            <div className="gx-nav__rule" aria-hidden="true" />

            {screens.filter((d) => d.screen === "session").map((d) => {
              const Icon = d.icon;
              return (
                <Link
                  key={d.screen}
                  className="gx-nav__item"
                  to="/t/$tenantSlug/$screen"
                  params={{ tenantSlug, screen: d.screen }}
                  data-active={screen === d.screen ? "true" : undefined}
                  aria-current={screen === d.screen ? "page" : undefined}
                  onClick={closeDrawerOnMobile}
                >
                  <Icon size={18} aria-hidden="true" />
                  <span className="gx-nav__label">{t(d.label)}</span>
                  {live && <NavLiveMarker startedMs={startedMs} />}
                </Link>
              );
            })}
          </div>

          {/* Tenant-wide destinations, pinned under the scrolling list. */}
          <div className="gx-nav__foot">
            {screens.filter((d) => d.screen !== "session").map((d) => {
              const Icon = d.icon;
              return (
                <Link
                  key={d.screen}
                  className="gx-nav__item"
                  to="/t/$tenantSlug/$screen"
                  params={{ tenantSlug, screen: d.screen }}
                  data-active={screen === d.screen ? "true" : undefined}
                  aria-current={screen === d.screen ? "page" : undefined}
                  onClick={closeDrawerOnMobile}
                >
                  <Icon size={18} aria-hidden="true" />
                  <span className="gx-nav__label">{t(d.label)}</span>
                </Link>
              );
            })}
          </div>
        </nav>

        <SidebarUser user={user} />
      </aside>

      {/* inert while the drawer is open: it is covered by the scrim, so it
          must be out of the tab order and out of a screen reader's reach too. */}
      <div className="gx-main" inert={drawerOpen}>
        <header className="gx-topbar">
          {/* Tooltip, not title: it also shows on keyboard focus. (Not an
              IconButton: that one keeps its tooltip shut while aria-expanded
              is true, which for this toggle is whenever the sidebar is open.) */}
          <Tooltip content={t("shell.toggleSidebar")} side="bottom">
            <Button
              ref={toggleRef}
              variant="ghost"
              size="sm"
              className="gx-topbar__toggle"
              aria-label={t("shell.toggleSidebar")}
              aria-expanded={!collapsed}
              aria-controls={sidebarId}
              onClick={toggleSidebar}
              iconStart={<PanelLeft size={18} />}
            />
          </Tooltip>
          <div className="gx-topbar__divider" />
          <div className="gx-topbar__titles">
            {pageTitle && crumbCampaign ? (
              // "<Campaign> / <View>": which campaign this page is about, then
              // where in it. The campaign crumb leads to its Overview — except
              // on the Overview itself, where a link to the current page would
              // be a dead control.
              <nav className="gx-crumbs" aria-label={t("shell.breadcrumb")}>
                <ol>
                  <li className="gx-crumbs__campaign">
                    {activeView === "overview" ? (
                      <span title={crumbCampaign}>{crumbCampaign}</span>
                    ) : (
                      <Link
                        to="/t/$tenantSlug/$screen/$view"
                        params={{ tenantSlug, screen: "campaign", view: "overview" }}
                        title={crumbCampaign}
                      >
                        {crumbCampaign}
                      </Link>
                    )}
                  </li>
                  <li className="gx-crumbs__page" aria-current="page">
                    <span className="gx-crumbs__sep" aria-hidden="true">
                      /
                    </span>
                    <span className="gx-topbar__title">{pageTitle}</span>
                  </li>
                </ol>
              </nav>
            ) : (
              <div className="gx-topbar__title">{pageTitle ?? "Glyphoxa"}</div>
            )}
          </div>
          {/* A Voice Session is live and the GM is somewhere else: say so, and
              offer the way back. On the Session screen itself the screen's own
              controls already do. */}
          {live && screen !== "session" && <LivePill tenantSlug={tenantSlug} startedMs={startedMs} />}
          {/* The palette's visible door. On a wide bar it reads as a search
              field with its shortcut on a key cap; below the "stack"
              breakpoint it is down to the icon (the words are clipped, not
              removed, so its accessible name never changes) and a tooltip
              says what it is. */}
          <Tooltip
            content={t("palette.openSearch", { shortcut: paletteShortcut(t) })}
            side="bottom"
            disabled={!searchIconOnly}
          >
            <button
              type="button"
              className="gx-topbar__search"
              aria-keyshortcuts={isApplePlatform() ? "Meta+K" : "Control+K"}
              aria-haspopup="dialog"
              onClick={() => setPaletteOpen(true)}
            >
              <Search size={16} aria-hidden="true" />
              <span className="gx-topbar__search-label">{t("palette.searchButton")}</span>
              <PaletteShortcut className="gx-topbar__search-kbd" decorative />
            </button>
          </Tooltip>
          {/* The Active-Campaign switcher lives on every screen (#266a): the
              titles' flex:1 pushes it to the topbar's right edge. The display
              language used to sit beside it; it is in the account menu now
              (sidebar footer), where a once-chosen preference belongs. */}
          <CampaignSwitcher />
        </header>

        {/* "You are offline" / "Can't reach Glyphoxa" (#695), between the bar
            and the screen so it is seen from everywhere and covers nothing. */}
        <ConnectionBanner />

        <main className="gx-content" ref={contentRef}>
          <Outlet />
          {/* Impressum / Datenschutz / Nutzungsbedingungen on every app screen
              (#518) — German law wants them reachable from anywhere, not
              buried in a settings page. Inside the scrolling area, after the
              screen: as a fixed strip under it, it cost every screen a row of
              height and sat on top of the last lines of a long page on a
              phone. (The account menu links the same three documents.) */}
          <LegalFooter />
        </main>
      </div>

      {/* The Ctrl/Cmd+K palette (#591). Mounted in the shell like the Toaster —
          the global-overlay convention — so it is reachable from every
          authenticated screen and never renders for an unauthenticated visitor
          (the shell sits inside the AuthGate). Closed it renders nothing and
          fires no RPCs, so screen tests stay clean. */}
      <CommandPalette
        open={paletteOpen}
        onOpenChange={setPaletteOpen}
        platformAdmin={user.isPlatformAdmin}
      />

      {/* Single toast host for the whole app (ADR-0017: sonner). Mounted here, not
          in Providers, so the screen unit tests that render without the shell get a
          clean DOM and their deterministic inline cues instead of toast portals.
          On a drawer-sized viewport toasts come in at the top, with a close
          button: at the bottom they cover the controls a thumb is on (a chat
          composer, a Save bar), and there is no hover to pause them, so the
          GM needs a way to put one away. */}
      <Toaster
        theme="dark"
        richColors
        position={drawer ? "top-center" : "bottom-right"}
        closeButton={drawer}
        toastOptions={{ closeButtonAriaLabel: t("common.dismiss") }}
      />
    </div>
  );
}

// The clocks are their own components on purpose: useElapsedSeconds re-renders
// its caller every second, and that caller must not be the whole shell.

// NavLiveMarker is the live cue on the sidebar's Session item: a pulsing dot
// and the session's running time. The word "Live" is there for screen readers;
// sighted users read the dot.
function NavLiveMarker({ startedMs }: { startedMs: number | null }) {
  const { t } = useI18n();
  const elapsed = useElapsedSeconds(startedMs);
  return (
    <span className="gx-nav__live" data-testid="nav-live">
      <span className="gx-live-dot" aria-hidden="true" />
      <span className="gx-shell__sr">{t("shell.live")}</span>
      {startedMs != null && <span className="gx-nav__live-time">{formatElapsed(elapsed)}</span>}
    </span>
  );
}

// LivePill is the topbar's "Live 01:12:40 · Open session" link to the Session
// screen. As the bar narrows it sheds its parts from the right — the words,
// then the clock, then "Live", down to the pulsing dot — but only VISUALLY
// (see the width steps in appShell.css): the link keeps its full accessible
// name at every width, and the title gives a pointer user the same on hover.
function LivePill({ tenantSlug, startedMs }: { tenantSlug: string; startedMs: number | null }) {
  const { t } = useI18n();
  const elapsed = useElapsedSeconds(startedMs);
  return (
    <Link
      className="gx-live-pill"
      to="/t/$tenantSlug/$screen"
      params={{ tenantSlug, screen: "session" }}
      title={`${t("shell.live")} · ${t("shell.openSession")}`}
    >
      <span className="gx-live-dot" aria-hidden="true" />
      <span className="gx-live-pill__label">{t("shell.live")}</span>
      {startedMs != null && <span className="gx-live-pill__time">{formatElapsed(elapsed)}</span>}
      <span className="gx-live-pill__more">
        <span aria-hidden="true">·</span> {t("shell.openSession")}
      </span>
    </Link>
  );
}
